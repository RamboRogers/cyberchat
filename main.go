package main

import (
	"context"
	"cyberchat/server/config"
	"cyberchat/server"
	"cyberchat/server/db"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"cyberchat/server/telemetry"
    _ "embed"
)

const (
	version = "0.1.0a"
)

//go:embed private.txt
var privateConfig string

var (
	telemetryClient *telemetry.Client
)

// parsePrivateConfig parses the embedded configuration
func parsePrivateConfig() (server, token string, err error) {
	lines := strings.Split(privateConfig, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "TELEMETRY_SERVER":
			server = value
		case "TELEMETRY_TOKEN":
			token = value
		}
	}

	if server == "" {
		return "", "", fmt.Errorf("TELEMETRY_SERVER not found in embedded config")
	}
	if token == "" {
		return "", "", fmt.Errorf("TELEMETRY_TOKEN not found in embedded config")
	}

	return server, token, nil
}

// resetData removes the database and keys for a fresh start
func resetData(dataDir string) error {
	log.Printf("Resetting CyberChat data in: %s", dataDir)

	// List of files/directories to remove
	toRemove := []string{
		"cyberchat.db", // Database
		"cert.pem",     // Certificate
		"key.pem",      // Private key
	}

	for _, file := range toRemove {
		path := filepath.Join(dataDir, file)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	log.Printf("Reset complete. All data has been wiped.")
	return nil
}

// printUsage prints the usage help menu
func printUsage() {
	const cmd = "cyberchat"

	fmt.Fprintf(os.Stderr, "\033[1mCyberChat v%s\033[0m - Secure P2P Chat Application\n\n", version)
	fmt.Fprintf(os.Stderr, "Quick Start:\n")
	fmt.Fprintf(os.Stderr, "  go run .                    # Run directly with Go\n")
	fmt.Fprintf(os.Stderr, "  go build && ./cyberchat     # Build and run binary\n\n")
	fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", cmd)
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -d string\n\tCustom home directory for CyberChat data (default: ~/.cyberchat)\n")
	fmt.Fprintf(os.Stderr, "  -p int\n\tPort to listen on (default: 7331)\n")
	fmt.Fprintf(os.Stderr, "  -n string\n\tName to use for this peer (default: CyberChat)\n")
	fmt.Fprintf(os.Stderr, "  -r\n\tReset all data and start fresh\n")
	fmt.Fprintf(os.Stderr, "  -v\n\tShow version information\n")
	fmt.Fprintf(os.Stderr, "  -debug\n\tEnable debug logging\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s -p 7332 -n \"Alice\"     # Run on custom port with custom name\n", cmd)
	fmt.Fprintf(os.Stderr, "  %s -d ~/my-cyberchat           # Use custom data directory\n", cmd)
	fmt.Fprintf(os.Stderr, "  %s -r                          # Reset to factory fresh state\n\n", cmd)
	fmt.Fprintf(os.Stderr, "For more information, visit: https://github.com/yourusername/cyberchat\n")
}

// logFilter filters out unwanted log messages
type logFilter struct {
	output io.Writer
	debug  bool
}

func (f *logFilter) Write(p []byte) (n int, err error) {
	msg := string(p)

	// Always skip mDNS INFO messages
	if strings.Contains(msg, "[INFO] mdns:") {
		return len(p), nil
	}

	// In non-debug mode, filter out noise
	if !f.debug {
		// Filter out TLS handshake errors
		if strings.Contains(msg, "TLS handshake error") {
			return len(p), nil
		}
		// Filter out detailed discovery messages
		if strings.Contains(msg, "[Discovery]") && !strings.Contains(msg, "Starting") {
			return len(p), nil
		}
		// Filter out detailed debug messages
		if strings.Contains(msg, "DEBUG") {
			return len(p), nil
		}
	}

	return f.output.Write(p)
}

// printBanner displays the CyberChat ASCII art banner
func printBanner(debug bool, port int) {
	// ANSI color codes
	purple := "\033[38;5;135m" // Matching the SHAME purple
	blue := "\033[38;5;39m"    // For the description
	green := "\033[38;5;47m"   // For INFO messages
	yellow := "\033[38;5;227m" // For URLs/links
	reset := "\033[0m"

	// Current time for log-style prefix
	timeStr := time.Now().Format("2006-01-02 15:04:05")

	banner := purple + `
  ____      _                ____ _           _
 / ___|   _| |__   ___ _ __ / ___| |__   __ _| |_
| |  | | | | '_ \ / _ \ '__| |   | '_ \ / _` + "`" + ` | __|
| |__| |_| | |_) |  __/ |  | |___| | | | (_| | |_
 \____\__, |_.__/ \___|_|   \____|_| |_|\__,_|\__|
      |___/
` + reset + blue + `
🔐 Secure P2P Service v` + version + reset + `
🌟 By RamboRogers` + yellow + ` (github.com/RamboRogers/cyberchat)` + reset + `
🎓 Rogerscissp` + yellow + ` (X/Twitter)` + reset + `


` + blue + `🌐 Access Client:` + reset + `

` + yellow + fmt.Sprintf("https://127.0.0.1:%d", port) + reset + `

`
	if debug {
		banner += blue + `
🐛 Debug Mode Enabled` + reset
	}

	banner += `
` + green + timeStr + ` | INFO | Starting CyberChat...` + reset + `
` + green + timeStr + ` | INFO | Press Ctrl+C to exit` + reset + `

`
	fmt.Print(banner)
}

func main() {

	// Initialize telemetry client in background
	go func() {
			server, token, err := parsePrivateConfig()
			if err != nil {
				log.Printf("Warning: Failed to parse embedded config: %v", err)
				return
			}

			var clientErr error
			telemetryClient, clientErr = telemetry.NewClient(server, token, version)
			if clientErr != nil {
				// Log error but continue - telemetry is non-critical
				log.Printf("Failed to initialize telemetry: %v", clientErr)
				return
			}
			if err := telemetryClient.Start(); err != nil {
				// Log error but continue - telemetry is non-critical
				log.Printf("Failed to start telemetry: %v", err)
				telemetryClient = nil // Disable telemetry on error
			}
	}()

	// Parse command line flags first
	customDir := flag.String("d", "", "Custom home directory for CyberChat data")
	customPort := flag.Int("p", 7331, "Port to listen on")
	customName := flag.String("n", "", "Name to use for this peer")
	resetFlag := flag.Bool("r", false, "Reset all data and start fresh")
	versionFlag := flag.Bool("v", false, "Show version information")
	debugFlag := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Set up logging with debug flag
	log.SetFlags(log.LstdFlags)
	defaultLogger := log.Default()
	log.SetOutput(&logFilter{output: defaultLogger.Writer(), debug: *debugFlag})

	// Display banner with debug status and port
	printBanner(*debugFlag, *customPort)

	// Handle version flag before any other operations
	if *versionFlag {
		fmt.Printf("CyberChat v%s\n", version)
		os.Exit(0)
	}

	// Handle reset flag
	if *resetFlag {
		// Determine data directory
		var dataDir string
		if *customDir != "" {
			dataDir = *customDir
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				log.Fatalf("Failed to get home directory: %v", err)
			}
			dataDir = filepath.Join(homeDir, ".cyberchat")
		}

		if err := resetData(dataDir); err != nil {
			log.Fatalf("Failed to reset data: %v", err)
		}
		fmt.Printf("CyberChat data reset complete. You can now start fresh.\n")
		os.Exit(0)
	}

	// Validate port range
	if *customPort < 1024 || *customPort > 65535 {
		fmt.Fprintf(os.Stderr, "\033[31mError:\033[0m Port must be between 1024 and 65535\n\n")
		printUsage()
		os.Exit(1)
	}

	// Create context that will be canceled on interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupts
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down...")
		cancel()
	}()

	// Determine data directory
	var dataDir string
	if *customDir != "" {
		dataDir = *customDir
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Failed to get home directory: %v", err)
		}
		dataDir = filepath.Join(homeDir, ".cyberchat")
	}

	// Create default config with custom settings
	defaultConfig := &config.Config{
		Port:            *customPort,
		TrustSelfSigned: true,
		Name:            "CyberChat",
		DataDir:         dataDir,
		Debug:           *debugFlag,
	}

	// If custom name provided, override default
	if *customName != "" {
		defaultConfig.Name = *customName
	}

	// Ensure data directory exists
	if err := os.MkdirAll(defaultConfig.DataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize database
	database, err := db.New(filepath.Join(defaultConfig.DataDir, "cyberchat.db"), *debugFlag)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize schema
	if err := database.InitSchema(); err != nil {
		log.Fatalf("Failed to initialize database schema: %v", err)
	}

	// Get or create config from database
	cfg, err := database.GetConfig()
	if err != nil {
		log.Printf("Failed to get config from database, using defaults: %v", err)
		cfg = defaultConfig
		// Save default config
		if err := database.SaveConfig(cfg); err != nil {
			log.Printf("Warning: Failed to save default config: %v", err)
		}
	} else {
		// Override with command line arguments if provided
		if *customPort != 7331 {
			cfg.Port = *customPort
		}
		if *customDir != "" {
			cfg.DataDir = *customDir
		}
		if *customName != "" {
			cfg.Name = *customName
		}
		// Always ensure TrustSelfSigned is true
		cfg.TrustSelfSigned = true
		// Save updated config
		if err := database.SaveConfig(cfg); err != nil {
			log.Printf("Warning: Failed to save updated config: %v", err)
		}
	}

	// Create server instance
	s, err := server.New(cfg, database)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Perform first time setup if needed
	if err := s.FirstTimeSetup(); err != nil {
		log.Fatalf("First time setup failed: %v", err)
	}

	if err := s.StartServer(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
