package clientapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"cyberchat/server/db"
	"cyberchat/server/discovery"
	"cyberchat/server/messages"
)

// Handlers contains HTTP handlers for client API operations
type Handlers struct {
	db           *db.DB
	guid         string
	clientAPIKey string
	onMessage    func(*messages.Message, string) *messages.MessageDeliveryReport
	discovery    *discovery.Service
}

// NewHandlers creates a new Handlers instance
func NewHandlers(db *db.DB, guid string, clientAPIKey string, onMessage func(*messages.Message, string) *messages.MessageDeliveryReport, discovery *discovery.Service) *Handlers {
	return &Handlers{
		db:           db,
		guid:         guid,
		clientAPIKey: clientAPIKey,
		onMessage:    onMessage,
		discovery:    discovery,
	}
}

// verifyAPIKey checks if the provided API key is valid
func (h *Handlers) verifyAPIKey(r *http.Request) bool {
	apiKey := r.Header.Get("X-Client-API-Key")
	return apiKey == h.clientAPIKey
}

// verifyClientIP checks if the request is coming from localhost
func (h *Handlers) verifyClientIP(r *http.Request) bool {
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

// verifyClient checks both API key and client IP
func (h *Handlers) verifyClient(r *http.Request) bool {
	return h.verifyAPIKey(r) && h.verifyClientIP(r)
}

// HandleAuth returns the client API key only to localhost clients
func (h *Handlers) HandleAuth(w http.ResponseWriter, r *http.Request) {
	if !h.verifyClientIP(r) {
		http.Error(w, "Unauthorized - client API only available from localhost", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"api_key": h.clientAPIKey,
	})
}

// HandleMessage processes a message from the web client
func (h *Handlers) HandleMessage(w http.ResponseWriter, r *http.Request) {
	if !h.verifyClient(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Parse message
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

	// Create message using web-specific constructor
	message := messages.NewWebMessage(h.guid, msg.ReceiverGUID, messages.MessageType(msg.Type), msg.Content)
	message.Scope = messages.MessageScope(msg.Scope)

	// Get source IP
	sourceIP := r.RemoteAddr
	if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		sourceIP = forwardedFor
	}

	// Process message and get delivery report
	var report *messages.MessageDeliveryReport
	if h.onMessage != nil {
		report = h.onMessage(message, sourceIP)
	}

	// Return delivery report
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(report); err != nil {
		http.Error(w, "Failed to encode delivery report", http.StatusInternalServerError)
		return
	}
}

// HandleGetMessages returns messages from the database
func (h *Handlers) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	if !h.verifyClient(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse query parameters
	sinceStr := r.URL.Query().Get("since")
	var since time.Time
	if sinceStr != "" {
		var err error
		since, err = time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			http.Error(w, "Invalid since parameter", http.StatusBadRequest)
			return
		}
	} else {
		since = time.Now().Add(-24 * time.Hour) // Default to last 24 hours
	}

	// Get messages from database
	msgs, err := h.db.GetMessages(h.guid, since, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert messages to web format
	webMsgs := make([]map[string]interface{}, len(msgs))
	for i, msg := range msgs {
		webMsgs[i] = map[string]interface{}{
			"id":            msg.ID,
			"sender_guid":   msg.SenderGUID,
			"receiver_guid": msg.ReceiverGUID,
			"type":          string(msg.Type),
			"scope":         string(msg.Scope),
			"content":       string(msg.Content),
			"timestamp":     msg.Timestamp,
		}
	}

	// Return messages as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(webMsgs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// HandleTruncateMessages truncates all messages from the database
func (h *Handlers) HandleTruncateMessages(w http.ResponseWriter, r *http.Request) {
	if !h.verifyClient(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Truncate messages
	if err := h.db.TruncateMessages(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to truncate messages: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "All messages truncated",
	})
}

// HandleName processes a name update request from the web client
func (h *Handlers) HandleName(w http.ResponseWriter, r *http.Request) {
	if !h.verifyClient(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Read request body
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Failed to decode request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "Name cannot be empty", http.StatusBadRequest)
		return
	}

	// Save name to database
	if err := h.db.SaveName(req.Name); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save name: %v", err), http.StatusInternalServerError)
		return
	}

	// Trigger a peer update to broadcast the name change
	if h.discovery != nil {
		if err := h.discovery.UpdateName(req.Name); err != nil {
			log.Printf("Warning: Failed to broadcast name update: %v", err)
		}
	}

	// Return success
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"name":   req.Name,
	})
}
