package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"cyberchat/server/clientapi"
	"cyberchat/server/config"
	"cyberchat/server/db"
	"cyberchat/server/discovery"
	"cyberchat/server/files"
	"cyberchat/server/logging"
	"cyberchat/server/messagehandler"
	"cyberchat/server/messages"
	"cyberchat/server/peers"
	"cyberchat/server/web"
	"cyberchat/server/websocket"

	"github.com/google/uuid"
)

const (
	defaultPort     = 7331
	maxPortAttempts = 100
	certValidDays   = 36500               // 100 years
	messageMaxAge   = 30 * 24 * time.Hour // 30 days
)

// Peer represents a discovered peer in the network
type Peer struct {
	GUID      string
	Port      int
	Name      string
	IPAddress string
}

// Server represents the main server instance
type Server struct {
	cfg            *config.Config
	db             *db.DB
	server         *http.Server
	discovery      *discovery.Service
	peerMgr        *peers.Manager
	messageQueue   chan *messages.Message
	wsManager      *websocket.Manager
	guid           string
	publicKey      *rsa.PublicKey
	privateKey     *rsa.PrivateKey
	OnMessage      func(*messages.Message)
	messageHandler *messagehandler.Handler
	peerHandlers   *peers.Handlers
	clientHandlers *clientapi.Handlers
	fileHandlers   *files.Handlers
	tlsConfig      *tls.Config
	listener       net.Listener
}

// WebMessage represents a message in the format expected by web clients
type WebMessage struct {
	ID           string    `json:"id"`
	SenderGUID   string    `json:"sender_guid"`
	ReceiverGUID string    `json:"receiver_guid"`
	Type         string    `json:"type"`
	Scope        string    `json:"scope"`
	Content      string    `json:"content"`
	Timestamp    time.Time `json:"timestamp"`
	SenderIP     string    `json:"sender_ip"`
	SenderPort   int       `json:"sender_port"`
}

// New creates a new server instance
func New(cfg *config.Config, database *db.DB) (*Server, error) {
	// Try to get existing GUID from database
	guid, err := database.GetGUID()
	if err != nil || guid == "" {
		// Generate new GUID if none exists
		guid = uuid.New().String()
		if err := database.SaveGUID(guid); err != nil {
			return nil, fmt.Errorf("failed to save GUID: %w", err)
		}
	}

	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key pair: %w", err)
	}

	s := &Server{
		cfg:          cfg,
		db:           database,
		guid:         guid,
		publicKey:    &privateKey.PublicKey,
		privateKey:   privateKey,
		messageQueue: make(chan *messages.Message, 100),
	}

	// Initialize WebSocket manager
	s.wsManager = websocket.NewManager(s.processMessage, s.guid)

	// Initialize peer manager
	s.peerMgr = peers.New(database, s.handlePeerUpdate)

	// Initialize discovery service
	pubKeyBytes := x509.MarshalPKCS1PublicKey(s.publicKey)
	discoveryService, err := discovery.New(s.guid, cfg.Port, pubKeyBytes, s.db, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery service: %w", err)
	}
	s.discovery = discoveryService

	// Initialize message handler
	s.messageHandler = messagehandler.New(s.db, s.guid, s.privateKey, s.discovery, s.wsManager, s.peerMgr)

	// Initialize peer handlers
	s.peerHandlers = peers.NewHandlers(s.peerMgr, s.discovery)

	// Get or generate client API key
	clientAPIKey, err := s.db.GetClientAPIKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get client API key: %w", err)
	}
	if clientAPIKey == "" {
		clientAPIKey = uuid.New().String()
		if err := s.db.SaveClientAPIKey(clientAPIKey); err != nil {
			return nil, fmt.Errorf("failed to save client API key: %w", err)
		}
	}

	// Initialize client API handlers with message handler that returns delivery report
	s.clientHandlers = clientapi.NewHandlers(
		s.db,
		s.guid,
		clientAPIKey,
		s.messageHandler.ProcessMessage,
		s.discovery,
	)

	// Initialize file handlers with database adapter
	dbAdapter := &fileDBAdapter{db: s.db}
	s.fileHandlers = files.NewHandlers(dbAdapter, s.guid, clientAPIKey, s.wsManager)
	return s, nil
}

