package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"cyberchat/server/config"
	"cyberchat/server/messages"

	_ "github.com/mattn/go-sqlite3"
)

// DB represents the database connection
type DB struct {
	conn   *sql.DB
	dbPath string
	debug  bool
}

// New creates a new database connection
func New(dbPath string, debug bool) (*DB, error) {
	// Ensure the database directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open SQLite database
	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create database instance
	db := &DB{
		conn:   conn,
		dbPath: dbPath,
		debug:  debug,
	}

	// Initialize schema
	if err := db.InitSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return db, nil
}

// DefaultConfig returns the default database configuration
func DefaultConfig() *config.Config {
	// Get user's home directory in a cross-platform way
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home directory cannot be determined
		homeDir = "."
	}

	// Use filepath.Join for cross-platform path construction
	dataDir := filepath.Join(homeDir, ".cyberchat")

	return &config.Config{
		DataDir: dataDir,
		Debug:   false,
	}
}

// TestConfig returns a configuration suitable for testing
func TestConfig() *config.Config {
	// Get user's home directory in a cross-platform way
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	// Use filepath.Join for cross-platform path construction
	dataDir := filepath.Join(homeDir, ".cyberchat_test")

	return &config.Config{
		DataDir: dataDir,
		Debug:   true,
	}
}

// InitSchema initializes the database schema
func (db *DB) InitSchema() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY,
			key TEXT NOT NULL UNIQUE,
			value TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS peers (
			id INTEGER PRIMARY KEY,
			guid TEXT NOT NULL UNIQUE,
			username TEXT NOT NULL,
			public_key TEXT,
			ip_address TEXT NOT NULL,
			port INTEGER NOT NULL,
			trust_level INTEGER DEFAULT 0,
			group_name TEXT,
			last_seen TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY,
			message_id TEXT NOT NULL UNIQUE,
			sender_guid TEXT NOT NULL,
			receiver_guid TEXT NOT NULL,
			content BLOB NOT NULL,
			type TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'private',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			source_ip TEXT,
			FOREIGN KEY(sender_guid) REFERENCES peers(guid),
			FOREIGN KEY(receiver_guid) REFERENCES peers(guid)
		)`,
		`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY,
			file_id TEXT NOT NULL UNIQUE,
			sender_guid TEXT NOT NULL,
			receiver_guid TEXT NOT NULL,
			filename TEXT NOT NULL,
			filepath TEXT NOT NULL,
			size INTEGER NOT NULL,
			mime_type TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(sender_guid) REFERENCES peers(guid),
			FOREIGN KEY(receiver_guid) REFERENCES peers(guid)
		)`,
		`CREATE TABLE IF NOT EXISTS relays (
			id INTEGER PRIMARY KEY,
			peer_guid TEXT NOT NULL,
			allowed_sender TEXT NOT NULL,
			allowed_receiver TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(peer_guid) REFERENCES peers(guid)
		)`,
	}

	for _, query := range queries {
		if _, err := db.conn.Exec(query); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	return nil
}

// SaveGUID stores the server's GUID in settings
func (db *DB) SaveGUID(guid string) error {
	query := `INSERT INTO settings (key, value) VALUES ('guid', ?)`
	if _, err := db.conn.Exec(query, guid); err != nil {
		return fmt.Errorf("failed to save GUID: %w", err)
	}
	return nil
}

// GetGUID retrieves the server's GUID from settings
func (db *DB) GetGUID() (string, error) {
	var guid string
	query := `SELECT value FROM settings WHERE key = 'guid'`
	err := db.conn.QueryRow(query).Scan(&guid)
	if err != nil {
		return "", fmt.Errorf("failed to get GUID: %w", err)
	}
	return guid, nil
}

// SaveKeys stores the server's RSA keys in settings in PEM format
func (db *DB) SaveKeys(publicKey, privateKey []byte) error {
	// Ensure keys are in PEM format
	if !bytes.HasPrefix(publicKey, []byte("-----BEGIN RSA PUBLIC KEY-----")) ||
		!bytes.HasPrefix(privateKey, []byte("-----BEGIN RSA PRIVATE KEY-----")) {
		return fmt.Errorf("keys must be in PEM format")
	}

	queries := []struct {
		key   string
		value []byte
	}{
		{"public_key", publicKey},
		{"private_key", privateKey},
	}

	for _, q := range queries {
		query := `INSERT INTO settings (key, value) VALUES (?, ?)`
		if _, err := db.conn.Exec(query, q.key, q.value); err != nil {
			return fmt.Errorf("failed to save %s: %w", q.key, err)
		}
	}
	return nil
}

// GetKeys retrieves the server's RSA keys from settings
func (db *DB) GetKeys() (publicKey, privateKey []byte, err error) {
	query := `SELECT value FROM settings WHERE key = ?`

	err = db.conn.QueryRow(query, "public_key").Scan(&publicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get public key: %w", err)
	}

	err = db.conn.QueryRow(query, "private_key").Scan(&privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get private key: %w", err)
	}

	// Verify PEM format
	if !bytes.HasPrefix(publicKey, []byte("-----BEGIN RSA PUBLIC KEY-----")) ||
		!bytes.HasPrefix(privateKey, []byte("-----BEGIN RSA PRIVATE KEY-----")) {
		return nil, nil, fmt.Errorf("invalid key format in database")
	}

	return publicKey, privateKey, nil
}

// CleanupOldMessages removes messages older than the specified duration
func (db *DB) CleanupOldMessages(ctx context.Context, age time.Duration) error {
	cutoff := time.Now().Add(-age)
	query := `DELETE FROM messages WHERE created_at < ?`

	result, err := db.conn.ExecContext(ctx, query, cutoff)
	if err != nil {
		return fmt.Errorf("failed to cleanup old messages: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		log.Printf("Warning: couldn't get affected rows count: %v", err)
		return nil
	}

	log.Printf("Cleaned up %d old messages", rows)
	return nil
}

// SaveMessage stores a message in the database
func (db *DB) SaveMessage(msg *messages.Message, sourceIP string) error {
	query := `
		INSERT INTO messages (
			message_id, sender_guid, receiver_guid,
			content, type, scope, created_at, source_ip
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		msg.ID,
		msg.SenderGUID,
		msg.ReceiverGUID,
		msg.Content,
		string(msg.Type),
		string(msg.Scope),
		msg.Timestamp,
		sourceIP,
	)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}
	return nil
}

