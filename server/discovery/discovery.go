package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberchat/server/db"

	"github.com/hashicorp/mdns"
)

const (
	serviceName          = "_cyberchat._tcp"
	domain               = "local."
	ttl                  = 10               // seconds
	activePeerTimeout    = 10 * time.Minute // Time after which a peer is considered inactive
	networkCheckInterval = 30 * time.Second // How often to check for network changes
)

// Service handles peer discovery using mDNS
type Service struct {
	guid      string
	port      int
	publicKey []byte
	server    *mdns.Server
	peers     map[string]*Peer // Only contains active peers
	updates   chan Peer
	mu        sync.RWMutex
	db        *db.DB
	name      string
	currentIP net.IP
	ctx       context.Context
	cancel    context.CancelFunc
}

// Peer represents a discovered peer
type Peer struct {
	GUID      string
	Port      int
	IP        net.IP
	PublicKey []byte
	Name      string
	LastSeen  time.Time
}

// New creates a new discovery service
func New(guid string, port int, publicKey []byte, db *db.DB, name string) (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		guid:      guid,
		port:      port,
		publicKey: publicKey,
		peers:     make(map[string]*Peer),
		updates:   make(chan Peer, 100),
		db:        db,
		name:      name,
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// getLocalIP gets the current best local IP for broadcasting
func (s *Service) getLocalIP() (net.IP, error) {
	// Get all network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	// First try to find a suitable interface
	var bestIface net.Interface
	for _, iface := range ifaces {
		// Skip interfaces that are down or loopback
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		// Look for a valid IPv4 address on this interface
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					bestIface = iface
					log.Printf("[Discovery] Selected network interface: %s (%s)", iface.Name, ip4.String())
					break
				}
			}
		}
		if bestIface.Name != "" {
			break
		}
	}

	// If we found a suitable interface, use it to get the IP
	if bestIface.Name != "" {
		addrs, err := bestIface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("failed to get addresses for interface %s: %w", bestIface.Name, err)
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					return ip4, nil
				}
			}
		}
	}

	// Fallback to old method if no suitable interface found
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface addresses: %w", err)
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4, nil
			}
		}
	}

	return nil, fmt.Errorf("no suitable local IP found")
}

// restartMDNS restarts the mDNS server with new IP
func (s *Service) restartMDNS() error {
	if s.server != nil {
		s.server.Shutdown()
	}

	host, _ := os.Hostname()

	// Get current local IP and interface
	localIP, err := s.getLocalIP()
	if err != nil {
		return err
	}
	s.currentIP = localIP

	// Find the interface for this IP
	var selectedIface *net.Interface
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					if ipnet.IP.Equal(localIP) {
						selectedIface = &iface
						break
					}
				}
			}
			if selectedIface != nil {
				break
			}
		}
	}

	log.Printf("[Discovery] Starting/Restarting mDNS with IP: %s", localIP)

	// Include IP in text record
	info := []string{
		"id=" + s.guid,
		fmt.Sprintf("port=%d", s.port),
		fmt.Sprintf("name=%s", s.name),
		fmt.Sprintf("ip=%s", localIP.String()),
	}

	service, err := mdns.NewMDNSService(
		host,        // instance name
		serviceName, // service type
		domain,      // domain
		"",          // host name (empty for default)
		s.port,      // port number
		nil,         // IPs (nil for all interfaces)
		info,        // text info
	)
	if err != nil {
		return fmt.Errorf("failed to create mDNS service: %w", err)
	}

	// Configure mDNS server with both IPv4 and IPv6 support
	config := &mdns.Config{
		Zone:  service,
		Iface: selectedIface, // Use selected interface if found
	}

	server, err := mdns.NewServer(config)
	if err != nil {
		return fmt.Errorf("failed to start mDNS server: %w", err)
	}

	s.server = server
	return nil
}