// fileDBAdapter adapts db.DB to files.DB interface
type fileDBAdapter struct {
	db *db.DB
}

func (a *fileDBAdapter) SaveFile(fileID, senderGUID, receiverGUID, filename, filepath string, size int64, mimeType string) error {
	return a.db.SaveFile(fileID, senderGUID, receiverGUID, filename, filepath, size, mimeType)
}

func (a *fileDBAdapter) GetFile(fileID string) (*files.FileRecord, error) {
	record, err := a.db.GetFile(fileID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &files.FileRecord{
		FileID:       record.FileID,
		SenderGUID:   record.SenderGUID,
		ReceiverGUID: record.ReceiverGUID,
		Filename:     record.Filename,
		Filepath:     record.Filepath,
		Size:         record.Size,
		MimeType:     record.MimeType,
		CreatedAt:    record.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (a *fileDBAdapter) GetFiles() ([]files.FileRecord, error) {
	records, err := a.db.GetFiles()
	if err != nil {
		return nil, err
	}

	fileRecords := make([]files.FileRecord, len(records))
	for i, record := range records {
		fileRecords[i] = files.FileRecord{
			FileID:       record.FileID,
			SenderGUID:   record.SenderGUID,
			ReceiverGUID: record.ReceiverGUID,
			Filename:     record.Filename,
			Filepath:     record.Filepath,
			Size:         record.Size,
			MimeType:     record.MimeType,
			CreatedAt:    record.CreatedAt.Format(time.RFC3339),
		}
	}
	return fileRecords, nil
}

func (a *fileDBAdapter) TruncateFiles() error {
	return a.db.TruncateFiles()
}

// FirstTimeSetup performs initial server setup if needed
func (s *Server) FirstTimeSetup() error {
	// Check if first time setup is needed
	if _, err := os.Stat(s.cfg.DataDir); os.IsNotExist(err) {
		log.Printf("First time setup: creating data directory %s", s.cfg.DataDir)
		if err := os.MkdirAll(s.cfg.DataDir, 0755); err != nil {
			return fmt.Errorf("failed to create data directory: %w", err)
		}
	}

	// Generate certificates if needed
	if err := s.GenerateCertificates(); err != nil {
		return fmt.Errorf("failed to generate certificates: %w", err)
	}

	// Initialize database
	if err := s.InitDB(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	return nil
}

// cleanupRoutine periodically removes old messages
func (s *Server) cleanupRoutine() {
	ticker := time.NewTicker(24 * time.Hour) // Run once per day
	defer ticker.Stop()

	for range ticker.C {
		ctx := context.Background()
		if err := s.db.CleanupOldMessages(ctx, messageMaxAge); err != nil {
			log.Printf("Error cleaning up old messages: %v", err)
		}
	}
}

// GenerateCertificates generates self-signed certificates for HTTPS
func (s *Server) GenerateCertificates() error {
	// Create certificate directory with proper permissions
	if err := os.MkdirAll(s.cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	certPath := filepath.Join(s.cfg.DataDir, "cert.pem")
	keyPath := filepath.Join(s.cfg.DataDir, "key.pem")

	// Check if certificates already exist
	certExists := false
	keyExists := false
	if _, err := os.Stat(certPath); err == nil {
		certExists = true
	}
	if _, err := os.Stat(keyPath); err == nil {
		keyExists = true
	}

	// If both files exist, we're done
	if certExists && keyExists {
		log.Printf("Certificates already exist in %s", s.cfg.DataDir)
		return nil
	}

	// Generate private key if it doesn't exist
	if s.privateKey == nil {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return fmt.Errorf("failed to generate private key: %w", err)
		}
		s.privateKey = key
		s.publicKey = &key.PublicKey
	}

	// Generate certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"CyberChat"},
			CommonName:   "*",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(certValidDays * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("0.0.0.0"), net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"*", "localhost"},
	}

	// Create certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &s.privateKey.PublicKey, s.privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	log.Printf("Writing certificate to %s", certPath)
	// Write certificate with explicit file permissions
	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create cert.pem: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		certOut.Close()
		return fmt.Errorf("failed to write cert.pem: %w", err)
	}
	certOut.Close()

	log.Printf("Writing private key to %s", keyPath)
	// Write private key with explicit file permissions
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create key.pem: %w", err)
	}
	privBytes := x509.MarshalPKCS1PrivateKey(s.privateKey)
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes}); err != nil {
		keyOut.Close()
		return fmt.Errorf("failed to write key.pem: %w", err)
	}
	keyOut.Close()

	log.Printf("Successfully generated certificates in %s", s.cfg.DataDir)
	return nil
}