// GetMessages retrieves messages from the database
func (db *DB) GetMessages(guid string, since time.Time, limit int) ([]*messages.Message, error) {
	query := `
		SELECT message_id, sender_guid, receiver_guid, content, type, scope, created_at
		FROM messages
		WHERE (receiver_guid = ? OR sender_guid = ? OR scope = 'broadcast')
		AND created_at > ?
		ORDER BY created_at DESC
		LIMIT ?
	`
	rows, err := db.conn.Query(query, guid, guid, since, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var msgs []*messages.Message
	for rows.Next() {
		var msg messages.Message
		err := rows.Scan(
			&msg.ID,
			&msg.SenderGUID,
			&msg.ReceiverGUID,
			&msg.Content,
			&msg.Type,
			&msg.Scope,
			&msg.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		msgs = append(msgs, &msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	return msgs, nil
}

// SaveConfig stores the server configuration
func (db *DB) SaveConfig(config *config.Config) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Save full config
	query := `INSERT OR REPLACE INTO settings (key, value) VALUES ('config', ?)`
	if _, err := db.conn.Exec(query, string(data)); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Save name separately for easy access
	nameQuery := `INSERT OR REPLACE INTO settings (key, value) VALUES ('name', ?)`
	if _, err := db.conn.Exec(nameQuery, config.Name); err != nil {
		return fmt.Errorf("failed to save name: %w", err)
	}

	return nil
}

// GetConfig retrieves the server configuration
func (db *DB) GetConfig() (*config.Config, error) {
	var data string
	query := `SELECT value FROM settings WHERE key = 'config'`
	err := db.conn.QueryRow(query).Scan(&data)
	if err == sql.ErrNoRows {
		// Try to get name from settings
		var name string
		nameQuery := `SELECT value FROM settings WHERE key = 'name'`
		if err := db.conn.QueryRow(nameQuery).Scan(&name); err == nil {
			return &config.Config{
				Port:            7331,
				TrustSelfSigned: false,
				Name:            name,
				DataDir:         filepath.Join(os.Getenv("HOME"), ".cyberchat"),
			}, nil
		}
		// Return default config if no name found
		return &config.Config{
			Port:            7331,
			TrustSelfSigned: false,
			Name:            "Anonymous",
			DataDir:         filepath.Join(os.Getenv("HOME"), ".cyberchat"),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config: %w", err)
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Update name from settings if available
	var name string
	nameQuery := `SELECT value FROM settings WHERE key = 'name'`
	if err := db.conn.QueryRow(nameQuery).Scan(&name); err == nil {
		cfg.Name = name
	}

	return &cfg, nil
}

// SaveName stores the server name in settings
func (db *DB) SaveName(name string) error {
	query := `INSERT OR REPLACE INTO settings (key, value) VALUES ('name', ?)`
	if _, err := db.conn.Exec(query, name); err != nil {
		return fmt.Errorf("failed to save name: %w", err)
	}
	return nil
}

// GetName retrieves the server name from settings
func (db *DB) GetName() (string, error) {
	var name string
	query := `SELECT value FROM settings WHERE key = 'name'`
	err := db.conn.QueryRow(query).Scan(&name)
	if err == sql.ErrNoRows {
		return "Anonymous", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get name: %w", err)
	}
	return name, nil
}

// GetMessageCount returns the total number of messages in the database
func (db *DB) GetMessageCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get message count: %w", err)
	}
	return count, nil
}

// RecentMessage represents a message with additional debug information
type RecentMessage struct {
	*messages.Message
	SourceIP string `json:"source_ip"`
}

// GetRecentMessages returns the last n messages with source IPs
func (db *DB) GetRecentMessages(limit int) ([]RecentMessage, error) {
	query := `
		SELECT message_id, sender_guid, receiver_guid, content, type, created_at, source_ip
		FROM messages
		ORDER BY created_at DESC
		LIMIT ?
	`
	rows, err := db.conn.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent messages: %w", err)
	}
	defer rows.Close()

	var msgs []RecentMessage
	for rows.Next() {
		var msg RecentMessage
		msg.Message = &messages.Message{}
		var typeStr string
		err := rows.Scan(
			&msg.Message.ID,
			&msg.Message.SenderGUID,
			&msg.Message.ReceiverGUID,
			&msg.Message.Content,
			&typeStr,
			&msg.Message.Timestamp,
			&msg.SourceIP,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		msg.Message.Type = messages.MessageType(typeStr)
		msgs = append(msgs, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	return msgs, nil
}

// SavePeer stores or updates a peer in the database
func (db *DB) SavePeer(guid string, ip string, port int, publicKey []byte, name string) error {
	// First check if peer exists and if data is actually different
	existing, err := db.GetPeer(guid)
	if err == nil && existing != nil {
		// Check if anything has actually changed
		if existing.IPAddress == ip &&
			existing.Port == port &&
			existing.Username == name &&
			((publicKey == nil && len(existing.PublicKey) == 0) ||
				(publicKey != nil && bytes.Equal(publicKey, existing.PublicKey))) {
			return nil // No changes needed
		}
	}

	now := time.Now()
	query := `
		INSERT INTO peers (
			guid, username, public_key, ip_address, port, last_seen
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(guid) DO UPDATE SET
			ip_address = excluded.ip_address,
			port = excluded.port,
			username = CASE
				WHEN excluded.username != '' THEN excluded.username
				ELSE username
			END,
			public_key = CASE
				WHEN excluded.public_key IS NOT NULL AND length(excluded.public_key) > 0 THEN excluded.public_key
				ELSE public_key
			END,
			last_seen = ?
	`
	if name == "" {
		name = fmt.Sprintf("Peer-%s", guid[:8]) // Default username using first 8 chars of GUID
	}

	// Convert nil public key to empty string for SQLite
	var pubKeyStr string
	if publicKey != nil {
		pubKeyStr = string(publicKey)
	}

	// Only log in debug mode
	if db.debug {
		log.Printf("[DB] Saving peer: GUID=%s IP=%s Port=%d Name=%s PubKey=%v LastSeen=%v",
			guid, ip, port, name, len(pubKeyStr) > 0, now)
	}

	_, err = db.conn.Exec(query, guid, name, pubKeyStr, ip, port, now, now)
	if err != nil {
		return fmt.Errorf("failed to save peer: %w", err)
	}

	// Only verify and log in debug mode
	if db.debug {
		saved, err := db.GetPeer(guid)
		if err != nil {
			log.Printf("[DB] Warning: Could not verify peer save: %v", err)
		} else if saved == nil {
			log.Printf("[DB] Warning: Peer not found after save: %s", guid)
		} else {
			log.Printf("[DB] Successfully saved/updated peer: GUID=%s Name=%s PubKey=%v LastSeen=%v",
				saved.GUID, saved.Username, len(saved.PublicKey) > 0, saved.LastSeen)
		}
	}

	return nil
}

// Peer represents a peer in the database
type Peer struct {
	GUID       string
	Username   string
	PublicKey  []byte
	IPAddress  string
	Port       int
	TrustLevel int
	GroupName  sql.NullString // Changed to sql.NullString to handle NULL
	LastSeen   time.Time
}

// GetPeer retrieves a peer from the database by GUID
func (db *DB) GetPeer(guid string) (*Peer, error) {
	query := `
		SELECT guid, username, public_key, ip_address, port, trust_level, group_name, last_seen
		FROM peers
		WHERE guid = ?
	`
	var peer Peer
	err := db.conn.QueryRow(query, guid).Scan(
		&peer.GUID,
		&peer.Username,
		&peer.PublicKey,
		&peer.IPAddress,
		&peer.Port,
		&peer.TrustLevel,
		&peer.GroupName, // Will now handle NULL correctly
		&peer.LastSeen,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get peer: %w", err)
	}
	return &peer, nil
}

// GetAllPeers retrieves all peers from the database
func (db *DB) GetAllPeers() ([]*Peer, error) {
	query := `
		SELECT guid, username, public_key, ip_address, port, trust_level, group_name, last_seen
		FROM peers
		ORDER BY last_seen DESC
	`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query peers: %w", err)
	}
	defer rows.Close()

	var peers []*Peer
	for rows.Next() {
		var peer Peer
		err := rows.Scan(
			&peer.GUID,
			&peer.Username,
			&peer.PublicKey,
			&peer.IPAddress,
			&peer.Port,
			&peer.TrustLevel,
			&peer.GroupName, // Will now handle NULL correctly
			&peer.LastSeen,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan peer: %w", err)
		}
		peers = append(peers, &peer)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating peers: %w", err)
	}

	return peers, nil
}

// DeletePeer removes a peer from the database by GUID
func (db *DB) DeletePeer(guid string) error {
	result, err := db.conn.Exec("DELETE FROM peers WHERE guid = ?", guid)
	if err != nil {
		return fmt.Errorf("failed to delete peer: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("peer not found")
	}

	return nil
}

// GetClientAPIKey retrieves the stored client API key from settings
func (db *DB) GetClientAPIKey() (string, error) {
	var value string
	err := db.conn.QueryRow("SELECT value FROM settings WHERE key = 'client_api_key' LIMIT 1").Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get client API key: %w", err)
	}
	return value, nil
}

// SaveClientAPIKey stores the client API key in settings
func (db *DB) SaveClientAPIKey(key string) error {
	_, err := db.conn.Exec(`
		INSERT INTO settings (key, value, updated_at)
		VALUES ('client_api_key', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, key)
	if err != nil {
		return fmt.Errorf("failed to save client API key: %w", err)
	}
	return nil
}

// TruncateMessages removes all messages from the database and reclaims space
func (db *DB) TruncateMessages() error {
	// Start a transaction
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete all messages
	if _, err := tx.Exec("DELETE FROM messages"); err != nil {
		return fmt.Errorf("failed to truncate messages: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Vacuum the database to reclaim space and optimize
	if _, err := db.conn.Exec("VACUUM"); err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}

	return nil
}

// SaveFile stores a file record in the database
func (db *DB) SaveFile(fileID, senderGUID, receiverGUID, filename, filepath string, size int64, mimeType string) error {
	query := `
		INSERT INTO files (
			file_id, sender_guid, receiver_guid, filename, filepath, size, mime_type
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query, fileID, senderGUID, receiverGUID, filename, filepath, size, mimeType)
	if err != nil {
		return fmt.Errorf("failed to save file: %w", err)
	}
	return nil
}

// GetFile retrieves a file record by its ID
func (db *DB) GetFile(fileID string) (*FileRecord, error) {
	query := `
		SELECT file_id, sender_guid, receiver_guid, filename, filepath, size, mime_type, created_at
		FROM files
		WHERE file_id = ?
	`
	var file FileRecord
	err := db.conn.QueryRow(query, fileID).Scan(
		&file.FileID,
		&file.SenderGUID,
		&file.ReceiverGUID,
		&file.Filename,
		&file.Filepath,
		&file.Size,
		&file.MimeType,
		&file.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}
	return &file, nil
}

// FileRecord represents a file in the database
type FileRecord struct {
	FileID       string
	SenderGUID   string
	ReceiverGUID string
	Filename     string
	Filepath     string
	Size         int64
	MimeType     string
	CreatedAt    time.Time
}

// TruncateFiles removes all files from the database
func (db *DB) TruncateFiles() error {
	query := `DELETE FROM files`
	_, err := db.conn.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to truncate files: %w", err)
	}
	return nil
}

// GetPeersLastSeenAfter retrieves all peers last seen after the specified time
func (db *DB) GetPeersLastSeenAfter(cutoff time.Time) ([]*Peer, error) {
	query := `
		SELECT guid, username, public_key, ip_address, port, trust_level, group_name, last_seen
		FROM peers
		WHERE last_seen > ?
		ORDER BY last_seen DESC
	`
	rows, err := db.conn.Query(query, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent peers: %w", err)
	}
	defer rows.Close()

	var peers []*Peer
	for rows.Next() {
		var peer Peer
		err := rows.Scan(
			&peer.GUID,
			&peer.Username,
			&peer.PublicKey,
			&peer.IPAddress,
			&peer.Port,
			&peer.TrustLevel,
			&peer.GroupName,
			&peer.LastSeen,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan peer: %w", err)
		}
		peers = append(peers, &peer)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating peers: %w", err)
	}

	return peers, nil
}

// MessageExists checks if a message with the given ID already exists
func (db *DB) MessageExists(messageID string) (bool, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM messages WHERE id = ?", messageID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check message existence: %w", err)
	}
	return count > 0, nil
}

// GetFiles returns all files from the database
func (db *DB) GetFiles() ([]FileRecord, error) {
	rows, err := db.conn.Query(`
		SELECT file_id, sender_guid, receiver_guid, filename, filepath, size, mime_type, created_at
		FROM files
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query files: %w", err)
	}
	defer rows.Close()

	var files []FileRecord
	for rows.Next() {
		var file FileRecord
		var createdAt time.Time
		err := rows.Scan(
			&file.FileID,
			&file.SenderGUID,
			&file.ReceiverGUID,
			&file.Filename,
			&file.Filepath,
			&file.Size,
			&file.MimeType,
			&createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file row: %w", err)
		}
		file.CreatedAt = createdAt
		files = append(files, file)
	}

	return files, nil
}
