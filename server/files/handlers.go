package files

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Handlers contains HTTP handlers for file operations
type Handlers struct {
	db        DB
	guid      string
	apiKey    string
	wsManager WebSocketManager
}

// NewHandlers creates a new Handlers instance
func NewHandlers(db DB, guid string, apiKey string, wsManager WebSocketManager) *Handlers {
	return &Handlers{
		db:        db,
		guid:      guid,
		apiKey:    apiKey,
		wsManager: wsManager,
	}
}

// verifyAPIKey checks if the provided API key is valid
func (h *Handlers) verifyAPIKey(r *http.Request) bool {
	key := r.Header.Get("X-Client-API-Key")
	return key != "" && key == h.apiKey
}

// readerState holds the state for progress tracking
type readerState struct {
	*ProgressReader
	wsManager  WebSocketManager
	fileID     string
	filename   string
	size       int64
	clientIP   string
	transferID string
}

// HandleDownload handles file download requests
func (h *Handlers) HandleDownload(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	// Handle preflight
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract file ID from URL path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	fileID := parts[len(parts)-1]

	// Get file record from database
	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "Failed to get file record", http.StatusInternalServerError)
		return
	}
	if file == nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Get client IP for logging
	clientIP := r.Header.Get("X-Real-IP")
	if clientIP == "" {
		clientIP = r.RemoteAddr
		if i := strings.LastIndex(clientIP, ":"); i != -1 {
			clientIP = clientIP[:i]
		}
	}

	// Open file
	f, err := os.Open(file.Filepath)
	if err != nil {
		http.Error(w, "Failed to open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// Set response headers
	w.Header().Set("Content-Type", file.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, file.Filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", file.Size))

	// Generate transfer ID
	transferID := uuid.New().String()

	// Create reader state
	state := &readerState{
		wsManager:  h.wsManager,
		fileID:     fileID,
		filename:   file.Filename,
		size:       file.Size,
		clientIP:   clientIP,
		transferID: transferID,
	}

	// Create progress reader
	state.ProgressReader = &ProgressReader{
		Reader:     f,
		Size:       file.Size,
		Progress:   0,
		LastUpdate: time.Now(),
		OnProgress: func(bytesRead, totalSize int64) {
			// Only send updates every 500ms to avoid flooding
			now := time.Now()
			if now.Sub(state.LastUpdate) >= 500*time.Millisecond {
				progress := int((float64(bytesRead) / float64(totalSize)) * 100)

				if state.wsManager != nil {
					state.wsManager.Broadcast(struct {
						Type    string `json:"type"`
						Content struct {
							FileID     string  `json:"file_id"`
							Filename   string  `json:"filename"`
							Size       int64   `json:"size"`
							ClientIP   string  `json:"client_ip"`
							Status     string  `json:"status"`
							Progress   int     `json:"progress"`
							StartTime  int64   `json:"start_time"`
							TransferID string  `json:"transfer_id"`
							BytesRead  int64   `json:"bytes_read"`
							Speed      float64 `json:"speed"` // bytes per second
						} `json:"content"`
					}{
						Type: "file_transfer",
						Content: struct {
							FileID     string  `json:"file_id"`
							Filename   string  `json:"filename"`
							Size       int64   `json:"size"`
							ClientIP   string  `json:"client_ip"`
							Status     string  `json:"status"`
							Progress   int     `json:"progress"`
							StartTime  int64   `json:"start_time"`
							TransferID string  `json:"transfer_id"`
							BytesRead  int64   `json:"bytes_read"`
							Speed      float64 `json:"speed"`
						}{
							FileID:     state.fileID,
							Filename:   state.filename,
							Size:       state.size,
							ClientIP:   state.clientIP,
							Status:     "transferring",
							Progress:   progress,
							StartTime:  time.Now().Unix(),
							TransferID: state.transferID,
							BytesRead:  bytesRead,
							Speed:      float64(bytesRead) / time.Since(state.StartTime).Seconds(),
						},
					})
				}
				state.LastUpdate = now
			}
		},
	}

	// Notify about download start
	if h.wsManager != nil {
		h.wsManager.Broadcast(struct {
			Type    string `json:"type"`
			Content struct {
				FileID     string `json:"file_id"`
				Filename   string `json:"filename"`
				Size       int64  `json:"size"`
				ClientIP   string `json:"client_ip"`
				Status     string `json:"status"`
				Progress   int    `json:"progress"`
				StartTime  int64  `json:"start_time"`
				TransferID string `json:"transfer_id"`
			} `json:"content"`
		}{
			Type: "file_transfer",
			Content: struct {
				FileID     string `json:"file_id"`
				Filename   string `json:"filename"`
				Size       int64  `json:"size"`
				ClientIP   string `json:"client_ip"`
				Status     string `json:"status"`
				Progress   int    `json:"progress"`
				StartTime  int64  `json:"start_time"`
				TransferID string `json:"transfer_id"`
			}{
				FileID:     state.fileID,
				Filename:   state.filename,
				Size:       state.size,
				ClientIP:   state.clientIP,
				Status:     "starting",
				Progress:   0,
				StartTime:  time.Now().Unix(),
				TransferID: state.transferID,
			},
		})
	}

	// Stream file to response with progress tracking
	bytesWritten, err := io.Copy(w, state.ProgressReader)
	if err != nil {
		// Notify about failure
		if h.wsManager != nil {
			h.wsManager.Broadcast(struct {
				Type    string `json:"type"`
				Content struct {
					FileID     string `json:"file_id"`
					Filename   string `json:"filename"`
					Size       int64  `json:"size"`
					ClientIP   string `json:"client_ip"`
					Status     string `json:"status"`
					Error      string `json:"error"`
					TransferID string `json:"transfer_id"`
				} `json:"content"`
			}{
				Type: "file_transfer",
				Content: struct {
					FileID     string `json:"file_id"`
					Filename   string `json:"filename"`
					Size       int64  `json:"size"`
					ClientIP   string `json:"client_ip"`
					Status     string `json:"status"`
					Error      string `json:"error"`
					TransferID string `json:"transfer_id"`
				}{
					FileID:     state.fileID,
					Filename:   state.filename,
					Size:       state.size,
					ClientIP:   state.clientIP,
					Status:     "failed",
					Error:      err.Error(),
					TransferID: state.transferID,
				},
			})
		}
		http.Error(w, "Failed to stream file", http.StatusInternalServerError)
		return
	}

	// Notify about successful completion
	if h.wsManager != nil {
		h.wsManager.Broadcast(struct {
			Type    string `json:"type"`
			Content struct {
				FileID     string  `json:"file_id"`
				Filename   string  `json:"filename"`
				Size       int64   `json:"size"`
				ClientIP   string  `json:"client_ip"`
				Status     string  `json:"status"`
				TransferID string  `json:"transfer_id"`
				Duration   float64 `json:"duration"`
				AvgSpeed   float64 `json:"avg_speed"`
			} `json:"content"`
		}{
			Type: "file_transfer",
			Content: struct {
				FileID     string  `json:"file_id"`
				Filename   string  `json:"filename"`
				Size       int64   `json:"size"`
				ClientIP   string  `json:"client_ip"`
				Status     string  `json:"status"`
				TransferID string  `json:"transfer_id"`
				Duration   float64 `json:"duration"`
				AvgSpeed   float64 `json:"avg_speed"`
			}{
				FileID:     state.fileID,
				Filename:   state.filename,
				Size:       state.size,
				ClientIP:   state.clientIP,
				Status:     "completed",
				TransferID: state.transferID,
				Duration:   time.Since(state.StartTime).Seconds(),
				AvgSpeed:   float64(bytesWritten) / time.Since(state.StartTime).Seconds(),
			},
		})
	}
}

// ProgressReader wraps an io.Reader to track read progress
type ProgressReader struct {
	Reader     io.Reader
	Size       int64
	Progress   int64
	LastUpdate time.Time
	StartTime  time.Time
	OnProgress func(int64, int64)
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	if pr.StartTime.IsZero() {
		pr.StartTime = time.Now()
	}

	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.Progress += int64(n)
		if pr.OnProgress != nil {
			pr.OnProgress(pr.Progress, pr.Size)
		}
	}
	return n, err
}

// HandleUpload handles file path registration from the web client
func (h *Handlers) HandleUpload(w http.ResponseWriter, r *http.Request) {
	// Verify API key
	if !h.verifyAPIKey(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(100 << 20); err != nil { // 100MB max
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Get file path from form
	filePath := r.FormValue("filepath")
	if filePath == "" {
		http.Error(w, "Missing filepath", http.StatusBadRequest)
		return
	}

	// Get file ID and receiver GUID from form
	fileID := r.FormValue("file_id")
	if fileID == "" {
		http.Error(w, "Missing file_id", http.StatusBadRequest)
		return
	}

	// Get receiver GUID (optional for broadcast)
	receiverGUID := r.FormValue("receiver_guid")

	// Verify file exists and get its info
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "File not found or inaccessible", http.StatusBadRequest)
		return
	}

	// Save file record to database
	err = h.db.SaveFile(fileID, h.guid, receiverGUID, filepath.Base(filePath), filePath, fileInfo.Size(), "application/octet-stream")
	if err != nil {
		http.Error(w, "Failed to save file record", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleFilesystem handles filesystem browsing requests
func (h *Handlers) HandleFilesystem(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Content-Type", "application/json")

	// Handle preflight
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Verify API key
	if !h.verifyAPIKey(r) {
		http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Get query parameters
	path := r.URL.Query().Get("path")
	if path == "" || path == "~" {
		// Use home directory if available, otherwise fallback to root
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else if runtime.GOOS == "windows" {
			path = "C:"
		} else {
			path = "/"
		}
	} else if strings.HasPrefix(path, "~/") {
		// Expand ~ to home directory
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	// Clean and resolve the path
	path = filepath.Clean(path)

	// Get other parameters
	showHidden := r.URL.Query().Get("hidden") == "true"
	fileType := r.URL.Query().Get("type")
	if fileType == "" {
		fileType = "all"
	}

	// Read directory
	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Failed to read directory: %v"}`, err), http.StatusBadRequest)
		return
	}

	// Process entries
	var result struct {
		CurrentPath string      `json:"current_path"`
		ParentPath  string      `json:"parent_path"`
		IsRoot      bool        `json:"is_root"`
		Entries     []FileEntry `json:"entries"`
		Volumes     []string    `json:"volumes,omitempty"` // For Windows drives
	}

	result.CurrentPath = path
	result.ParentPath = filepath.Dir(path)
	result.IsRoot = path == "/" || (runtime.GOOS == "windows" && len(path) <= 3)

	// On Windows, list available drives at root
	if runtime.GOOS == "windows" && len(path) <= 3 {
		if drives, err := listWindowsDrives(); err == nil {
			result.Volumes = drives
		}
	}

	// Process entries
	result.Entries = make([]FileEntry, 0)
	for _, entry := range entries {
		// Skip hidden files unless requested
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		isDir := entry.IsDir()
		if fileType != "all" && ((fileType == "file" && isDir) || (fileType == "dir" && !isDir)) {
			continue
		}

		fileEntry := FileEntry{
			Name:       entry.Name(),
			Type:       "file",
			Path:       filepath.Join(path, entry.Name()),
			Size:       info.Size(),
			Modified:   info.ModTime(),
			IsHidden:   strings.HasPrefix(entry.Name(), "."),
			IsReadable: isReadable(filepath.Join(path, entry.Name())),
			IsWritable: isWritable(filepath.Join(path, entry.Name())),
		}

		if isDir {
			fileEntry.Type = "dir"
			fileEntry.Size = 0 // Don't show directory size
		} else {
			fileEntry.MimeType = getMimeType(entry.Name())
		}

		result.Entries = append(result.Entries, fileEntry)
	}

	// Sort entries (directories first, then alphabetically)
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].Type != result.Entries[j].Type {
			return result.Entries[i].Type == "dir"
		}
		return result.Entries[i].Name < result.Entries[j].Name
	})

	// Write JSON response
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Failed to encode response: %v"}`, err), http.StatusInternalServerError)
		return
	}
}

// HandleTruncate handles requests to remove all files
func (h *Handlers) HandleTruncate(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	// Handle preflight
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Verify API key
	if !h.verifyAPIKey(r) {
		http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Truncate files table
	if err := h.db.TruncateFiles(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Failed to truncate files: %v"}`, err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleListFiles returns a list of all shared files
func (h *Handlers) HandleListFiles(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAPIKey(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get all files from the database
	files, err := h.db.GetFiles()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get files: %v", err), http.StatusInternalServerError)
		return
	}

	// Return files as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// FileEntry represents a filesystem entry
type FileEntry struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	Modified   time.Time `json:"modified"`
	IsHidden   bool      `json:"is_hidden"`
	MimeType   string    `json:"mime_type,omitempty"`
	IsReadable bool      `json:"is_readable"`
	IsWritable bool      `json:"is_writable"`
}

// Helper functions for file permissions
func isReadable(path string) bool {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	file.Close()
	return true
}

func isWritable(path string) bool {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		// Try to create a temporary file in the directory
		tmpFile := filepath.Join(path, ".tmp_write_test")
		if err := os.WriteFile(tmpFile, []byte{}, 0666); err != nil {
			return false
		}
		os.Remove(tmpFile)
		return true
	}
	// For files, check if we can open for writing
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	file.Close()
	return true
}

// listWindowsDrives returns available Windows drive letters
func listWindowsDrives() ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}

	var drives []string
	for _, drive := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		path := string(drive) + ":\\"
		if _, err := os.Stat(path); err == nil {
			drives = append(drives, path)
		}
	}
	return drives, nil
}

// getMimeType returns the MIME type for a file based on its extension
func getMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// DB interface defines required database operations
type DB interface {
	SaveFile(fileID, senderGUID, receiverGUID, filename, filepath string, size int64, mimeType string) error
	GetFile(fileID string) (*FileRecord, error)
	TruncateFiles() error
	GetFiles() ([]FileRecord, error)
}

// FileRecord represents a file record from the database
type FileRecord struct {
	FileID       string
	SenderGUID   string
	ReceiverGUID string
	Filename     string
	Filepath     string
	Size         int64
	MimeType     string
	CreatedAt    string
}

// WebSocketManager interface for broadcasting messages
type WebSocketManager interface {
	Broadcast(message interface{})
}