// StartServer starts the HTTPS server on the first available port starting from 7331
func (s *Server) StartServer(ctx context.Context) error {
	// Find available port
	port := s.cfg.Port
	var listener net.Listener
	var err error

	// Try ports until we find an available one
	maxAttempts := 100 // Don't try forever
	for attempts := 0; attempts < maxAttempts; attempts++ {
		listener, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			break // Found an available port
		}

		if attempts == 0 {
			log.Printf("Port %d is in use, trying next port...", port)
		}
		port++
	}

	if err != nil {
		return fmt.Errorf("failed to find available port after %d attempts: %w", maxAttempts, err)
	}

	// Update server port to the one we found
	s.cfg.Port = port
	log.Printf("Found available port: %d", port)

	// Initialize discovery service with the actual port we're using
	pubKeyBytes := x509.MarshalPKCS1PublicKey(s.publicKey)
	discovery, err := discovery.New(s.guid, port, pubKeyBytes, s.db, s.cfg.Name)
	if err != nil {
		listener.Close()
		return fmt.Errorf("failed to create discovery service: %w", err)
	}
	s.discovery = discovery

	if err := s.discovery.Start(ctx); err != nil {
		listener.Close()
		return fmt.Errorf("failed to start discovery service: %w", err)
	}

	// Sync initial peers
	initialPeers := s.discovery.GetPeers()
	for _, dPeer := range initialPeers {
		peer := peers.Peer{
			GUID:      dPeer.GUID,
			Name:      dPeer.Name,
			Port:      dPeer.Port,
			IPAddress: dPeer.IP.String(),
		}
		s.peerMgr.HandleUpdate(peer)
	}

	// Start peer update handler
	go s.handlePeerUpdates(ctx)

	// Create TLS config
	cert, err := tls.LoadX509KeyPair(filepath.Join(s.cfg.DataDir, "cert.pem"), filepath.Join(s.cfg.DataDir, "key.pem"))
	if err != nil {
		listener.Close()
		return fmt.Errorf("failed to load TLS certificates: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		// Always accept self-signed certificates
		InsecureSkipVerify: true,
		// Disable client certificate verification
		ClientAuth: tls.NoClientCert,
		// Allow all cipher suites
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
	}

	// Create server
	mux := http.NewServeMux()
	s.SetupRoutes(mux)

	s.server = &http.Server{
		Addr:      fmt.Sprintf(":%d", port),
		Handler:   mux,
		TLSConfig: tlsConfig,
		// Increase timeouts
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	// Start server
	log.Printf("Starting CyberChat server on port %d", port)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Stop discovery service
		if err := s.discovery.Stop(); err != nil {
			log.Printf("Error stopping discovery service: %v", err)
		}

		if err := s.server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error shutting down server: %v", err)
		}
	}()

	if err := s.server.ServeTLS(listener, "", ""); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// SetupRoutes configures all API routes using ServeMux