// monitorNetwork checks for network interface changes
func (s *Service) monitorNetwork(ctx context.Context) {
	ticker := time.NewTicker(networkCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newIP, err := s.getLocalIP()
			if err != nil {
				log.Printf("[Discovery] Failed to get local IP: %v", err)
				continue
			}

			if s.currentIP == nil || !s.currentIP.Equal(newIP) {
				log.Printf("[Discovery] Network change detected. Old IP: %v, New IP: %v", s.currentIP, newIP)
				if err := s.restartMDNS(); err != nil {
					log.Printf("[Discovery] Failed to restart mDNS after network change: %v", err)
				}
			}
		}
	}
}

// cleanInactivePeers removes inactive peers from memory only
func (s *Service) cleanInactivePeers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var peersToRemove []string
	activePeers := 0

	for guid, peer := range s.peers {
		timeSinceLastSeen := now.Sub(peer.LastSeen.UTC())
		if timeSinceLastSeen > activePeerTimeout {
			peersToRemove = append(peersToRemove, guid)
			log.Printf("[Discovery] Peer inactive: GUID=%s Name=%s LastSeen=%s Age=%s",
				guid, peer.Name, peer.LastSeen.Format(time.RFC3339), timeSinceLastSeen)
		} else {
			activePeers++
			log.Printf("[Discovery] Peer active: GUID=%s Name=%s LastSeen=%s Age=%s",
				guid, peer.Name, peer.LastSeen.Format(time.RFC3339), timeSinceLastSeen)
		}
	}

	// Remove inactive peers from memory only
	for _, guid := range peersToRemove {
		delete(s.peers, guid)
	}

	if len(peersToRemove) > 0 || activePeers > 0 {
		log.Printf("[Discovery] Cleanup complete. Removed %d inactive peers. %d peers still active.",
			len(peersToRemove), activePeers)
	}
}

// Start starts the discovery service
func (s *Service) Start(ctx context.Context) error {
	// Initialize mDNS
	if err := s.restartMDNS(); err != nil {
		return fmt.Errorf("failed to start mDNS: %w", err)
	}

	// Start network monitoring
	go s.monitorNetwork(ctx)

	// Start continuous discovery
	go s.discover(ctx)

	log.Printf("[Discovery] Service started successfully for peer %s on port %d", s.guid, s.port)
	return nil
}

