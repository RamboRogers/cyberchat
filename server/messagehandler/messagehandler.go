package messagehandler

import (
	"bytes"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cyberchat/server/db"
	"cyberchat/server/discovery"
	"cyberchat/server/messages"
	"cyberchat/server/peers"
	"cyberchat/server/websocket"

	"github.com/google/uuid"
)

// Handler handles all message-related operations
type Handler struct {
	db          *db.DB
	guid        string
	privateKey  *rsa.PrivateKey
	discovery   *discovery.Service
	wsManager   *websocket.Manager
	peerMgr     *peers.Manager
	OnMessage   func(*messages.Message)
	failedPeers sync.Map // Tracks recently failed peers with their failure time
}

// New creates a new message handler
func New(db *db.DB, guid string, privateKey *rsa.PrivateKey, discovery *discovery.Service, wsManager *websocket.Manager, peerMgr *peers.Manager) *Handler {
	return &Handler{
		db:         db,
		guid:       guid,
		privateKey: privateKey,
		discovery:  discovery,
		wsManager:  wsManager,
		peerMgr:    peerMgr,
	}
}

// ProcessMessage handles an incoming message internally and returns a delivery report
func (h *Handler) ProcessMessage(msg *messages.Message, sourceIP string) *messages.MessageDeliveryReport {
	// Create delivery report
	report := &messages.MessageDeliveryReport{
		MessageID:    msg.ID,
		DeliveryTime: time.Now(),
		PeerStatuses: make([]messages.MessageDeliveryStatus, 0),
	}

	// Check if we've seen this message ID before
	if h.db != nil {
		exists, _ := h.db.MessageExists(msg.ID)
		if exists {
			log.Printf("[Message] Skipping duplicate message %s", msg.ID)
			return report
		}
	}

	// Store message with source IP before any processing
	if err := h.db.SaveMessage(msg, sourceIP); err != nil {
		log.Printf("Failed to store message: %v", err)
	}

	// Only attempt peer discovery and broadcast for messages we originate
	if msg.SenderGUID == h.guid {
		// Log message if handler is set
		if h.OnMessage != nil {
			h.OnMessage(msg)
		}

		// Convert to web message format with string content
		webMsg := &messages.WebMessage{
			ID:           msg.ID,
			SenderGUID:   msg.SenderGUID,
			ReceiverGUID: msg.ReceiverGUID,
			Type:         msg.Type,
			Scope:        msg.Scope,
			Content:      string(msg.Content),
			Timestamp:    msg.Timestamp,
		}

		// Broadcast to web clients
		h.wsManager.Broadcast(struct {
			Type    string               `json:"type"`
			Content *messages.WebMessage `json:"content"`
		}{
			Type:    "message",
			Content: webMsg,
		})

		// Log initial message info
		log.Printf("[Message] Processing %s message (ID: %s) from %s", msg.Scope, msg.ID, msg.SenderGUID)

		// Send initial delivery status to web clients
		h.wsManager.Broadcast(struct {
			Type    string `json:"type"`
			Content struct {
				MessageID string `json:"message_id"`
				Status    string `json:"status"`
				Details   string `json:"details"`
			} `json:"content"`
		}{
			Type: "delivery_status",
			Content: struct {
				MessageID string `json:"message_id"`
				Status    string `json:"status"`
				Details   string `json:"details"`
			}{
				MessageID: msg.ID,
				Status:    "processing",
				Details:   "Starting message delivery...",
			},
		})

		// Handle message forwarding based on scope
		if msg.Scope == messages.ScopeBroadcast {
			// Get peers exclusively from manager
			managerPeers := h.peerMgr.GetPeers()
			var broadcastPeers []discovery.Peer

			// Convert manager peers to discovery peers for compatibility
			for _, mgrPeer := range managerPeers {
				if mgrPeer.GUID != msg.SenderGUID {
					peer := discovery.Peer{
						GUID: mgrPeer.GUID,
						Name: mgrPeer.Name,
						IP:   net.ParseIP(mgrPeer.IPAddress),
						Port: mgrPeer.Port,
					}
					broadcastPeers = append(broadcastPeers, peer)
				}
			}

			report.TotalPeers = len(broadcastPeers)

			if report.TotalPeers == 0 {
				log.Printf("[Message] No other peers available for broadcast message %s", msg.ID)
				// Notify web clients about empty peer list
				h.wsManager.Broadcast(struct {
					Type    string `json:"type"`
					Content struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
					} `json:"content"`
				}{
					Type: "delivery_status",
					Content: struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
					}{
						MessageID: msg.ID,
						Status:    "completed",
						Details:   "No peers available for broadcast",
					},
				})
			} else {
				log.Printf("[Message] Broadcasting to %d peers", report.TotalPeers)

				// Send initial broadcast status
				h.wsManager.Broadcast(struct {
					Type    string `json:"type"`
					Content struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						Total     int    `json:"total"`
					} `json:"content"`
				}{
					Type: "delivery_status",
					Content: struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						Total     int    `json:"total"`
					}{
						MessageID: msg.ID,
						Status:    "broadcasting",
						Details:   fmt.Sprintf("Broadcasting to %d peers...", report.TotalPeers),
						Total:     report.TotalPeers,
					},
				})

				// Forward to all peers
				for _, peer := range broadcastPeers {
					// Create a copy of the message with this peer as receiver
					peerMsg := *msg
					peerMsg.ReceiverGUID = peer.GUID
					status := h.ForwardMessageToPeer(&peerMsg, &peer)
					report.PeerStatuses = append(report.PeerStatuses, status)

					if status.Success {
						report.Succeeded++
						log.Printf("[Message] ✓ Successfully delivered to %s (%s)", peer.Name, peer.GUID)
					} else {
						report.Failed++
						log.Printf("[Message] ✗ Failed to deliver to %s (%s): %s", peer.Name, peer.GUID, status.Error)
						h.handleDeliveryFailure(&peer, &status)
					}

					// Send per-peer delivery status
					h.wsManager.Broadcast(struct {
						Type    string `json:"type"`
						Content struct {
							MessageID string `json:"message_id"`
							PeerGUID  string `json:"peer_guid"`
							PeerName  string `json:"peer_name"`
							Success   bool   `json:"success"`
							Error     string `json:"error,omitempty"`
							Progress  struct {
								Succeeded int `json:"succeeded"`
								Failed    int `json:"failed"`
								Total     int `json:"total"`
							} `json:"progress"`
						} `json:"content"`
					}{
						Type: "delivery_progress",
						Content: struct {
							MessageID string `json:"message_id"`
							PeerGUID  string `json:"peer_guid"`
							PeerName  string `json:"peer_name"`
							Success   bool   `json:"success"`
							Error     string `json:"error,omitempty"`
							Progress  struct {
								Succeeded int `json:"succeeded"`
								Failed    int `json:"failed"`
								Total     int `json:"total"`
							} `json:"progress"`
						}{
							MessageID: msg.ID,
							PeerGUID:  peer.GUID,
							PeerName:  peer.Name,
							Success:   status.Success,
							Error:     status.Error,
							Progress: struct {
								Succeeded int `json:"succeeded"`
								Failed    int `json:"failed"`
								Total     int `json:"total"`
							}{
								Succeeded: report.Succeeded,
								Failed:    report.Failed,
								Total:     report.TotalPeers,
							},
						},
					})
				}

				// Send final delivery status
				successRate := float64(report.Succeeded) / float64(report.TotalPeers) * 100
				h.wsManager.Broadcast(struct {
					Type    string `json:"type"`
					Content struct {
						MessageID string  `json:"message_id"`
						Status    string  `json:"status"`
						Details   string  `json:"details"`
						Success   float64 `json:"success_rate"`
						Final     struct {
							Succeeded int `json:"succeeded"`
							Failed    int `json:"failed"`
							Total     int `json:"total"`
						} `json:"final"`
					} `json:"content"`
				}{
					Type: "delivery_final",
					Content: struct {
						MessageID string  `json:"message_id"`
						Status    string  `json:"status"`
						Details   string  `json:"details"`
						Success   float64 `json:"success_rate"`
						Final     struct {
							Succeeded int `json:"succeeded"`
							Failed    int `json:"failed"`
							Total     int `json:"total"`
						} `json:"final"`
					}{
						MessageID: msg.ID,
						Status:    "completed",
						Details:   fmt.Sprintf("Delivery complete: %d/%d successful (%.1f%%)", report.Succeeded, report.TotalPeers, successRate),
						Success:   successRate,
						Final: struct {
							Succeeded int `json:"succeeded"`
							Failed    int `json:"failed"`
							Total     int `json:"total"`
						}{
							Succeeded: report.Succeeded,
							Failed:    report.Failed,
							Total:     report.TotalPeers,
						},
					},
				})
			}
		} else if msg.Scope == messages.ScopePrivate {
			report.TotalPeers = 1
			log.Printf("[Message] Sending private message to %s", msg.ReceiverGUID)

			// Send initial private message status
			h.wsManager.Broadcast(struct {
				Type    string `json:"type"`
				Content struct {
					MessageID string `json:"message_id"`
					Status    string `json:"status"`
					Details   string `json:"details"`
					PeerGUID  string `json:"peer_guid"`
				} `json:"content"`
			}{
				Type: "delivery_status",
				Content: struct {
					MessageID string `json:"message_id"`
					Status    string `json:"status"`
					Details   string `json:"details"`
					PeerGUID  string `json:"peer_guid"`
				}{
					MessageID: msg.ID,
					Status:    "sending",
					Details:   fmt.Sprintf("Sending private message to %s...", msg.ReceiverGUID),
					PeerGUID:  msg.ReceiverGUID,
				},
			})

			// Get peer from manager first
			var peer *discovery.Peer
			if mgrPeer, exists := h.peerMgr.GetPeer(msg.ReceiverGUID); exists {
				peer = &discovery.Peer{
					GUID: mgrPeer.GUID,
					Name: mgrPeer.Name,
					IP:   net.ParseIP(mgrPeer.IPAddress),
					Port: mgrPeer.Port,
				}
			}

			if peer != nil {
				status := h.ForwardMessageToPeer(msg, peer)
				report.PeerStatuses = append(report.PeerStatuses, status)

				if status.Success {
					report.Succeeded++
					log.Printf("[Message] ✓ Successfully delivered private message to %s (%s)", peer.Name, peer.GUID)
				} else {
					report.Failed++
					log.Printf("[Message] ✗ Failed to deliver private message to %s (%s): %s", peer.Name, peer.GUID, status.Error)
					h.handleDeliveryFailure(peer, &status)
				}

				// Send final private message status
				h.wsManager.Broadcast(struct {
					Type    string `json:"type"`
					Content struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						PeerGUID  string `json:"peer_guid"`
						Success   bool   `json:"success"`
						Error     string `json:"error,omitempty"`
					} `json:"content"`
				}{
					Type: "delivery_final",
					Content: struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						PeerGUID  string `json:"peer_guid"`
						Success   bool   `json:"success"`
						Error     string `json:"error,omitempty"`
					}{
						MessageID: msg.ID,
						Status:    "completed",
						Details:   fmt.Sprintf("Private message delivery to %s %s", peer.Name, map[bool]string{true: "succeeded", false: "failed"}[status.Success]),
						PeerGUID:  peer.GUID,
						Success:   status.Success,
						Error:     status.Error,
					},
				})
			} else {
				status := messages.MessageDeliveryStatus{
					PeerGUID: msg.ReceiverGUID,
					PeerName: "Unknown",
					Success:  false,
					Error:    "Peer not found in active peers list",
					Time:     time.Now(),
				}
				report.PeerStatuses = append(report.PeerStatuses, status)
				report.Failed++
				log.Printf("[Message] ✗ Failed to deliver private message: peer %s not found", msg.ReceiverGUID)

				// Send failure status for unknown peer
				h.wsManager.Broadcast(struct {
					Type    string `json:"type"`
					Content struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						PeerGUID  string `json:"peer_guid"`
						Error     string `json:"error"`
					} `json:"content"`
				}{
					Type: "delivery_final",
					Content: struct {
						MessageID string `json:"message_id"`
						Status    string `json:"status"`
						Details   string `json:"details"`
						PeerGUID  string `json:"peer_guid"`
						Error     string `json:"error"`
					}{
						MessageID: msg.ID,
						Status:    "failed",
						Details:   fmt.Sprintf("Failed to deliver private message: peer %s not found", msg.ReceiverGUID),
						PeerGUID:  msg.ReceiverGUID,
						Error:     "Peer not found in active peers list",
					},
				})
			}
		}
	} else {
		// For messages from other peers, just notify web clients
		webMsg := &messages.WebMessage{
			ID:           msg.ID,
			SenderGUID:   msg.SenderGUID,
			ReceiverGUID: msg.ReceiverGUID,
			Type:         msg.Type,
			Scope:        msg.Scope,
			Content:      string(msg.Content),
			Timestamp:    msg.Timestamp,
		}

		h.wsManager.Broadcast(struct {
			Type    string               `json:"type"`
			Content *messages.WebMessage `json:"content"`
		}{
			Type:    "message",
			Content: webMsg,
		})

		log.Printf("[Message] Received %s message (ID: %s) from %s", msg.Scope, msg.ID, msg.SenderGUID)
	}

	// Log overall delivery status with more detail
	if report.TotalPeers > 0 {
		successRate := float64(report.Succeeded) / float64(report.TotalPeers) * 100
		log.Printf("[Message] Delivery complete for %s (ID: %s)", msg.Scope, msg.ID)
		log.Printf("[Message] Results: %d/%d delivered (%.1f%%) with %d failures",
			report.Succeeded, report.TotalPeers, successRate, report.Failed)

		if report.Failed > 0 {
			log.Printf("[Message] Failed deliveries:")
			for _, status := range report.PeerStatuses {
				if !status.Success {
					log.Printf("[Message]   - %s (%s): %s", status.PeerName, status.PeerGUID, status.Error)
				}
			}
		}
	}

	// Add delivery summary to the report for client display
	if report.TotalPeers > 0 {
		summary := fmt.Sprintf("Delivered to %d/%d peers (%.1f%% success)",
			report.Succeeded, report.TotalPeers,
			float64(report.Succeeded)/float64(report.TotalPeers)*100)
		report.Summary = summary
	}

	return report
}