func (s *Server) SetupRoutes(mux *http.ServeMux) {
	// Core API routes (peer-to-peer)
	mux.HandleFunc("POST /api/v1/message", s.messageHandler.HandleMessage)
	mux.HandleFunc("GET /api/v1/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/v1/discovery", s.peerHandlers.HandleDiscovery)
	mux.HandleFunc("GET /api/v1/file/{file_id}", s.fileHandlers.HandleDownload)

	// Client API routes (web client only)
	mux.HandleFunc("GET /api/v1/client/auth", s.clientHandlers.HandleAuth)
	mux.HandleFunc("GET /api/v1/client/message", s.clientHandlers.HandleGetMessages)
	mux.HandleFunc("POST /api/v1/client/message", s.clientHandlers.HandleMessage)
	mux.HandleFunc("POST /api/v1/client/message/truncate", s.clientHandlers.HandleTruncateMessages)
	mux.HandleFunc("POST /api/v1/client/name", s.clientHandlers.HandleName)
	mux.HandleFunc("GET /api/v1/client/peers", s.peerHandlers.HandleGetPeers)
	mux.HandleFunc("GET /api/v1/client/filesystem", s.fileHandlers.HandleFilesystem)
	mux.HandleFunc("GET /api/v1/client/files", s.fileHandlers.HandleListFiles)
	mux.HandleFunc("POST /api/v1/client/file", s.fileHandlers.HandleUpload)
	mux.HandleFunc("POST /api/v1/client/file/truncate", s.fileHandlers.HandleTruncate)

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.wsManager.HandleConnection)

	// Web client route
	mux.HandleFunc("/", s.handleWebClient)

	// Debug routes
	if s.cfg.Debug {
		mux.HandleFunc("/status", s.handleStatus)
	}
}

// GetInstanceGUID returns this server's GUID
func (s *Server) GetInstanceGUID() string {
	return s.guid
}

// handlePeerUpdates processes peer updates from discovery service
func (s *Server) handlePeerUpdates(ctx context.Context) {
	log.Printf("[Server] Starting peer update handler for %s", s.guid)
	updates := s.discovery.PeerUpdates()
	for {
		select {
		case <-ctx.Done():
			return
		case dPeer := <-updates:
			peer := peers.Peer{
				GUID:      dPeer.GUID,
				Name:      dPeer.Name,
				Port:      dPeer.Port,
				IPAddress: dPeer.IP.String(),
			}
			s.peerMgr.HandleUpdate(peer)
		}
	}
}

// processMessage handles an incoming message internally
func (s *Server) processMessage(msg *messages.Message, sourceIP string) {
	// Log message if handler is set
	if s.OnMessage != nil {
		s.OnMessage(msg)
	}

	logging.Info("Server", "Processing message from %s to %s (type: %s, scope: %s)",
		msg.SenderGUID, msg.ReceiverGUID, msg.Type, msg.Scope)

	// Store message with source IP
	if err := s.db.SaveMessage(msg, sourceIP); err != nil {
		logging.Error("Server", "Failed to store message: %v", err)
		return
	}

	// Convert to web message format with string content
	webMsg := &WebMessage{
		ID:           msg.ID,
		SenderGUID:   msg.SenderGUID,
		ReceiverGUID: msg.ReceiverGUID,
		Type:         string(msg.Type),
		Scope:        string(msg.Scope),
		Content:      string(msg.Content), // Convert bytes to string
		Timestamp:    msg.Timestamp,
		SenderIP:     sourceIP,
		SenderPort:   s.cfg.Port, // Use actual server port
	}

	// Broadcast to web clients
	s.wsManager.Broadcast(struct {
		Type    string      `json:"type"`
		Content *WebMessage `json:"content"`
	}{
		Type:    "message",
		Content: webMsg,
	})

	// Handle message forwarding based on scope
	if msg.Scope == messages.ScopeBroadcast {
		logging.Info("Server", "Broadcasting message to all peers")
		// Forward to all peers except sender
		peers := s.discovery.GetPeers()
		for _, peer := range peers {
			if peer.GUID == msg.SenderGUID {
				continue // Skip sender
			}
			logging.Info("Server", "Forwarding broadcast message to peer %s (%s)", peer.Name, peer.GUID)
			// Create a copy of the message with this peer as receiver
			peerMsg := *msg
			peerMsg.ReceiverGUID = peer.GUID
			s.forwardMessageToPeer(&peerMsg, &peer)
		}
	} else if msg.Scope == messages.ScopePrivate && msg.ReceiverGUID != "" {
		logging.Info("Server", "Forwarding private message to peer %s", msg.ReceiverGUID)
		// Forward to specific peer
		if peer := s.discovery.GetPeer(msg.ReceiverGUID); peer != nil {
			s.forwardMessageToPeer(msg, peer)
		} else {
			logging.Error("Server", "Receiver peer %s not found", msg.ReceiverGUID)
		}
	}
}