// discover continuously looks for peers
func (s *Service) discover(ctx context.Context) {
	baseInterval := 2 * time.Second
	maxInterval := 15 * time.Second
	currentInterval := baseInterval
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	lastPeerCount := 0
	consecutiveUnchanged := 0
	maxConsecutiveUnchanged := 3

	cleanupTicker := time.NewTicker(activePeerTimeout / 2)
	defer cleanupTicker.Stop()

	log.Printf("[Discovery] Starting peer discovery for %s", s.guid)

	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			log.Printf("[Discovery] Running cleanup cycle")
			s.cleanInactivePeers()
		case <-ticker.C:
			entriesCh := make(chan *mdns.ServiceEntry, 10)
			foundPeers := 0

			scanCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)

			go func() {
				defer cancel()

				// Get the current interface being used
				var selectedIface *net.Interface
				if s.currentIP != nil {
					ifaces, err := net.Interfaces()
					if err == nil {
						for _, iface := range ifaces {
							addrs, err := iface.Addrs()
							if err != nil {
								continue
							}
							for _, addr := range addrs {
								if ipnet, ok := addr.(*net.IPNet); ok {
									if ipnet.IP.Equal(s.currentIP) {
										selectedIface = &iface
										break
									}
								}
							}
							if selectedIface != nil {
								break
							}
						}
					}
				}

				params := &mdns.QueryParam{
					Service:             serviceName,
					Domain:              domain,
					Entries:             entriesCh,
					DisableIPv6:         true,
					WantUnicastResponse: true,
					Timeout:             time.Second,
					Interface:           selectedIface, // Use selected interface
				}

				mdns.Query(params)
			}()

			for {
				select {
				case entry, ok := <-entriesCh:
					if !ok {
						goto SCAN_DONE
					}

					// Only process CyberChat services
					if !strings.Contains(entry.Name, serviceName) {
						continue
					}

					// Only log entries that are actually CyberChat peers
					peer, err := s.parsePeer(entry)
					if err != nil {
						log.Printf("[Discovery] Failed to parse peer from entry: %v", err)
						continue
					}

					if peer.GUID == s.guid {
						continue
					}

					// Check for existing peers with same name and port but different GUID
					s.mu.Lock()
					var peersToRemove []string
					var oldPublicKey []byte
					for existingGUID, existingPeer := range s.peers {
						if existingGUID != peer.GUID &&
							existingPeer.Name == peer.Name &&
							existingPeer.Port == peer.Port {
							// Found a stale peer entry - save its public key if available
							if existingPeer.PublicKey != nil {
								oldPublicKey = existingPeer.PublicKey
							}
							// Remove it
							peersToRemove = append(peersToRemove, existingGUID)
							log.Printf("[Discovery] Removing stale peer: GUID=%s Name=%s", existingGUID, existingPeer.Name)
						}
					}

					// Remove stale peers
					for _, guid := range peersToRemove {
						delete(s.peers, guid)
						if s.db != nil {
							if err := s.db.DeletePeer(guid); err != nil {
								log.Printf("[Discovery] Failed to delete stale peer from DB: %v", err)
							}
						}
					}

					// Now handle the new/updated peer
					existing := s.peers[peer.GUID]
					if existing == nil {
						foundPeers++
						log.Printf("[Discovery] New peer: GUID=%s Name=%s IP=%s Port=%d",
							peer.GUID, peer.Name, peer.IP, peer.Port)

						// Transfer public key from old peer entry if available
						if oldPublicKey != nil {
							peer.PublicKey = oldPublicKey
						}

						// Save the peer first without public key
						s.peers[peer.GUID] = peer

						if s.db != nil {
							// Save peer with current timestamp
							if err := s.db.SavePeer(peer.GUID, peer.IP.String(), peer.Port, peer.PublicKey, peer.Name); err != nil {
								log.Printf("[Discovery] DB save failed: %v", err)
							}
						}

						// Try to fetch public key in background
						go func(p Peer) {
							pubKey, err := s.GetPeerPublicKey(p)
							if err != nil {
								log.Printf("[Discovery] Warning: Failed to fetch public key for new peer %s: %v", p.GUID, err)
								return
							}

							s.mu.Lock()
							if existingPeer := s.peers[p.GUID]; existingPeer != nil {
								existingPeer.PublicKey = pubKey
								if s.db != nil {
									if err := s.db.SavePeer(p.GUID, p.IP.String(), p.Port, pubKey, p.Name); err != nil {
										log.Printf("[Discovery] Failed to save fetched public key: %v", err)
									}
								}
							}
							s.mu.Unlock()
						}(*peer)

						select {
						case s.updates <- *peer:
							log.Printf("[Discovery] Sent peer update for %s", peer.GUID)
						default:
							log.Printf("[Discovery] Update channel full for %s", peer.GUID)
						}
					} else if existing.Port != peer.Port || existing.IP.String() != peer.IP.String() || existing.Name != peer.Name {
						log.Printf("[Discovery] Updated peer: GUID=%s Name=%s IP=%s Port=%d",
							peer.GUID, peer.Name, peer.IP, peer.Port)

						// Preserve existing public key
						peer.PublicKey = existing.PublicKey

						if s.db != nil {
							// Update peer with current timestamp
							if err := s.db.SavePeer(peer.GUID, peer.IP.String(), peer.Port, peer.PublicKey, peer.Name); err != nil {
								log.Printf("[Discovery] DB update failed: %v", err)
							}
						}

						s.peers[peer.GUID] = peer

						select {
						case s.updates <- *peer:
							log.Printf("[Discovery] Sent peer update for %s", peer.GUID)
						default:
							log.Printf("[Discovery] Update channel full for %s", peer.GUID)
						}
					} else {
						// Peer exists and hasn't changed, but update LastSeen
						existing.LastSeen = time.Now()
						if s.db != nil {
							if err := s.db.SavePeer(peer.GUID, peer.IP.String(), peer.Port, existing.PublicKey, peer.Name); err != nil {
								log.Printf("[Discovery] DB update failed: %v", err)
							}
						}
					}
					s.mu.Unlock()

				case <-scanCtx.Done():
					goto SCAN_DONE
				}
			}

		SCAN_DONE:
			s.mu.RLock()
			currentPeerCount := len(s.peers)
			s.mu.RUnlock()

			if foundPeers == 0 && currentPeerCount == lastPeerCount {
				consecutiveUnchanged++
				if consecutiveUnchanged >= maxConsecutiveUnchanged {
					currentInterval = time.Duration(float64(currentInterval) * 1.25)
					if currentInterval > maxInterval {
						currentInterval = maxInterval
					}
					consecutiveUnchanged = 0
				}
			} else {
				currentInterval = baseInterval
				consecutiveUnchanged = 0
			}

			lastPeerCount = currentPeerCount
			ticker.Reset(currentInterval)

			// Only log if we found new peers or current count
			if foundPeers > 0 || currentPeerCount > 0 {
				log.Printf("[Discovery] Scan complete. Found %d new peers. Total active: %d",
					foundPeers, currentPeerCount)
			}

			// Update LastSeen in both memory and database
			s.mu.Lock()
			now := time.Now()
			for _, peer := range s.peers {
				peer.LastSeen = now
				if s.db != nil {
					if err := s.db.SavePeer(peer.GUID, peer.IP.String(), peer.Port, peer.PublicKey, peer.Name); err != nil {
						log.Printf("[Discovery] DB update failed: %v", err)
					}
				}
			}
			s.mu.Unlock()
		}
	}
}

