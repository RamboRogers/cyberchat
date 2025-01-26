package messages

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	MaxMessageSize = 100 * 1024 * 1024 // 100MB
)

// MessageType represents the type of message content
type MessageType string

// MessageScope represents the scope of the message delivery
type MessageScope string

const (
	TypeText  MessageType = "text"
	TypeImage MessageType = "image"
	TypeFile  MessageType = "file"

	// Message scope constants
	ScopePrivate   MessageScope = "private"   // Message sent to a single peer
	ScopeBroadcast MessageScope = "broadcast" // Message sent to all peers
)

// Message represents a chat message
type Message struct {
	ID           string       `json:"id"`
	SenderGUID   string       `json:"sender_guid"`
	ReceiverGUID string       `json:"receiver_guid"`
	Type         MessageType  `json:"type"`
	Scope        MessageScope `json:"scope"`
	Content      []byte       `json:"content"`
	Timestamp    time.Time    `json:"timestamp"`
}

// WebMessage represents a message for web client communication
type WebMessage struct {
	ID           string       `json:"id"`
	SenderGUID   string       `json:"sender_guid"`
	ReceiverGUID string       `json:"receiver_guid"`
	Type         MessageType  `json:"type"`
	Scope        MessageScope `json:"scope"`
	Content      string       `json:"content"` // String content for web clients
	Timestamp    time.Time    `json:"timestamp"`
}

// MessageDeliveryStatus represents the delivery status for a single peer
type MessageDeliveryStatus struct {
	PeerGUID string    `json:"peer_guid"`
	PeerName string    `json:"peer_name"`
	Success  bool      `json:"success"`
	Error    string    `json:"error,omitempty"`
	Time     time.Time `json:"time"`
}

// MessageDeliveryReport contains the overall message delivery status
type MessageDeliveryReport struct {
	MessageID    string                  `json:"message_id"`
	TotalPeers   int                     `json:"total_peers"`
	Succeeded    int                     `json:"succeeded"`
	Failed       int                     `json:"failed"`
	DeliveryTime time.Time               `json:"delivery_time"`
	PeerStatuses []MessageDeliveryStatus `json:"peer_statuses"`
	Summary      string                  `json:"summary"` // Human-readable delivery summary
}

// NewMessage creates a new message
func NewMessage(senderGUID, receiverGUID string, msgType MessageType, content []byte) *Message {
	return &Message{
		ID:           uuid.New().String(),
		SenderGUID:   senderGUID,
		ReceiverGUID: receiverGUID,
		Type:         msgType,
		Scope:        ScopePrivate, // Default to private messages
		Content:      content,
		Timestamp:    time.Now(),
	}
}

// NewWebMessage creates a new message with string content for web clients
func NewWebMessage(senderGUID string, receiverGUID string, messageType MessageType, content string) *Message {
	return &Message{
		ID:           uuid.New().String(),
		SenderGUID:   senderGUID,
		ReceiverGUID: receiverGUID,
		Type:         messageType,
		Scope:        "broadcast",
		Content:      []byte(content),
		Timestamp:    time.Now(),
	}
}

// EncryptedMessage represents an encrypted message ready for transmission
type EncryptedMessage struct {
	ID           string       `json:"id"`
	SenderGUID   string       `json:"sender_guid"`
	ReceiverGUID string       `json:"receiver_guid"`
	Type         string       `json:"type"`
	Scope        MessageScope `json:"scope"`
	Content      string       `json:"content"` // Base64 encoded encrypted content
	Timestamp    time.Time    `json:"timestamp"`
}

// Encrypt encrypts a message for the receiver using their public key
func (m *Message) Encrypt(receiverKey *rsa.PublicKey) (*EncryptedMessage, error) {
	// Encrypt content
	label := []byte(m.ID) // Use message ID as label for additional security
	ciphertext, err := rsa.EncryptOAEP(
		sha256.New(),
		rand.Reader,
		receiverKey,
		m.Content,
		label,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt message: %w", err)
	}

	// Encode encrypted content as base64
	encoded := base64.StdEncoding.EncodeToString(ciphertext)

	return &EncryptedMessage{
		ID:           m.ID,
		SenderGUID:   m.SenderGUID,
		ReceiverGUID: m.ReceiverGUID,
		Type:         string(m.Type),
		Scope:        m.Scope,
		Content:      encoded,
		Timestamp:    m.Timestamp,
	}, nil
}

// Decrypt decrypts an encrypted message using the receiver's private key
func (em *EncryptedMessage) Decrypt(privateKey *rsa.PrivateKey) (*Message, error) {
	// Decode base64 content
	ciphertext, err := base64.StdEncoding.DecodeString(em.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode message content: %w", err)
	}

	// Decrypt content
	label := []byte(em.ID) // Use message ID as label
	plaintext, err := rsa.DecryptOAEP(
		sha256.New(),
		rand.Reader,
		privateKey,
		ciphertext,
		label,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt message: %w", err)
	}

	return &Message{
		ID:           em.ID,
		SenderGUID:   em.SenderGUID,
		ReceiverGUID: em.ReceiverGUID,
		Type:         MessageType(em.Type),
		Scope:        em.Scope,
		Content:      plaintext,
		Timestamp:    em.Timestamp,
	}, nil
}

// ValidateContent checks if the message content is valid
func (m *Message) ValidateContent() error {
	if len(m.Content) == 0 {
		return fmt.Errorf("message content cannot be empty")
	}

	if len(m.Content) > MaxMessageSize {
		return fmt.Errorf("message content exceeds maximum size of %d bytes", MaxMessageSize)
	}

	switch m.Type {
	case TypeText:
		// Text messages don't need additional validation
		return nil
	case TypeImage:
		// TODO: Add image format validation
		return nil
	case TypeFile:
		// TODO: Add file type validation
		return nil
	default:
		return fmt.Errorf("invalid message type: %s", m.Type)
	}
}

// ToJSON converts a message to JSON
func (m *Message) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}

// FromJSON creates a message from JSON
func FromJSON(data []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}
	return &m, nil
}

// ToWebMessage converts a Message to a WebMessage
func (m *Message) ToWebMessage() *WebMessage {
	return &WebMessage{
		ID:           m.ID,
		SenderGUID:   m.SenderGUID,
		ReceiverGUID: m.ReceiverGUID,
		Type:         m.Type,
		Scope:        m.Scope,
		Content:      string(m.Content),
		Timestamp:    m.Timestamp,
	}
}

// GetContentString returns the message content as a string
func (m *Message) GetContentString() string {
	return string(m.Content)
}
