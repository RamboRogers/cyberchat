package keys

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"cyberchat/server/db"
)

// Manager handles key operations for the server
type Manager struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	keyFile    string
	db         *db.DB
}

// New creates a new key manager
func New(keyFile string, db *db.DB) *Manager {
	return &Manager{
		keyFile: keyFile,
		db:      db,
	}
}

// Setup generates or loads the server's key pair
func (m *Manager) Setup() error {
	// Check if keys already exist in database first
	if m.db != nil {
		_, privKey, err := m.db.GetKeys()
		if err == nil {
			// Parse keys from database
			block, _ := pem.Decode(privKey)
			if block != nil && block.Type == "RSA PRIVATE KEY" {
				privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
				if err == nil {
					m.privateKey = privateKey
					m.publicKey = &privateKey.PublicKey
					return nil
				}
			}
		}
	}

	// Check if keys exist in files
	if _, err := os.Stat(m.keyFile); err == nil {
		// Load existing keys
		keyData, err := os.ReadFile(m.keyFile)
		if err != nil {
			return fmt.Errorf("failed to read key file: %w", err)
		}

		block, _ := pem.Decode(keyData)
		if block == nil {
			return fmt.Errorf("failed to decode PEM block")
		}

		// Always use PKCS1 for private keys
		privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}

		m.privateKey = privateKey
		m.publicKey = &privateKey.PublicKey

		// Store keys in database if available
		if m.db != nil {
			if err := m.saveToDatabase(); err != nil {
				return fmt.Errorf("failed to save keys to database: %w", err)
			}
		}
		return nil
	}

	// Generate new key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate key pair: %w", err)
	}

	// Save private key - CONSISTENTLY using PKCS1
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyBytes,
	})

	if err := os.WriteFile(m.keyFile, keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	m.privateKey = privateKey
	m.publicKey = &privateKey.PublicKey

	// Store new keys in database if available
	if m.db != nil {
		if err := m.saveToDatabase(); err != nil {
			return fmt.Errorf("failed to save keys to database: %w", err)
		}
	}

	return nil
}

// saveToDatabase stores the current keys in the database
func (m *Manager) saveToDatabase() error {
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: x509.MarshalPKCS1PublicKey(m.publicKey),
	})
	privKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(m.privateKey),
	})
	return m.db.SaveKeys(pubKeyPEM, privKeyPEM)
}

// GetPrivateKey returns the current private key
func (m *Manager) GetPrivateKey() *rsa.PrivateKey {
	return m.privateKey
}

// GetPublicKey returns the current public key
func (m *Manager) GetPublicKey() *rsa.PublicKey {
	return m.publicKey
}
