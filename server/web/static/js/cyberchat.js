        // Add at the start of the script section, before the BasicChat class
        window.handleCertError = function(peerUrl) {
            // Remove any existing cert warnings for this peer
            const existingWarning = document.querySelector(`.cert-warning[data-peer="${peerUrl}"]`);
            if (existingWarning) {
                existingWarning.remove();
                return;
            }

            const warning = document.createElement('div');
            warning.className = 'cert-warning';
            warning.setAttribute('data-peer', peerUrl);

            warning.innerHTML = `
                <div class="cert-warning-content">
                    <div class="cert-header">
                        <span class="cert-icon">🔒</span>
                        <h3>Security Certificate Required</h3>
                    </div>
                    <div class="cert-steps">
                        <p>Accept certificate from <strong class="cyber-text">${peerUrl}</strong> to view 📁 content:</p>
                        <div class="steps-grid">
                            <div class="step"><span class="step-num">1</span> Click "Accept"</div>
                            <div class="step"><span class="step-num">2</span> Click "Advanced"</div>
                            <div class="step"><span class="step-num">3</span> Click "Proceed"</div>
                            <div class="step"><span class="step-num">4</span> Click "Retry"</div>
                        </div>
                    </div>
                    <div class="cert-actions">
                        <a href="https://${peerUrl}/api/v1/whoami" target="_blank" class="cyber-button accept">Accept Certificate</a>
                        <button onclick="this.closest('.basic-chat').chat.retryContent(this, '${peerUrl}', null)" class="cyber-button retry">Retry</button>
                    </div>
                </div>
            `;

            document.body.appendChild(warning);
        };

        class FileBrowser {
            constructor(chat) {
                this.chat = chat;
                this.container = document.getElementById('file-browser');
                if (!this.container) {
                    throw new Error('File browser container not found');
                }
                this.fileList = this.container.querySelector('.file-list');
                this.currentPath = this.container.querySelector('.current-path');
                this.typeFilter = this.container.querySelector('.type-filter');
                this.history = [];

                // Bind event listeners
                this.typeFilter.addEventListener('change', () => this.loadDirectory(this.currentPath.textContent));
            }

            async show() {
                this.container.style.display = 'flex';
                try {
                    const homePath = await this.getHomePath();
                    console.log('Loading directory:', homePath);
                    await this.loadDirectory(homePath);
                } catch (error) {
                    console.error('Failed to show file browser:', error);
                    this.showError('Failed to open file browser: ' + error.message);
                }
            }

            hide() {
                this.container.style.display = 'none';
            }

            async getHomePath() {
                try {
                    const response = await fetch('/api/v1/client/filesystem?path=~', {
                        headers: {
                            'X-Client-API-Key': window.chat.apiKey
                        }
                    });
                    if (!response.ok) {
                        throw new Error(`HTTP error! status: ${response.status}`);
                    }
                    const data = await response.json();
                    return data.current_path;
                } catch (e) {
                    console.error('Failed to get home path:', e);
                    return '/';
                }
            }

            async loadDirectory(path) {
                try {
                    const response = await fetch(`/api/v1/client/filesystem?path=${encodeURIComponent(path)}&type=${this.typeFilter.value}`, {
                        headers: { 'X-Client-API-Key': this.chat.apiKey }
                    });

                    if (!response.ok) {
                        throw new Error(`HTTP error! status: ${response.status}`);
                    }

                    const data = await response.json();

                    // Update current path display
                    this.currentPath.textContent = data.current_path;

                    // Clear and populate file list
                    this.fileList.innerHTML = '';
                    data.entries.forEach(entry => this.addEntry(entry));

                    // Add to history if it's a new path
                    if (!this.history.length || this.history[this.history.length - 1] !== path) {
                        this.history.push(path);
                    }
                } catch (error) {
                    console.error('Failed to load directory:', error);
                    this.showError(`Failed to load directory: ${error.message}`);
                }
            }

            showError(message) {
                // Create error element
                const errorDiv = document.createElement('div');
                errorDiv.className = 'file-browser-error';
                errorDiv.textContent = message;

                // Clear and show error
                this.fileList.innerHTML = '';
                this.fileList.appendChild(errorDiv);
            }

            addEntry(entry) {
                const div = document.createElement('div');
                div.className = `file-entry ${entry.type} ${!entry.is_readable ? 'not-readable' : ''}`;

                const icon = entry.type === 'dir' ? '📁' : this.getFileIcon(entry.mime_type);
                const size = entry.type === 'dir' ? '--' : this.formatSize(entry.size);
                const date = new Date(entry.modified).toLocaleString();

                div.innerHTML = `
                    <div class="file-info">
                        <span class="file-icon">${icon}</span>
                        <span class="file-name">${entry.name}</span>
                        <div class="file-meta">
                            <span class="file-size">${size}</span>
                            <span class="file-date">${date}</span>
                        </div>
                    </div>
                `;

                if (entry.is_readable) {
                    div.onclick = () => {
                        if (entry.type === 'dir') {
                            // For directories, navigate into them
                            this.loadDirectory(entry.path);
                        } else {
                            // For files, select them for sending
                            this.selectFile(entry);
                        }
                    };
                }

                this.fileList.appendChild(div);
            }

            selectFile(entry) {
                // Only allow selecting files, not directories
                if (entry.type === 'dir') {
                    return;
                }

                // Create a File-like object with the path
                const file = {
                    name: entry.name,
                    path: entry.path,
                    type: entry.mime_type,
                    size: entry.size
                };

                // Send the file
                this.chat.handleFileUpload(file);
                this.hide();
            }

            goBack() {
                if (this.history.length > 1) {
                    this.history.pop(); // Remove current
                    const previousPath = this.history.pop(); // Get & remove previous
                    this.loadDirectory(previousPath);
                }
            }

            goUp() {
                const parentPath = this.currentPath.textContent.split('/').slice(0, -1).join('/') || '/';
                this.loadDirectory(parentPath);
            }

            getFileIcon(mimeType) {
                if (!mimeType) return '📄';
                if (mimeType.startsWith('image/')) return '🖼️';
                if (mimeType.startsWith('video/')) return '🎬';
                if (mimeType.startsWith('audio/')) return '🎵';
                if (mimeType.startsWith('text/')) return '📝';
                return '📄';
            }

            formatSize(bytes) {
                const units = ['B', 'KB', 'MB', 'GB'];
                let size = bytes;
                let unitIndex = 0;
                while (size >= 1024 && unitIndex < units.length - 1) {
                    size /= 1024;
                    unitIndex++;
                }
                return `${size.toFixed(1)} ${units[unitIndex]}`;
            }

            toggleHidden() {
                // TODO: Implement toggle hidden files
                this.loadDirectory(this.currentPath.textContent);
            }
        }



        // Add access denied check
        window.onload = () => {
            // Check if we're accessing from non-localhost
            if (!window.location.hostname.match(/^(localhost|127\.0\.0\.1)$/)) {
                document.getElementById('access-denied').style.display = 'flex';
                return;
            }
            new BasicChat();
        };

        // Helper function to escape HTML
        function escapeHtml(unsafe) {
            return unsafe
                .replace(/&/g, "&amp;")
                .replace(/</g, "&lt;")
                .replace(/>/g, "&gt;")
                .replace(/"/g, "&quot;")
                .replace(/'/g, "&#039;");
        }