// forwardMessageToPeer forwards a message to a specific peer
func (s *Server) forwardMessageToPeer(msg *messages.Message, peer *discovery.Peer) {
	// Get peer's public key
	pubKeyBytes, err := s.discovery.GetPeerPublicKey(*peer)
	if err != nil {
		logging.Error("Server", "Failed to get peer's public key: %v", err)
		return
	}

	// Parse public key
	block, _ := pem.Decode(pubKeyBytes)
	if block == nil {
		logging.Error("Server", "Failed to decode peer's public key")
		return
	}

	receiverPubKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		logging.Error("Server", "Failed to parse peer's public key: %v", err)
		return
	}

	// Encrypt message for peer
	encryptedMsg, err := msg.Encrypt(receiverPubKey)
	if err != nil {
		logging.Error("Server", "Failed to encrypt message: %v", err)
		return
	}

	// Create HTTP client that accepts self-signed certs
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Marshal encrypted message
	msgData, err := json.Marshal(encryptedMsg)
	if err != nil {
		logging.Error("Server", "Failed to marshal message: %v", err)
		return
	}

	// Forward to peer's server
	url := fmt.Sprintf("https://%s:%d/api/v1/message", peer.IP, peer.Port)
	logging.Info("Server", "Forwarding message to %s", url)
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(msgData))
	if err != nil {
		logging.Error("Server", "Failed to forward message: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		logging.Error("Server", "Peer returned error: %s", string(body))
	}
}

// handleGetMessages handles message retrieval
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
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

	limit := 100 // Default limit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		var err error
		limit, err = strconv.Atoi(limitStr)
		if err != nil || limit <= 0 || limit > 1000 {
			http.Error(w, "Invalid limit parameter", http.StatusBadRequest)
			return
		}
	}

	// Get messages from database
	msgs, err := s.db.GetMessages(s.guid, since, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert messages to web format and decrypt if needed
	webMsgs := make([]WebMessage, len(msgs))
	for i, msg := range msgs {
		// Check if content appears to be encrypted (base64 format)
		content := string(msg.Content)
		if len(content) > 100 && isBase64(content) {
			// Try to decrypt the message
			encMsg := messages.EncryptedMessage{
				ID:           msg.ID,
				SenderGUID:   msg.SenderGUID,
				ReceiverGUID: msg.ReceiverGUID,
				Type:         string(msg.Type),
				Scope:        msg.Scope,
				Content:      content,
				Timestamp:    msg.Timestamp,
			}
			if decrypted, err := encMsg.Decrypt(s.privateKey); err == nil {
				msg = decrypted
			}
		}

		webMsgs[i] = WebMessage{
			ID:           msg.ID,
			SenderGUID:   msg.SenderGUID,
			ReceiverGUID: msg.ReceiverGUID,
			Type:         string(msg.Type),
			Scope:        string(msg.Scope),
			Content:      string(msg.Content),
			Timestamp:    msg.Timestamp,
		}
	}

	// Return messages as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(webMsgs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// Helper function to check if a string is base64 encoded
func isBase64(s string) bool {
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}

// Update file download handler:
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Delegate to file handlers
	s.fileHandlers.HandleDownload(w, r)
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get name from database
	name, err := s.db.GetName()
	if err != nil {
		name = s.cfg.Name // Fallback to config name if DB error
	}

	// Marshal public key to PEM format
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: x509.MarshalPKCS1PublicKey(s.publicKey),
	})

	info := struct {
		GUID      string `json:"guid"`
		PublicKey []byte `json:"public_key"`
		Name      string `json:"name"`
	}{
		GUID:      s.guid,
		PublicKey: pubKeyPEM,
		Name:      name,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleWebClient serves the web client if available
func (s *Server) handleWebClient(w http.ResponseWriter, r *http.Request) {
	client, err := web.New()
	if err != nil {
		// Return a simple message if web client is not available
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("CyberChat server running. Web client not available.\n"))
		return
	}

	// Serve the web client
	client.ServeHTTP(w, r)
}

// InitDB initializes the database
func (s *Server) InitDB() error {
	// Initialize database connection
	dbPath := filepath.Join(s.cfg.DataDir, "cyberchat.db")
	database, err := db.New(dbPath, s.cfg.Debug)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	s.db = database

	// Initialize schema
	if err := s.db.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	return nil
}

