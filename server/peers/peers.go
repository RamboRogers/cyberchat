package peers

import (
	"fmt"
	"net"
	"sync"
	"time"

	"cyberchat/server/db"
	"cyberchat/server/discovery"
	"cyberchat/server/logging"
)

const (
	activePeerTimeout = 10 * time.Minute // Match discovery service timeout
)

// Peer represents a discovered peer in the network
type Peer struct {
	GUID      string
	Port      int
	Name      string
	IPAddress string
	LastSeen  time.Time
}

// Manager handles peer operations and state
type Manager struct {
	peers    map[string]Peer // Only contains active peers
	updates  chan Peer
	db       *db.DB
	mu       sync.RWMutex
	onUpdate func(Peer)
}

// New creates a new peer manager
func New(db *db.DB, onUpdate func(Peer)) *Manager {
	m := &Manager{
		peers:    make(map[string]Peer),
		updates:  make(chan Peer, 100),
		db:       db,
		onUpdate: onUpdate,
	}

	// Load only active peers from database
	if db != nil {
		if err := m.loadActivePeers(); err != nil {
			logging.Error("Peers", "Failed to load active peers from database: %v", err)
		}
	}

	return m
}

// loadActivePeers loads only active peers from the database
func (m *Manager) loadActivePeers() error {
	cutoff := time.Now().UTC().Add(-activePeerTimeout)
	dbPeers, err := m.db.GetPeersLastSeenAfter(cutoff)
	if err != nil {
		return fmt.Errorf("failed to get active peers from database: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, p := range dbPeers {
		peer := Peer{
			GUID:      p.GUID,
			Name:      p.Username,
			Port:      p.Port,
			IPAddress: p.IPAddress,
			LastSeen:  p.LastSeen.UTC(), // Ensure LastSeen is in UTC
		}
		m.peers[peer.GUID] = peer
		logging.Info("Peers", "Loaded active peer from database: GUID=%s Name=%s Port=%d IP=%s LastSeen=%s",
			peer.GUID, peer.Name, peer.Port, peer.IPAddress, peer.LastSeen)
	}

	return nil
}

// HandleUpdate processes a peer update
func (m *Manager) HandleUpdate(peer Peer) {
	m.mu.Lock()
	existing, exists := m.peers[peer.GUID]
	peer.LastSeen = time.Now().UTC() // Always update LastSeen time in UTC
	m.peers[peer.GUID] = peer
	m.mu.Unlock()

	// Only log if peer is new or has changed
	if !exists || existing != peer {
		logging.Info("Peers", "Updated peer: GUID=%s Name=%s Port=%d IP=%s",
			peer.GUID, peer.Name, peer.Port, peer.IPAddress)
	}

	// Save peer to database
	if m.db != nil {
		ip := net.ParseIP(peer.IPAddress)
		if ip == nil {
			logging.Error("Peers", "Invalid IP address for peer %s: %s", peer.GUID, peer.IPAddress)
			return
		}

		err := m.db.SavePeer(peer.GUID, peer.IPAddress, peer.Port, nil, peer.Name)
		if err != nil {
			logging.Error("Peers", "Error saving peer to database: %v", err)
		} else {
			logging.Debug("Peers", "Saved peer to database: GUID=%s", peer.GUID)
		}
	}

	// Notify callback if registered
	if m.onUpdate != nil {
		m.onUpdate(peer)
	}
}

// GetPeers returns a list of all active peers
func (m *Manager) GetPeers() []Peer {
	m.mu.RLock()
	defer m.mu.RUnlock()

	peers := make([]Peer, 0, len(m.peers))
	for _, peer := range m.peers {
		peers = append(peers, peer)
	}

	logging.Info("Peers", "GetPeers returning %d active peers", len(peers))
	for _, peer := range peers {
		logging.Debug("Peers", "- Peer: GUID=%s Name=%s Port=%d IP=%s LastSeen=%s",
			peer.GUID, peer.Name, peer.Port, peer.IPAddress, peer.LastSeen)
	}

	return peers
}

// ConvertFromDiscovery converts discovery peers to server peers
func ConvertFromDiscovery(dpeers []discovery.Peer) []Peer {
	peers := make([]Peer, len(dpeers))
	for i, p := range dpeers {
		peers[i] = Peer{
			GUID:      p.GUID,
			Name:      p.Name,
			Port:      p.Port,
			IPAddress: p.IP.String(),
			LastSeen:  p.LastSeen,
		}
	}
	return peers
}

// GetPeer returns a specific active peer by GUID
func (m *Manager) GetPeer(guid string) (Peer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	peer, exists := m.peers[guid]
	return peer, exists
}

// GetHistoricalPeer returns a peer from the database, regardless of active status
func (m *Manager) GetHistoricalPeer(guid string) (*Peer, error) {
	if m.db == nil {
		return nil, fmt.Errorf("no database connection")
	}

	dbPeer, err := m.db.GetPeer(guid)
	if err != nil {
		return nil, err
	}
	if dbPeer == nil {
		return nil, nil
	}

	return &Peer{
		GUID:      dbPeer.GUID,
		Name:      dbPeer.Username,
		Port:      dbPeer.Port,
		IPAddress: dbPeer.IPAddress,
		LastSeen:  dbPeer.LastSeen,
	}, nil
}

// Updates returns the channel for peer updates
func (m *Manager) Updates() chan Peer {
	return m.updates
}

// RemoveInactivePeer removes an inactive peer from memory only
func (m *Manager) RemoveInactivePeer(guid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.peers, guid)
}

// GetPeersLastSeenAfter returns peers that were last seen after the given cutoff time
func (m *Manager) GetPeersLastSeenAfter(cutoff time.Time) ([]Peer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoffUTC := cutoff.UTC()
	var activePeers []Peer
	for _, peer := range m.peers {
		if peer.LastSeen.UTC().After(cutoffUTC) {
			activePeers = append(activePeers, peer)
		}
	}
	return activePeers, nil
}
