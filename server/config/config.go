package config

// Config holds server configuration options
type Config struct {
	Port            int    `json:"port"`              // Port to listen on
	TrustSelfSigned bool   `json:"trust_self_signed"` // Whether to trust self-signed certificates
	Name            string `json:"name"`              // Name to advertise to other peers
	DataDir         string `json:"data_dir"`          // Directory for storing data
	Debug           bool   `json:"debug"`             // Whether to enable debug logging
}
