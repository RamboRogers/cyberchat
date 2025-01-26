package peers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"cyberchat/server/discovery"
)

// Handlers contains HTTP handlers for peer operations
type Handlers struct {
	manager   *Manager
	discovery *discovery.Service
}

// NewHandlers creates a new Handlers instance
func NewHandlers(manager *Manager, discovery *discovery.Service) *Handlers {
	return &Handlers{
		manager:   manager,
		discovery: discovery,
	}
}

// HandleDiscovery returns the list of discovered peers
func (h *Handlers) HandleDiscovery(w http.ResponseWriter, r *http.Request) {
	// Get active peers from discovery service
	discoveredPeers := h.discovery.GetActivePeers()
	peerList := ConvertFromDiscovery(discoveredPeers)

	// Get active peers from manager
	managerPeers := h.manager.GetPeers()

	// Merge peers, preferring discovery service data for duplicates
	seenGUIDs := make(map[string]bool)
	for _, peer := range peerList {
		seenGUIDs[peer.GUID] = true
	}

	// Add manager peers that aren't in discovery
	for _, peer := range managerPeers {
		if !seenGUIDs[peer.GUID] {
			peerList = append(peerList, peer)
		}
	}

	// Log the total number of peers being returned
	log.Printf("[Peers] Returning %d total peers (%d from discovery, %d from manager)",
		len(peerList), len(discoveredPeers), len(managerPeers))

	// Return the combined list as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(peerList); err != nil {
		log.Printf("[Peers] Error encoding peer list: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// HandleGetPeers returns all peers from the database
func (h *Handlers) HandleGetPeers(w http.ResponseWriter, r *http.Request) {
	// Only return peers seen within the active timeout period, using UTC for consistency with DB
	cutoff := time.Now().UTC().Add(-activePeerTimeout)
	peers, err := h.manager.GetPeersLastSeenAfter(cutoff)
	if err != nil {
		http.Error(w, "Failed to get peers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peers)
}