// startHTTPServer starts the HTTPS server
func (s *Server) startHTTPServer() error {
	// Configure HTTPS server
	mux := http.NewServeMux()
	s.SetupRoutes(mux)

	s.server = &http.Server{
		Addr:      fmt.Sprintf(":%d", s.cfg.Port),
		Handler:   mux,
		TLSConfig: s.tlsConfig,
	}

	// Start HTTPS server
	log.Printf("Starting CyberChat server on port %d", s.cfg.Port)
	return s.server.ListenAndServeTLS(filepath.Join(s.cfg.DataDir, "cert.pem"), filepath.Join(s.cfg.DataDir, "key.pem"))
}

// Start starts the server
func (s *Server) Start() error {
	// Perform first time setup
	if err := s.FirstTimeSetup(); err != nil {
		return fmt.Errorf("first time setup failed: %w", err)
	}
	log.Println("First time setup completed successfully")

	// Start discovery service
	if err := s.discovery.Start(context.Background()); err != nil {
		return fmt.Errorf("failed to start discovery service: %w", err)
	}

	// Start peer update handler
	go func() {
		log.Printf("[Server] Starting peer update handler for %s", s.guid)
		for {
			select {
			case peer := <-s.peerMgr.Updates():
				log.Printf("[Server] Received peer update from discovery service: GUID=%s Port=%d", peer.GUID, peer.Port)
				s.handlePeerUpdate(peer)
			}
		}
	}()

	// Start HTTP server
	if err := s.startHTTPServer(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	// Stop discovery service
	if s.discovery != nil {
		s.discovery.Stop()
	}

	// Stop HTTP server
	if s.server != nil {
		// Create context with timeout for shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("error shutting down server: %w", err)
		}
	}

	return nil
}

// handlePeerUpdate processes a peer update
func (s *Server) handlePeerUpdate(peer peers.Peer) {
	// Broadcast peer update to web clients
	s.wsManager.Broadcast(struct {
		Type    string     `json:"type"`
		Content peers.Peer `json:"content"`
	}{
		Type:    "peer",
		Content: peer,
	})

	log.Printf("[Server] Broadcasted peer update to web clients")
}

// PeerStatus represents a peer's status for the API
type PeerStatus struct {
	GUID      string `json:"guid"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Port      int    `json:"port"`
	PublicKey string `json:"public_key,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Get our local IP
	var localIP string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				localIP = ipnet.IP.String()
				break
			}
		}
	}

	peers, err := s.db.GetAllPeers()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get peers: %v", err), http.StatusInternalServerError)
		return
	}

	status := struct {
		GUID      string       `json:"guid"`
		Name      string       `json:"name"`
		Port      int          `json:"port"`
		IPAddress string       `json:"ip_address"`
		Peers     []PeerStatus `json:"peers"`
	}{
		GUID:      s.guid,
		Name:      s.cfg.Name,
		Port:      s.cfg.Port,
		IPAddress: localIP,
		Peers:     make([]PeerStatus, 0),
	}

	for _, peer := range peers {
		// Convert public key to truncated base64 if available
		var pubKeyStr string
		if len(peer.PublicKey) > 0 {
			encoded := base64.StdEncoding.EncodeToString(peer.PublicKey)
			if len(encoded) > 16 {
				pubKeyStr = encoded[:16] + "..."
			} else {
				pubKeyStr = encoded
			}
		}

		// Only include group name if it's not null
		var groupName string
		if peer.GroupName.Valid {
			groupName = peer.GroupName.String
		}

		status.Peers = append(status.Peers, PeerStatus{
			GUID:      peer.GUID,
			Name:      peer.Username,
			IPAddress: peer.IPAddress,
			Port:      peer.Port,
			PublicKey: pubKeyStr,
			LastSeen:  peer.LastSeen.Format(time.RFC3339),
			GroupName: groupName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetDiscoveredPeers returns a list of discovered peers
func (s *Server) GetDiscoveredPeers() []discovery.Peer {
	if s.discovery == nil {
		return nil
	}
	return s.discovery.GetPeers()
}
