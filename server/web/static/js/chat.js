class BasicChat {
    constructor() {
        window.chat = this;
        this.peerNames = {};
        this.peers = new Map(); // Store full peer info
        this.ws = null;
        this.apiKey = null;
        this.peerList = document.getElementById('peer-list');
        this.messages = document.getElementById('messages');
        this.messageInput = document.getElementById('message-input');
        this.fileInput = document.getElementById('file-input');
        this.fileButton = document.getElementById('file-button');
        this.sendButton = document.getElementById('send-button');
        this.scopeFilter = document.getElementById('scope-filter');
        this.soundToggle = document.getElementById('sound-toggle');
        this.clearMessages = document.getElementById('clear-messages');
        this.sharedFilesBtn = document.getElementById('shared-files-btn');
        this.settingsButton = document.getElementById('settings-button');
        this.settingsDialog = document.getElementById('settings-dialog');
        this.displayNameInput = document.getElementById('display-name');
        this.scopeFilterActive = false;
        this.soundEnabled = true;
        this.suppressSounds = true; // Add sound suppression flag
        this.myGuid = null;
        this.myName = null;
        this.currentMessages = [];
        this.fileTransfers = new Map(); // Track ongoing file transfers
        this.locallyDisplayedFiles = new Set(); // Track files we've shown locally
        this.reconnectAttempts = 0;
        this.maxReconnectAttempts = 3;
        this.offlineAlert = document.getElementById('offline-alert');
        this.peerCount = document.getElementById('peer-count');
        this.titleInterval = null;
        this.originalTitle = document.title;

        // Initialize sound
        this.messageSound = new Audio('/media/hailbeep_clean.mp3');
        this.messageSound.volume = 0.1 ; // 20% volume

        // Set initial placeholder
        this.messageInput.placeholder = 'Message to everyone...';

        // Get API key and initialize
        this.getApiKey()
            .then(() => this.getMyInfo())
            .then(() => this.loadPeers())
            .then(() => this.connectWebSocket())
            .then(() => this.loadMessages())
            .then(() => {
                this.setupListeners();
                this.fileBrowser = new FileBrowser(this);
                this.fileButton.onclick = () => {
                    this.fileBrowser.show();
                };
            });
    }

    setupListeners() {
        // Sound toggle
        this.soundToggle.onclick = () => {
            this.soundEnabled = !this.soundEnabled;
            this.soundToggle.textContent = this.soundEnabled ? '🔊' : '🔈';
            this.soundToggle.classList.toggle('active');
            // Play a test sound when enabling
            if (this.soundEnabled) {
                this.playMessageSound();
            }
        };

        // Clear messages
        this.clearMessages.onclick = async () => {
            if (confirm('Are you sure you want to clear all messages?')) {
                try {
                    const response = await fetch('/api/v1/client/message/truncate', {
                        method: 'POST',
                        headers: {
                            'X-Client-API-Key': this.apiKey
                        }
                    });
                    if (response.ok) {
                        this.currentMessages = [];
                        this.messages.innerHTML = '';
                        this.log('Message history cleared');
                    } else {
                        this.log('Failed to clear message history');
                    }
                } catch (error) {
                    console.error('Failed to clear messages:', error);
                    this.log('Failed to clear message history');
                }
            }
        };

        // Send on button click
        this.sendButton.onclick = () => this.sendMessage();

        // Send on Enter key
        this.messageInput.onkeypress = (e) => {
            if (e.key === 'Enter') {
                this.sendMessage();
            }
        };

        // Handle scope changes
        this.peerList.onchange = () => {
            this.filterMessages();
            this.updateInputPlaceholder();
        };

        // Smart focus handling
        document.body.onclick = (e) => {
            // Don't auto-focus if clicking on select, buttons, links, or settings dialog
            if (e.target.tagName === 'SELECT' ||
                e.target.tagName === 'BUTTON' ||
                e.target.tagName === 'A' ||
                e.target.tagName === 'OPTION' ||
                e.target.closest('#settings-dialog')) {
                return;
            }
            this.messageInput.focus();
        };

        // Add file upload handling
        this.fileInput.onchange = async (e) => {
            const file = e.target.files[0];
            if (!file) return;
            await this.handleFileUpload(file);
            this.fileInput.value = '';
        };

        // File button click handler
        this.fileButton.onclick = () => {
            this.fileBrowser.show();
        };

        // Scope filter toggle
        this.scopeFilter.onclick = () => {
            this.scopeFilterActive = !this.scopeFilterActive;
            this.scopeFilter.classList.toggle('active');
            this.filterMessages();
        };

        // Add settings button handler
        this.settingsButton.onclick = () => this.showNameDialog();

        // Add Enter key handler for name input
        this.displayNameInput.onkeypress = (e) => {
            if (e.key === 'Enter') {
                this.saveDisplayName();
            }
        };
    }

    updateInputPlaceholder() {
        const selectedGuid = this.peerList.value;
        if (!selectedGuid) {
            this.messageInput.placeholder = 'Message to everyone...';
        } else {
            const peerName = this.peerNames[selectedGuid] || selectedGuid;
            this.messageInput.placeholder = `Message to ${peerName}...`;
        }
    }

    filterMessages() {
        const selectedGuid = this.peerList.value;
        const messageList = document.getElementById('messages');
        messageList.innerHTML = '';

        this.currentMessages.forEach(msg => {
            if (this.shouldShowMessage(msg)) {
                this.showMessage(msg, false);
            }
        });

        messageList.scrollTop = messageList.scrollHeight;
    }

    shouldShowMessage(msg) {
        const selectedGuid = this.peerList.value;

        // If scope filter is not active, show all messages in current scope
        if (!this.scopeFilterActive) {
            return !selectedGuid || // Show all in broadcast mode
                msg.sender_guid === selectedGuid || // From selected peer
                msg.receiver_guid === selectedGuid || // To selected peer
                msg.sender_guid === this.myGuid || // From me
                msg.receiver_guid === this.myGuid; // To me
        }

        // If scope filter is active, only show messages in exact scope
        if (!selectedGuid) {
            // In broadcast mode, only show broadcast messages
            return msg.scope === 'broadcast';
        } else {
            // In private mode, only show messages between selected peer and me
            return (msg.sender_guid === selectedGuid && msg.receiver_guid === this.myGuid) ||
                   (msg.sender_guid === this.myGuid && msg.receiver_guid === selectedGuid);
        }
    }

    async getApiKey() {
        try {
            const response = await fetch('/api/v1/client/auth');
            const data = await response.json();
            this.apiKey = data.api_key;
        } catch (error) {
            console.error('Failed to get API key:', error);
            this.log('Failed to initialize client authentication');
        }
    }

    async getMyInfo() {
        try {
            const response = await fetch('/api/v1/whoami');
            const data = await response.json();
            this.myGuid = data.guid;
            this.myName = data.name;
            // Set Title
            document.title = `${this.myName} - CyberChat`;

            // Show name dialog if no name is set
            if (!this.myName || this.myName === this.myGuid) {
                this.showNameDialog();
            }
        } catch (error) {
            console.error('Failed to get own info:', error);
            this.log('Failed to initialize user information');
        }
    }

    async loadPeers() {
        try {
            // Get active peers from discovery service
            const response = await fetch('/api/v1/discovery');
            const discoveryPeers = await response.json();

            // Update peers map with active peers
            discoveryPeers.forEach(peer => {
                this.peers.set(peer.GUID, peer);
            });

            // Update peer names and dropdown
            this.peerNames = {};
            this.peerList.innerHTML = '<option value="">Broadcast</option>';

            Array.from(this.peers.values())
                .sort((a, b) => a.Name.localeCompare(b.Name)) // Sort by name
                .forEach(peer => {
                    this.peerNames[peer.GUID] = peer.Name;
                    const option = document.createElement('option');
                    option.value = peer.GUID;
                    option.textContent = `${peer.Name} (${peer.GUID})`;
                    this.peerList.appendChild(option);
                });

            // Update peer count
            this.peerCount.textContent = `${this.peers.size} peers`;

            // Log peer load
            if (this.peers.size > 0) {
                this.log(`Loaded ${this.peers.size} active peers`);
            }
        } catch (e) {
            console.error('Failed to load peers:', e);
            this.log('Failed to load peer list');
            throw e; // Re-throw to handle in caller
        }
    }

    async loadMessages() {
        try {
            const since = new Date();
            since.setHours(since.getHours() - 24); // Last 24 hours
            const url = `/api/v1/client/message?since=${since.toISOString()}&limit=100`;
            const response = await fetch(url, {
                headers: {
                    'X-Client-API-Key': this.apiKey
                }
            });
            const text = await response.text();
            try {
                const messages = JSON.parse(text);
                // Sort messages by timestamp before processing
                messages.sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));

                // Process each message with sound suppression
                this.suppressSounds = true;
                for (const msg of messages) {
                    if (msg.type === 'file') {
                        // Handle file messages directly
                        this.showMessage(msg);
                    } else if (typeof msg.content === 'string') {
                        try {
                            // Try to parse as JSON in case it's a structured message
                            const parsedContent = JSON.parse(msg.content);
                            msg.content = parsedContent;
                        } catch (e) {
                            // Not JSON, treat as regular text message
                        }
                        this.showMessage(msg);
                    }
                }
                // Enable sounds after initial load
                setTimeout(() => {
                    this.suppressSounds = false;
                }, 2000); // 2 second delay before enabling sounds
            } catch (e) {
                console.error('Failed to parse messages:', e);
                this.log('Failed to parse messages: ' + e.message);
            }
        } catch (error) {
            console.error('Failed to load messages:', error);
            this.log('Failed to load messages: ' + error.message);
        }
    }

    connectWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/ws`;

        this.ws = new WebSocket(wsUrl);

        this.ws.onmessage = (e) => {
            try {
                const text = e.data;
                console.log('Raw WebSocket message:', text);
                const data = JSON.parse(text);
                this.handleIncomingMessage(data);
            } catch (e) {
                console.error('Failed to parse WebSocket message:', e);
                console.log('Raw message:', e.data);
            }
        };

        this.ws.onopen = async () => {
            this.log('WebSocket connected');
            this.reconnectAttempts = 0;
            this.offlineAlert.style.display = 'none';

            // Reload peers on reconnection
            try {
                await this.loadPeers();
                this.log('Successfully reloaded peer list');
            } catch (error) {
                console.error('Failed to reload peers:', error);
                this.log('Failed to reload peer list');
            }
        };

        this.ws.onclose = () => {
            this.log('WebSocket disconnected');
            this.showOfflineAlert();
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
            this.log('WebSocket error occurred');
            this.showOfflineAlert();
        };
    }

    handleIncomingMessage(data) {
        try {
            if (data.type === 'file_transfer') {
                console.log('Received file transfer message:', data);
                if (window.fileTransferManager) {
                    window.fileTransferManager.handleTransfer(data);
                } else {
                    console.error('FileTransferManager not initialized!');
                }
                return;
            }

            if (data.type === 'message') {
                const msg = data.content;
                // Track message IDs to prevent duplicates
                if (this.processedMessages && this.processedMessages.has(msg.id)) {
                    return; // Skip already processed messages
                }

                // Initialize message tracking if needed
                if (!this.processedMessages) {
                    this.processedMessages = new Set();
                }

                // Add message to processed set
                this.processedMessages.add(msg.id);

                // Cleanup old messages (keep last 1000)
                if (this.processedMessages.size > 1000) {
                    const toRemove = Array.from(this.processedMessages).slice(0, 100);
                    toRemove.forEach(id => this.processedMessages.delete(id));
                }

                if (msg.type === 'file') {
                    // Handle file messages directly
                    this.showMessage(msg);
                } else if (typeof msg.content === 'string') {
                    try {
                        // Try to parse as JSON in case it's a structured message
                        const parsedContent = JSON.parse(msg.content);
                        msg.content = parsedContent;
                    } catch (e) {
                        // Not JSON, treat as regular text message
                    }
                    this.showMessage(msg);
                }
            } else if (data.type === 'delivery_status' || data.type === 'delivery_progress' || data.type === 'delivery_final') {
                // Handle delivery status updates
                this.showDeliveryStatus(data);
            } else if (data.type === 'peer') {
                // Update peer in our map
                const peer = data.content;
                const existingPeer = this.peers.get(peer.GUID);

                // Only update if the peer data has actually changed
                if (!existingPeer ||
                    existingPeer.Name !== peer.Name ||
                    existingPeer.Port !== peer.Port ||
                    existingPeer.IPAddress !== peer.IPAddress) {

                    this.peers.set(peer.GUID, peer);
                    this.peerNames[peer.GUID] = peer.Name;

                    // Refresh the peer list dropdown
                    this.peerList.innerHTML = '<option value="">Broadcast</option>';
                    Array.from(this.peers.values())
                        .sort((a, b) => a.Name.localeCompare(b.Name)) // Sort by name
                        .forEach(p => {
                            const option = document.createElement('option');
                            option.value = p.GUID;
                            option.textContent = `${p.Name} (${p.GUID})`;
                            this.peerList.appendChild(option);
                        });

                    // Update peer count
                    this.peerCount.textContent = `${this.peers.size} peers`;

                    // Log peer update only if there were actual changes
                    this.log(`Peer ${peer.Name} (${peer.GUID}) updated`);
                }
            } else if (data.type === 'peer_offline') {
                // Remove offline peer
                const peerGuid = data.content.guid;
                const peerName = this.peerNames[peerGuid] || peerGuid;
                this.peers.delete(peerGuid);
                delete this.peerNames[peerGuid];

                // Refresh the peer list dropdown
                this.peerList.innerHTML = '<option value="">Broadcast</option>';
                Array.from(this.peers.values())
                    .sort((a, b) => a.Name.localeCompare(b.Name))
                    .forEach(p => {
                        const option = document.createElement('option');
                        option.value = p.GUID;
                        option.textContent = `${p.Name} (${p.GUID})`;
                        this.peerList.appendChild(option);
                    });

                // Update peer count
                this.peerCount.textContent = `${this.peers.size} peers`;

                // Don't log here since we're getting the system message from the server
            }
        } catch (error) {
            console.error('Failed to handle message:', error);
            this.log('Failed to handle message: ' + error.message);
        }
    }

    async sendMessage() {
        const content = this.messageInput.value.trim();
        if (!content) return;

        const receiverGuid = this.peerList.value;
        const message = {
            type: 'text',
            content: content,
            receiver_guid: receiverGuid,
            scope: receiverGuid ? 'private' : 'broadcast'
        };

        try {
            // Clear input before sending to prevent double-sends
            this.messageInput.value = '';

            // Send via secure client endpoint
            const response = await fetch('/api/v1/client/message', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Client-API-Key': this.apiKey
                },
                body: JSON.stringify(message)
            });

            const report = await response.json();
            this.showDeliveryStatus(report);
        } catch (error) {
            console.error('Failed to send message:', error);
            this.log('Failed to send message: ' + error.message);
        }
    }

    showDeliveryStatus(report) {
        // Get or create status container
        let status = document.querySelector('.delivery-status');
        if (!status) {
            status = document.createElement('div');
            status.className = 'delivery-status';
            document.body.appendChild(status);
        }

        // Create status message based on report type
        let statusText = '';
        if (report.type === 'delivery_status') {
            if (report.content?.details === "No peers available for broadcast") {
                statusText = 'No peers available';
            } else {
                statusText = 'Starting delivery...';
            }
        }
        else if (report.type === 'delivery_progress') {
            const delivered = report.content?.progress?.succeeded || 0;
            const total = report.content?.progress?.total || 0;
            statusText = `${delivered}/${total} delivered`;
        }
        else if (report.type === 'delivery_final') {
            const successful = report.content?.final?.succeeded || 0;
            const total = report.content?.final?.total || 0;
            if (report.content?.details) {
                statusText = report.content.details;
            } else if (total === 0) {
                statusText = 'No peers available';
            } else {
                statusText = `${successful}/${total} delivered`;
                if (successful < total) {
                    status.classList.add('partial');
                }
            }
        }

        // Update the status text
        const statusLine = document.createElement('div');
        statusLine.textContent = statusText;

        // Clear previous content if this is a new delivery
        if (report.type === 'delivery_status') {
            status.innerHTML = '';
        }

        status.appendChild(statusLine);

        // Clear any existing fade timeouts
        if (this.statusFadeTimeout) {
            clearTimeout(this.statusFadeTimeout);
            clearTimeout(this.statusRemoveTimeout);
            status.classList.remove('fade-out');
        }

        // Handle cleanup based on report type
        if (report.type === 'delivery_final') {
            // Keep final status visible for 2 seconds
            this.statusFadeTimeout = setTimeout(() => {
                status.classList.add('fade-out');
                this.statusRemoveTimeout = setTimeout(() => {
                    if (status && status.parentNode) {
                        status.remove();
                    }
                    this.statusFadeTimeout = null;
                    this.statusRemoveTimeout = null;
                }, 500);
            }, 1000);
        } else if (!this.deliveryCleanupTimeout) {
            // Safety cleanup after 10 seconds if we never get a final message
            this.deliveryCleanupTimeout = setTimeout(() => {
                if (status && status.parentNode) {
                    status.classList.add('fade-out');
                    setTimeout(() => {
                        if (status && status.parentNode) {
                            status.remove();
                        }
                    }, 500);
                }
                this.deliveryCleanupTimeout = null;
            }, 1500);
        }
    }

    async handleFileUpload(file) {
        // Generate a unique file ID
        const fileID = uuid.v4();

        try {
            // Send the file path instead of uploading the file
            const formData = new FormData();
            formData.append('filepath', file.path);
            formData.append('file_id', fileID);
            formData.append('receiver_guid', this.peerList.value || ''); // Make receiver optional

            const uploadResponse = await fetch('/api/v1/client/file', {
                method: 'POST',
                headers: {
                    'X-Client-API-Key': this.apiKey
                },
                body: formData
            });

            if (!uploadResponse.ok) {
                throw new Error('File registration failed');
            }

            // Then send the file message
            const message = {
                type: 'text',
                content: JSON.stringify({
                    type: 'file',
                    file_id: fileID,
                    name: file.name,
                    mime: file.type,
                    size: file.size
                }),
                receiver_guid: this.peerList.value || '',
                scope: this.peerList.value ? 'private' : 'broadcast'
            };

            await fetch('/api/v1/client/message', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Client-API-Key': this.apiKey
                },
                body: JSON.stringify(message)
            });

        } catch (error) {
            console.error('Failed to send file:', error);
            this.log('Failed to send file: ' + error.message);
        }
    }

    async checkCertAcceptance(urlBase) {
        try {
            const response = await fetch(`https://${urlBase}/api/v1/whoami`, {
                method: 'HEAD'
            });
            return true;
        } catch (e) {
            return false;
        }
    }

    async showMessage(msg, store = true) {
        if (store) {
            this.currentMessages.push(msg);
        }

        const messageList = document.getElementById('messages');
        const messageDiv = document.createElement('div');

        const isOwnMessage = msg.sender_guid === this.myGuid;
        const isSystemMessage = msg.sender_guid === 'system';

        // Apply appropriate message class
        if (isSystemMessage) {
            messageDiv.className = 'message system-message';
        } else {
            messageDiv.className = `message ${isOwnMessage ? 'own-message' : 'other-message'}`;
            // Only add new-message class and play sound for actual new messages, not loaded history
            if (store && !isOwnMessage) {
                messageDiv.classList.add('new-message');
                this.playMessageSound();
                // Start title notification if tab not focused
                if (!document.hasFocus()) {
                    this.startTitleNotification();
                }
                // Remove highlight after message is visible for 2 seconds
                setTimeout(() => {
                    messageDiv.classList.add('fade');
                }, 2000);
            }
        }

        // Format timestamp
        const timestamp = new Date(msg.timestamp).toLocaleTimeString();

        // Get sender name (or use GUID if name not available)
        let senderName = this.peerNames[msg.sender_guid] || msg.sender_guid;
        if (isOwnMessage) {
            senderName = this.myName || senderName;
        }
        if (isSystemMessage) {
            senderName = 'System';
        }

        // Enhanced private message handling
        let scopeIndicator = '';
        if (msg.scope === 'private' && !isSystemMessage) {
            if (isOwnMessage) {
                const targetName = this.peerNames[msg.receiver_guid] || msg.receiver_guid;
                scopeIndicator = targetName ? ` (to: ${targetName})` : '';
            } else {
                const senderName = this.peerNames[msg.sender_guid] || msg.sender_guid;
                scopeIndicator = senderName ? ` (from: ${senderName})` : '';
            }
        }

        // Build message HTML with enhanced scope indicator
        let contentHtml = '';
        if (msg.type === 'text') {
            let content = msg.content;
            if (typeof content === 'object') {
                if (content.type === 'file') {
                    // Determine the correct URL base
                    let urlBase;
                    if (msg.sender_guid === this.myGuid) {
                        urlBase = `${window.location.hostname}:${window.location.port}`;
                    } else {
                        const peer = this.peers.get(msg.sender_guid);
                        urlBase = peer ? `${peer.IPAddress}:${peer.Port}` : undefined;
                    }

                    if (!urlBase) {
                        contentHtml = `<div class="error">Error: Could not determine file location</div>`;
                    } else {
                        contentHtml = `<div class="content-area"></div>`;
                        // Append the message div first
                        messageDiv.innerHTML = `
                            <span class="timestamp">[${timestamp}]</span>
                            <span class="sender" title="GUID: ${msg.sender_guid}">${escapeHtml(senderName.toString())}${scopeIndicator}:</span>
                            ${contentHtml}
                        `;
                        messageList.appendChild(messageDiv);
                        // Then try to load the content
                        await this.tryLoadContent(messageDiv, urlBase, content);
                        return; // Skip the normal message append
                    }
                } else {
                    content = JSON.stringify(content);
                }
            }
            contentHtml = `<span class="content">${escapeHtml(content.toString())}</span>`;
        } else {
            contentHtml = `<span class="content">${escapeHtml(msg.content.toString())}</span>`;
        }

        messageDiv.innerHTML = `
            <span class="timestamp">[${timestamp}]</span>
            <span class="sender" title="GUID: ${msg.sender_guid}">${escapeHtml(senderName.toString())}${scopeIndicator}:</span>
            ${contentHtml}
        `;

        messageList.appendChild(messageDiv);
        messageList.scrollTop = messageList.scrollHeight;
    }

    async tryLoadContent(messageDiv, urlBase, parsedContent) {
        try {
            // First check if the file exists by making a HEAD request
            const fileResponse = await fetch(`https://${urlBase}/api/v1/file/${parsedContent.file_id}`, {
                method: 'HEAD'
            });

            if (fileResponse.status === 404) {
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="file-container">
                        <div class="file-error">
                            <span class="file-icon">⚠️</span>
                            <span>File "${parsedContent.name}" is no longer available</span>
                        </div>
                    </div>`;
                this.messages.scrollTop = this.messages.scrollHeight;
                return;
            }

            const certAccepted = await this.checkCertAcceptance(urlBase);
            if (!certAccepted) {
                const escapedContent = JSON.stringify(parsedContent).replace(/"/g, '&quot;');
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="file-error">
                        <span class="file-icon">🔒</span>
                        <span>Certificate not accepted for ${urlBase}</span>
                        <div class="cert-actions">
                            <a href="https://${urlBase}/api/v1/whoami" target="_blank" class="cert-button accept" title="Open certificate acceptance page">Accept Cert</a>
                            <button onclick='window.chat.retryContent(this, "${urlBase}", ${escapedContent})' class="cert-button" title="Try downloading the file again">Retry</button>
                        </div>
                    </div>`;
                this.messages.scrollTop = this.messages.scrollHeight;
                return;
            }

            // If cert is accepted and file exists, show the appropriate content
            if (parsedContent.mime.startsWith('image/')) {
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="media-container">
                        <img src="https://${urlBase}/api/v1/file/${parsedContent.file_id}"
                             alt="${parsedContent.name}"
                             style="max-width: 512px; max-height: 256px; object-fit: contain;"
                             onload="this.closest('#messages').scrollTop = this.closest('#messages').scrollHeight" />
                        <a href="https://${urlBase}/api/v1/file/${parsedContent.file_id}"
                           download="${parsedContent.name}"
                           class="file-download">
                            🖼️ ${parsedContent.name} (${this.formatFileSize(parsedContent.size)})
                        </a>
                    </div>`;
            } else if (parsedContent.mime.startsWith('video/')) {
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="media-container">
                        <video controls style="max-width: 512px; max-height: 512px;"
                               onloadedmetadata="this.closest('#messages').scrollTop = this.closest('#messages').scrollHeight">
                            <source src="https://${urlBase}/api/v1/file/${parsedContent.file_id}"
                                    type="${parsedContent.mime}">
                            Your browser does not support video playback.
                        </video>
                        <a href="https://${urlBase}/api/v1/file/${parsedContent.file_id}"
                           download="${parsedContent.name}"
                           class="file-download">
                            🎬 ${parsedContent.name} (${this.formatFileSize(parsedContent.size)})
                        </a>
                    </div>`;
            } else {
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="file-container">
                        <a href="https://${urlBase}/api/v1/file/${parsedContent.file_id}"
                           download="${parsedContent.name}"
                           class="file-download">
                            ${this.getFileEmoji(parsedContent.mime)} ${parsedContent.name} (${this.formatFileSize(parsedContent.size)})
                        </a>
                    </div>`;
            }
            // Scroll for non-media files immediately
            if (!parsedContent.mime.startsWith('image/') && !parsedContent.mime.startsWith('video/')) {
                this.messages.scrollTop = this.messages.scrollHeight;
            }
        } catch (error) {
            // Check if it's a certificate error (Failed to fetch typically means cert issue)
            if (error.message.includes('Failed to fetch')) {
                const escapedContent = JSON.stringify(parsedContent).replace(/"/g, '&quot;');
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="file-error">
                        <span class="file-icon">🔒</span>
                        <span>Certificate not accepted for ${urlBase}</span>
                        <div class="cert-actions">
                            <a href="https://${urlBase}/api/v1/whoami" target="_blank" class="cert-button accept" title="Open certificate acceptance page">Accept Cert</a>
                            <button onclick='window.chat.retryContent(this, "${urlBase}", ${escapedContent})' class="cert-button" title="Try downloading the file again">Retry</button>
                        </div>
                    </div>`;
            } else {
                messageDiv.querySelector('.content-area').innerHTML = `
                    <div class="file-container">
                        <div class="file-error">
                            <span class="file-icon">⚠️</span>
                            <span>Error accessing file: ${error.message}</span>
                        </div>
                    </div>`;
            }
            this.messages.scrollTop = this.messages.scrollHeight;
        }
    }

    async retryContent(button, urlBase, parsedContent) {
        const messageDiv = button.closest('.message');
        await this.tryLoadContent(messageDiv, urlBase, parsedContent);
    }

    formatFileSize(bytes) {
        const units = ['B', 'KB', 'MB', 'GB'];
        let size = bytes;
        let unitIndex = 0;
        while (size >= 1024 && unitIndex < units.length - 1) {
            size /= 1024;
            unitIndex++;
        }
        return `${size.toFixed(1)} ${units[unitIndex]}`;
    }

    playMessageSound() {
        if (this.soundEnabled && !this.suppressSounds) {
            // Clone and play to allow overlapping sounds
            this.messageSound.cloneNode().play().catch(e => {
                console.log('Sound play failed:', e);
            });
        }
    }

    log(msg) {
        this.showMessage({
            type: 'text',
            sender_guid: 'system',
            content: msg,
            timestamp: new Date().toISOString()
        });
    }

    getFileEmoji(mime) {
        const emojis = {
            'image/jpeg': '🖼️',
            'image/png': '🖼️',
            'image/gif': '🖼️',
            'video/mp4': '🎬',
            'video/webm': '🎬',
            'audio/mpeg': '🎵'
        };
        return emojis[mime] || '📎';
    }

    showOfflineAlert() {
        this.offlineAlert.style.display = 'flex';
        this.startMatrixRain();
    }

    startMatrixRain() {
        // Remove any existing rain
        const existingRain = document.querySelector('.matrix-rain');
        if (existingRain) {
            existingRain.remove();
        }

        const container = document.createElement('div');
        container.className = 'matrix-rain';
        this.offlineAlert.appendChild(container);

        // Create 100 columns of rain (increased from 50)
        for (let i = 0; i < 100; i++) {
            const column = document.createElement('div');
            column.className = 'rain-column';

            // Random position
            column.style.left = `${Math.random() * 100}%`;

            // Random animation duration between 4 and 12 seconds (slower)
            column.style.animationDuration = `${4 + Math.random() * 8}s`;

            // Random delay
            column.style.animationDelay = `${Math.random() * 5}s`;

            // Create longer random binary string
            let text = '';
            for (let j = 0; j < 35; j++) { // Increased from 20 to 35 characters
                text += Math.random() > 0.5 ? '1' : '0';
                text += '\n';
            }
            column.textContent = text;

            container.appendChild(column);
        }
    }

    // Add these new methods for name dialog handling
    showNameDialog() {
        this.displayNameInput.value = this.myName || '';
        this.settingsDialog.classList.add('show');
        this.displayNameInput.focus();
    }

    closeNameDialog() {
        this.settingsDialog.classList.remove('show');
    }

    async saveDisplayName() {
        const newName = this.displayNameInput.value.trim();
        if (!newName) return;

        try {
            const response = await fetch('/api/v1/client/name', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Client-API-Key': this.apiKey
                },
                body: JSON.stringify({ name: newName })
            });

            if (response.ok) {
                this.myName = newName;
                document.title = `${this.myName} - CyberChat`;
                this.log(`Name updated to: ${this.myName}`);
                this.closeNameDialog();
            } else {
                this.log('Failed to update name');
            }
        } catch (error) {
            console.error('Failed to save name:', error);
            this.log('Failed to update name: ' + error.message);
        }
    }

    updatePeerList() {
        const select = document.getElementById('peer-select');
        select.innerHTML = '';

        // Add broadcast option
        const broadcastOption = document.createElement('option');
        broadcastOption.value = 'broadcast';
        broadcastOption.textContent = 'BROADCAST';
        select.appendChild(broadcastOption);

        // Sort peers by name
        const sortedPeers = Array.from(this.peers.values())
            .sort((a, b) => a.name.localeCompare(b.name));

        // Add peer options with status indicators
        for (const peer of sortedPeers) {
            const option = document.createElement('option');
            option.value = peer.guid;

            // Create a div to hold the status indicator and name
            const optionContent = document.createElement('div');
            optionContent.className = 'peer-option';

            // Add status indicator
            const statusIndicator = document.createElement('span');
            statusIndicator.className = `status-indicator ${peer.lastSeen && (Date.now() - new Date(peer.lastSeen).getTime() < 10 * 60 * 1000) ? 'online' : 'offline'}`;
            optionContent.appendChild(statusIndicator);

            // Add peer name
            const peerName = document.createElement('span');
            peerName.textContent = peer.name;
            optionContent.appendChild(peerName);

            option.appendChild(optionContent);
            select.appendChild(option);
        }

        // Update peer count
        this.peerCount.textContent = `${this.peers.size} peers`;
    }

    // Add this new method after showDeliveryStatus
    showSystemNotification(message, details = '') {
        // Remove any existing notification
        const existing = document.querySelector('.system-notification');
        if (existing) {
            existing.remove();
        }

        // Create new notification element
        const notification = document.createElement('div');
        notification.className = 'system-notification';

        notification.innerHTML = `
            <div class="notification-header">${message}</div>
            ${details ? `<div class="notification-details">${details}</div>` : ''}
        `;

        // Append to messages container
        const messagesContainer = document.getElementById('messages');
        messagesContainer.appendChild(notification);

        // Fade out after 5 seconds
        setTimeout(() => {
            notification.classList.add('fade-out');
            setTimeout(() => notification.remove(), 5000);
        }, 5000);
    }

    startTitleNotification() {
        if (this.titleInterval) return; // Already notifying

        let showBell = true;
        this.titleInterval = setInterval(() => {
            document.title = showBell ? `🔔 ${this.originalTitle}` : this.originalTitle;
            showBell = !showBell;
        }, 1000);

        // Add focus event listener to stop notification
        window.addEventListener('focus', this.stopTitleNotification.bind(this));
    }

    stopTitleNotification() {
        if (this.titleInterval) {
            clearInterval(this.titleInterval);
            this.titleInterval = null;
            document.title = this.originalTitle;
        }
    }
}