// parsePeer extracts peer information from mDNS entry
func (s *Service) parsePeer(entry *mdns.ServiceEntry) (*Peer, error) {
	var guid string
	var port int
	var name string
	var ip net.IP = entry.AddrV4 // Default to AddrV4 from entry

	// Parse TXT records
	for _, field := range entry.InfoFields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}

		switch parts[0] {
		case "id":
			guid = parts[1]
		case "port":
			var err error
			port, err = strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("invalid port number: %w", err)
			}
		case "name":
			name = parts[1]
		case "ip":
			// Use IP from text record if available
			if parsedIP := net.ParseIP(parts[1]); parsedIP != nil {
				ip = parsedIP
			}
		}
	}

	if guid == "" {
		return nil, fmt.Errorf("missing peer GUID")
	}

	if port == 0 {
		port = entry.Port
	}

	peer := &Peer{
		GUID:      guid,
		Port:      port,
		IP:        ip,
		PublicKey: nil, // Will be fetched separately
		Name:      name,
	}

	return peer, nil
}

// isValidUUID checks if a string is a valid UUID
func isValidUUID(uuid string) bool {
	// UUID format: 8-4-4-4-12 (32 hex digits + 4 hyphens)
	if len(uuid) != 36 {
		return false
	}

	// Check for hyphens in correct positions
	if uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return false
	}

	// Check that all other characters are hex digits
	for i, c := range uuid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}

	return true
}

// Stop stops the discovery service
func (s *Service) Stop() error {
	s.cancel() // Cancel our context
	if s.server != nil {
		s.server.Shutdown()
	}
	close(s.updates)
	return nil
}

// GetPeers returns a list of all active peers
func (s *Service) GetPeers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers := make([]Peer, 0, len(s.peers))
	for _, peer := range s.peers {
		peers = append(peers, *peer)
	}

	log.Printf("[Discovery] GetPeers returning %d active peers for GUID %s", len(peers), s.guid)
	log.Printf("[Discovery] Active peers in memory:")
	for _, peer := range peers {
		log.Printf("[Discovery] - %s (%s) at %s:%d LastSeen=%s",
			peer.Name, peer.GUID, peer.IP, peer.Port, peer.LastSeen)
	}

	return peers
}

