class PeerViewer {
    constructor() {
        this.overlay = null;
        this.container = null;
        this.peerList = null;
        this.initializeUI();
        this.setupEventListeners();
    }

    initializeUI() {
        // Create overlay
        this.overlay = document.createElement('div');
        this.overlay.className = 'peer-viewer-overlay';

        // Create container
        this.container = document.createElement('div');
        this.container.className = 'peer-viewer-container';

        // Create header
        const header = document.createElement('div');
        header.className = 'peer-viewer-header';

        const title = document.createElement('div');
        title.className = 'peer-viewer-title';
        title.textContent = 'NETWORK PEERS';

        const closeBtn = document.createElement('button');
        closeBtn.className = 'peer-viewer-close';
        closeBtn.textContent = '×';
        closeBtn.onclick = () => this.hide();

        header.appendChild(title);
        header.appendChild(closeBtn);

        // Create peer list container
        this.peerList = document.createElement('div');
        this.peerList.className = 'peer-list';

        // Assemble the components
        this.container.appendChild(header);
        this.container.appendChild(this.peerList);
        this.overlay.appendChild(this.container);
        document.body.appendChild(this.overlay);
    }

    setupEventListeners() {
        // Close on overlay click (but not container click)
        this.overlay.addEventListener('click', (e) => {
            if (e.target === this.overlay) {
                this.hide();
            }
        });

        // Close on Escape key
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape' && this.overlay.classList.contains('show')) {
                this.hide();
            }
        });
    }

    show() {
        this.updatePeerList();
        this.overlay.classList.add('show');
    }

    hide() {
        this.overlay.classList.remove('show');
    }

    formatTimeAgo(timestamp) {
        const now = new Date();
        const date = new Date(timestamp);
        const seconds = Math.floor((now - date) / 1000);

        if (seconds < 60) return 'just now';
        if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
        if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
        return `${Math.floor(seconds / 86400)}d ago`;
    }

    updatePeerList() {
        this.peerList.innerHTML = '';
        const peers = Array.from(window.chat.peers.values())
            .sort((a, b) => new Date(b.LastSeen) - new Date(a.LastSeen));

        peers.forEach(peer => {
            const peerElement = document.createElement('div');
            peerElement.className = 'peer-item';

            const lastSeen = new Date(peer.LastSeen);
            const isActive = (Date.now() - lastSeen) < 10 * 60 * 1000; // 10 minutes

            peerElement.innerHTML = `
                <div class="peer-name">${peer.Name}</div>
                <div class="peer-guid">${peer.GUID}</div>
                <div class="peer-details">
                    <div class="peer-connection">
                        <span class="peer-status ${isActive ? 'status-active' : 'status-inactive'}"></span>
                        ${peer.IPAddress}:${peer.Port}
                    </div>
                    <div class="peer-time">
                        ${this.formatTimeAgo(peer.LastSeen)}
                    </div>
                </div>
            `;

            this.peerList.appendChild(peerElement);
        });
    }
}

// Initialize the viewer after DOM is loaded
document.addEventListener('DOMContentLoaded', () => {
    window.peerViewer = new PeerViewer();

    // Make peer count clickable
    const peerCount = document.getElementById('peer-count');
    if (peerCount) {
        peerCount.style.cursor = 'pointer';
        peerCount.title = 'Click to view peer details';
        peerCount.onclick = () => window.peerViewer.show();
    }
});