// ForwardMessageToPeer forwards a message to a specific peer and returns the delivery status
func (h *Handler) ForwardMessageToPeer(msg *messages.Message, peer *discovery.Peer) messages.MessageDeliveryStatus {
	status := messages.MessageDeliveryStatus{
		PeerGUID: peer.GUID,
		PeerName: peer.Name,
		Time:     time.Now(),
	}

	// Get peer's public key
	pubKeyBytes, err := h.discovery.GetPeerPublicKey(*peer)
	if err != nil {
		status.Success = false
		status.Error = fmt.Sprintf("Failed to get public key: %v", err)
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	// Parse public key
	block, _ := pem.Decode(pubKeyBytes)
	if block == nil {
		status.Success = false
		status.Error = "Failed to decode public key"
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	receiverPubKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		status.Success = false
		status.Error = fmt.Sprintf("Failed to parse public key: %v", err)
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	// Encrypt message for peer
	encryptedMsg, err := msg.Encrypt(receiverPubKey)
	if err != nil {
		status.Success = false
		status.Error = fmt.Sprintf("Failed to encrypt message: %v", err)
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	// Create HTTP client with short timeout
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			DialContext: (&net.Dialer{
				Timeout: 500 * time.Millisecond,
			}).DialContext,
			TLSHandshakeTimeout: 500 * time.Millisecond,
		},
		Timeout: 500 * time.Millisecond,
	}

	// Marshal encrypted message
	msgData, err := json.Marshal(encryptedMsg)
	if err != nil {
		status.Success = false
		status.Error = fmt.Sprintf("Failed to marshal message: %v", err)
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	// Forward to peer's server
	url := fmt.Sprintf("https://%s:%d/api/v1/message", peer.IP, peer.Port)
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(msgData))
	if err != nil {
		status.Success = false
		status.Error = fmt.Sprintf("Failed to send message: %v", err)
		h.handleDeliveryFailure(peer, &status)
		return status
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		status.Success = false
		status.Error = fmt.Sprintf("Peer returned error (HTTP %d): %s", resp.StatusCode, string(body))
		h.handleDeliveryFailure(peer, &status)
		return status
	}

	status.Success = true
	return status
}

// handleDeliveryFailure handles a failed message delivery by removing the peer from memory
func (h *Handler) handleDeliveryFailure(peer *discovery.Peer, status *messages.MessageDeliveryStatus) {
	// Check if peer is already marked as failed recently
	if failureTime, exists := h.failedPeers.Load(peer.GUID); exists {
		// If failure was recorded in last 5 seconds, skip duplicate handling
		if time.Since(failureTime.(time.Time)) < 5*time.Second {
			return
		}
	}

	// Get peer name from manager before removal
	peerName := peer.Name
	if mgrPeer, exists := h.peerMgr.GetPeer(peer.GUID); exists {
		peerName = mgrPeer.Name
	}

	// Remove peer from both discovery and manager
	h.discovery.RemoveInactivePeer(peer.GUID)
	h.peerMgr.RemoveInactivePeer(peer.GUID)

	// Add to failed peers map with current timestamp
	h.failedPeers.Store(peer.GUID, time.Now())

	// Log the removal
	log.Printf("[Message] Removing unreachable peer from active list: %s (%s) - %s",
		peerName, peer.GUID, status.Error)

	// Notify web clients about peer removal with historical name
	h.wsManager.Broadcast(struct {
		Type    string `json:"type"`
		Content struct {
			GUID   string `json:"guid"`
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"content"`
	}{
		Type: "peer_offline",
		Content: struct {
			GUID   string `json:"guid"`
			Name   string `json:"name"`
			Reason string `json:"reason"`
		}{
			GUID:   peer.GUID,
			Name:   peerName,
			Reason: status.Error,
		},
	})

	// Send system message to web clients
	h.wsManager.Broadcast(struct {
		Type    string               `json:"type"`
		Content *messages.WebMessage `json:"content"`
	}{
		Type: "message",
		Content: &messages.WebMessage{
			ID:         uuid.New().String(),
			Type:       "system",
			SenderGUID: "system",
			Content:    fmt.Sprintf("Peer %s (%s) went offline: %s", peerName, peer.GUID, status.Error),
			Timestamp:  time.Now(),
		},
	})
}

// discoverPeerFromMessage attempts to discover a peer from an incoming message
func (h *Handler) discoverPeerFromMessage(msg *messages.Message, sourceIP string) {
	// Skip if message is from ourselves
	if msg.SenderGUID == h.guid {
		return
	}

	// Check if we already know this peer
	if mgrPeer, exists := h.peerMgr.GetPeer(msg.SenderGUID); exists {
		// Update last seen time by re-saving the peer
		h.peerMgr.HandleUpdate(peers.Peer{
			GUID:      mgrPeer.GUID,
			Name:      mgrPeer.Name,
			IPAddress: mgrPeer.IPAddress,
			Port:      mgrPeer.Port,
		})
		return
	}

	// Check if peer is in cooldown period
	if failureTime, ok := h.failedPeers.Load(msg.SenderGUID); ok {
		if time.Since(failureTime.(time.Time)) < 5*time.Minute {
			log.Printf("[Discovery] Skipping peer discovery for %s - in cooldown period after recent failure",
				msg.SenderGUID)
			return
		}
		// Cooldown period expired, remove from failed peers map
		h.failedPeers.Delete(msg.SenderGUID)
	}

	// Extract IP from source address
	ip := sourceIP
	if strings.Contains(ip, ":") {
		ip = strings.Split(ip, ":")[0]
	}

	// Try multiple common ports for discovery
	commonPorts := []int{7331, 7332, 7333, 7334, 7335}
	var discoveryError error

	for _, port := range commonPorts {
		// Try to get peer info from whoami endpoint
		url := fmt.Sprintf("https://%s:%d/api/v1/whoami", ip, port)
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
				DialContext: (&net.Dialer{
					Timeout: 1000 * time.Millisecond,
				}).DialContext,
				TLSHandshakeTimeout: 1000 * time.Millisecond,
			},
			Timeout: 1000 * time.Millisecond,
		}

		resp, err := client.Get(url)
		if err != nil {
			discoveryError = err
			continue // Try next port
		}

		defer resp.Body.Close()

		var peerInfo struct {
			GUID string `json:"guid"`
			Name string `json:"name"`
			Port int    `json:"port"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&peerInfo); err != nil {
			discoveryError = fmt.Errorf("failed to decode peer info: %v", err)
			continue
		}

		// Verify the GUID matches the message sender
		if peerInfo.GUID != msg.SenderGUID {
			discoveryError = fmt.Errorf("GUID mismatch: message claims %s but whoami reports %s",
				msg.SenderGUID, peerInfo.GUID)
			continue
		}

		// Successfully discovered peer
		log.Printf("[Discovery] Found peer %s (%s) at %s:%d", peerInfo.Name, peerInfo.GUID, ip, peerInfo.Port)

		// Save to database
		if h.db != nil {
			if err := h.db.SavePeer(peerInfo.GUID, ip, peerInfo.Port, nil, peerInfo.Name); err != nil {
				log.Printf("[Discovery] DB save failed: %v", err)
			}
		}

		// Create peer object
		peer := peers.Peer{
			GUID:      peerInfo.GUID,
			Name:      peerInfo.Name,
			IPAddress: ip,
			Port:      peerInfo.Port,
			LastSeen:  time.Now(),
		}

		// Update peer manager
		h.peerMgr.HandleUpdate(peer)

		// Notify web clients about new peer
		h.wsManager.Broadcast(struct {
			Type    string `json:"type"`
			Content struct {
				GUID      string `json:"guid"`
				Name      string `json:"name"`
				IPAddress string `json:"ip_address"`
				Port      int    `json:"port"`
				Status    string `json:"status"`
			} `json:"content"`
		}{
			Type: "peer_discovered",
			Content: struct {
				GUID      string `json:"guid"`
				Name      string `json:"name"`
				IPAddress string `json:"ip_address"`
				Port      int    `json:"port"`
				Status    string `json:"status"`
			}{
				GUID:      peer.GUID,
				Name:      peer.Name,
				IPAddress: peer.IPAddress,
				Port:      peer.Port,
				Status:    "active",
			},
		})

		return // Successfully discovered peer
	}

	// If we get here, all discovery attempts failed
	log.Printf("[Discovery] Failed to discover peer %s at %s: %v", msg.SenderGUID, ip, discoveryError)

	// Add to failed peers map with current timestamp
	h.failedPeers.Store(msg.SenderGUID, time.Now())

	// Notify web clients about discovery failure
	h.wsManager.Broadcast(struct {
		Type    string `json:"type"`
		Content struct {
			GUID   string `json:"guid"`
			IP     string `json:"ip"`
			Error  string `json:"error"`
			Status string `json:"status"`
		} `json:"content"`
	}{
		Type: "peer_discovery_failed",
		Content: struct {
			GUID   string `json:"guid"`
			IP     string `json:"ip"`
			Error  string `json:"error"`
			Status string `json:"status"`
		}{
			GUID:   msg.SenderGUID,
			IP:     ip,
			Error:  discoveryError.Error(),
			Status: "unreachable",
		},
	})
}

// HandleMessage processes an HTTP message request
func (h *Handler) HandleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Get source IP
	sourceIP := r.RemoteAddr
	if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		sourceIP = forwardedFor
	}

	var report *messages.MessageDeliveryReport

	// Try to parse as an encrypted message first
	var encMsg messages.EncryptedMessage
	if err := json.Unmarshal(body, &encMsg); err == nil {
		// Validate this message is for us
		if encMsg.ReceiverGUID != h.guid {
			log.Printf("Message not intended for this server (got %s, expected %s)", encMsg.ReceiverGUID, h.guid)
			http.Error(w, "Message not intended for this server", http.StatusBadRequest)
			return
		}

		// Decrypt the message
		message, err := encMsg.Decrypt(h.privateKey)
		if err != nil {
			log.Printf("Failed to decrypt message: %v", err)
			http.Error(w, "Failed to decrypt message", http.StatusInternalServerError)
			return
		}

		log.Printf("Successfully decrypted message from %s", message.SenderGUID)

		// Only try to discover peer if message is not from us
		if message.SenderGUID != h.guid {
			// Try to discover peer from message
			h.discoverPeerFromMessage(message, sourceIP)
		}

		// Process the decrypted message
		report = h.ProcessMessage(message, sourceIP)
	} else {
		// If not encrypted, try to parse as a web client message
		var msg struct {
			Type         string `json:"type"`
			Content      string `json:"content"`
			ReceiverGUID string `json:"receiver_guid"`
			Scope        string `json:"scope"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "Failed to parse message", http.StatusBadRequest)
			return
		}

		// Create a new message from the web client data
		message := messages.NewWebMessage(h.guid, msg.ReceiverGUID, messages.MessageType(msg.Type), msg.Content)
		message.Scope = messages.MessageScope(msg.Scope)

		// Process the message
		report = h.ProcessMessage(message, sourceIP)
	}

	// Return delivery report
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(report)
}