// PeerUpdates returns a channel that receives peer updates
func (s *Service) PeerUpdates() <-chan Peer {
	return s.updates
}

// GetPeerPublicKey fetches the public key for a peer
func (s *Service) GetPeerPublicKey(peer Peer) ([]byte, error) {

	// Create HTTP client that skips certificate verification and has a short timeout
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			// Add timeouts for connection operations
			DialContext: (&net.Dialer{
				Timeout: 1500 * time.Millisecond,
			}).DialContext,
			TLSHandshakeTimeout: 1500 * time.Millisecond,
		},
	}

	// Use peer's actual IP instead of localhost
	url := fmt.Sprintf("https://%s:%d/api/v1/whoami", peer.IP, peer.Port)
	log.Printf("[Discovery] Fetching public key from %s", url)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch peer info: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		GUID      string `json:"guid"`
		PublicKey []byte `json:"public_key"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode peer info: %w", err)
	}

	// Verify the GUID matches
	if info.GUID != peer.GUID {
		return nil, fmt.Errorf("GUID mismatch")
	}

	// Update peer's name and public key
	s.mu.Lock()
	if p := s.peers[peer.GUID]; p != nil {
		p.Name = info.Name
		p.PublicKey = info.PublicKey
	}
	s.mu.Unlock()

	// Save the public key to the database
	if s.db != nil {
		if err := s.db.SavePeer(peer.GUID, peer.IP.String(), peer.Port, info.PublicKey, info.Name); err != nil {
			log.Printf("[Discovery] Warning: Failed to save public key to database: %v", err)
		} else {
			log.Printf("[Discovery] Saved public key for peer %s to database", peer.GUID)
		}
	}

	return info.PublicKey, nil
}

// GetPeer returns a specific peer by GUID
func (s *Service) GetPeer(guid string) *Peer {
	// First check in-memory map
	s.mu.RLock()
	if peer := s.peers[guid]; peer != nil {
		s.mu.RUnlock()
		return peer
	}
	s.mu.RUnlock()

	// If not found in memory and we have a database, check there
	if s.db != nil {
		if dbPeer, err := s.db.GetPeer(guid); err == nil && dbPeer != nil {
			// Check if the peer was seen within the active timeout period
			if time.Since(dbPeer.LastSeen) > activePeerTimeout {
				// Peer is stale, remove it from the database
				if err := s.db.DeletePeer(guid); err != nil {
					log.Printf("[Discovery] Warning: Failed to delete stale peer %s: %v", guid, err)
				}
				return nil
			}

			// Convert database peer to discovery peer
			peer := &Peer{
				GUID:      dbPeer.GUID,
				Port:      dbPeer.Port,
				IP:        net.ParseIP(dbPeer.IPAddress),
				PublicKey: dbPeer.PublicKey,
				Name:      dbPeer.Username,
				LastSeen:  dbPeer.LastSeen,
			}

			// Cache the peer in memory
			s.mu.Lock()
			s.peers[guid] = peer
			s.mu.Unlock()

			return peer
		}
	}

	return nil
}

// GetActivePeers returns only peers that have been seen within the active timeout period
func (s *Service) GetActivePeers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()
	var activePeers []Peer
	for _, peer := range s.peers {
		if now.Sub(peer.LastSeen.UTC()) <= activePeerTimeout {
			activePeers = append(activePeers, *peer)
		}
	}

	return activePeers
}

// RemoveInactivePeer removes a peer from memory immediately
func (s *Service) RemoveInactivePeer(guid string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if peer, exists := s.peers[guid]; exists {
		log.Printf("[Discovery] Forcefully removing inactive peer: GUID=%s Name=%s LastSeen=%s",
			guid, peer.Name, peer.LastSeen)
		delete(s.peers, guid)
	}
}

// UpdateName updates the service's name and triggers a re-announcement
func (s *Service) UpdateName(name string) error {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	return s.restartMDNS()
}
