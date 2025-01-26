package websocket

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cyberchat/server/discovery"
	"cyberchat/server/logging"
	"cyberchat/server/messages"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Manager handles WebSocket connections and message broadcasting
type Manager struct {
	connections map[*Connection]bool
	mutex       sync.RWMutex
	onMessage   func(*messages.Message, string)
	myGUID      string
}

// Connection represents a single WebSocket connection
type Connection struct {
	conn             *websocket.Conn
	send             chan []byte
	connectedAt      time.Time
	messagesSent     int
	messagesReceived int
	remoteAddr       string
	id               string
}

// verifyLocalhost checks if the request is coming from localhost
func verifyLocalhost(r *http.Request) bool {
	// Get the real client IP, considering X-Forwarded-For
	ip := r.RemoteAddr
	if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		ip = forwardedFor
	}

	// Extract host from IP:port format
	host, _, err := net.SplitHostPort(ip)
	if err != nil {
		host = ip // If no port, use the IP as is
	}

	// Check if it's a localhost address
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// upgrader configures WebSocket connection parameters
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Only allow connections from localhost
		if !verifyLocalhost(r) {
			return false
		}

		// Get the Origin header
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}

		// Allow localhost origins
		return strings.HasPrefix(origin, "https://localhost:") ||
			strings.HasPrefix(origin, "https://127.0.0.1:") ||
			strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:")
	},
}

// NewManager creates a new WebSocket manager
func NewManager(messageHandler func(*messages.Message, string), myGUID string) *Manager {
	return &Manager{
		connections: make(map[*Connection]bool),
		onMessage:   messageHandler,
		myGUID:      myGUID,
	}
}

// HandleConnection upgrades HTTP connection to WebSocket and manages it
func (m *Manager) HandleConnection(w http.ResponseWriter, r *http.Request) {
	// Additional security check for localhost
	if !verifyLocalhost(r) {
		http.Error(w, "WebSocket connections only allowed from localhost", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}

	wsConn := &Connection{
		conn:             conn,
		send:             make(chan []byte, 256),
		connectedAt:      time.Now(),
		messagesSent:     0,
		messagesReceived: 0,
		remoteAddr:       r.RemoteAddr,
		id:               uuid.New().String(),
	}

	m.mutex.Lock()
	m.connections[wsConn] = true
	m.mutex.Unlock()

	go wsConn.writePump()
	go wsConn.readPump(m)
}

// SendPeerList sends the current peer list to a specific connection
func (m *Manager) SendPeerList(peers []*discovery.Peer) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	for conn := range m.connections {
		for _, peer := range peers {
			conn.send <- createPeerUpdate(peer)
		}
	}
}

// Broadcast sends a message to all connected clients
func (m *Manager) Broadcast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal broadcast message: %v", err)
		return
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	for conn := range m.connections {
		select {
		case conn.send <- data:
			// Message sent successfully
		default:
			// Buffer full, close connection
			close(conn.send)
			delete(m.connections, conn)
		}
	}
}

// writePump handles sending messages to the WebSocket connection
func (c *Connection) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			c.messagesSent++

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump handles receiving messages from the WebSocket connection
func (c *Connection) readPump(m *Manager) {
	defer func() {
		m.mutex.Lock()
		delete(m.connections, c)
		m.mutex.Unlock()
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512 * 1024) // 512KB max message size
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logging.Error("WebSocket", "Connection error: %v", err)
			}
			break
		}

		c.messagesReceived++

		var msg struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(message, &msg); err != nil {
			logging.Error("WebSocket", "Failed to parse message: %v", err)
			continue
		}

		switch msg.Type {
		case "message":
			var content struct {
				Type         string `json:"type"`
				Content      string `json:"content"`
				ReceiverGUID string `json:"receiver_guid"`
				Scope        string `json:"scope"`
			}
			if err := json.Unmarshal(msg.Content, &content); err != nil {
				logging.Error("WebSocket", "Failed to parse message content: %v", err)
				continue
			}

			logging.Info("WebSocket", "Received message: type=%s, content=%s, receiver=%s, scope=%s",
				content.Type, content.Content, content.ReceiverGUID, content.Scope)

			// Create a proper Message object
			message := messages.NewMessage(
				m.myGUID,
				content.ReceiverGUID,
				messages.MessageType(content.Type),
				[]byte(content.Content),
			)

			// Set scope based on explicit scope field or receiver
			if content.Scope == string(messages.ScopeBroadcast) {
				message.Scope = messages.ScopeBroadcast
				logging.Info("WebSocket", "Setting message scope to broadcast")
			} else if content.ReceiverGUID == "" {
				message.Scope = messages.ScopeBroadcast
				logging.Info("WebSocket", "Setting message scope to broadcast (empty receiver)")
			} else {
				message.Scope = messages.ScopePrivate
				logging.Info("WebSocket", "Setting message scope to private")
			}

			if m.onMessage != nil {
				m.onMessage(message, c.remoteAddr)
			} else {
				logging.Error("WebSocket", "No message handler set")
			}
		case "ping":
			// Send pong response
			pong := struct {
				Type string `json:"type"`
			}{
				Type: "pong",
			}
			data, _ := json.Marshal(pong)
			c.send <- data
		}
	}
}

// createPeerUpdate creates a JSON message for peer updates
func createPeerUpdate(peer *discovery.Peer) []byte {
	update := struct {
		Type    string `json:"type"`
		Content struct {
			GUID      string `json:"guid"`
			Name      string `json:"name"`
			Port      int    `json:"port"`
			IPAddress string `json:"ip_address"`
		} `json:"content"`
	}{
		Type: "peer",
		Content: struct {
			GUID      string `json:"guid"`
			Name      string `json:"name"`
			Port      int    `json:"port"`
			IPAddress string `json:"ip_address"`
		}{
			GUID:      peer.GUID,
			Name:      peer.Name,
			Port:      peer.Port,
			IPAddress: peer.IP.String(),
		},
	}

	data, err := json.Marshal(update)
	if err != nil {
		log.Printf("Failed to marshal peer update: %v", err)
		return nil
	}
	return data
}

// GetStats returns current WebSocket statistics
func (m *Manager) GetStats() (connections int, messagesSent int, messagesReceived int) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	connections = len(m.connections)
	for conn := range m.connections {
		messagesSent += conn.messagesSent
		messagesReceived += conn.messagesReceived
	}
	return
}
