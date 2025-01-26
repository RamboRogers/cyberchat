class FilesViewer {
    constructor() {
        this.overlay = null;
        this.container = null;
        this.filesList = null;
        this.files = new Map();
        this.initializeUI();
        this.setupEventListeners();
    }

    initializeUI() {
        // Create overlay
        this.overlay = document.createElement('div');
        this.overlay.className = 'files-viewer-overlay';

        // Create container
        this.container = document.createElement('div');
        this.container.className = 'files-viewer-container';

        // Create header
        const header = document.createElement('div');
        header.className = 'files-viewer-header';

        const title = document.createElement('div');
        title.className = 'files-viewer-title';
        title.textContent = 'SHARED FILES';

        const controls = document.createElement('div');
        controls.className = 'files-viewer-controls';

        const purgeBtn = document.createElement('button');
        purgeBtn.className = 'files-purge-btn';
        purgeBtn.textContent = 'PURGE ALL';
        purgeBtn.onclick = () => this.purgeFiles();

        const closeBtn = document.createElement('button');
        closeBtn.className = 'files-viewer-close';
        closeBtn.textContent = 'CLOSE';
        closeBtn.onclick = () => this.hide();

        controls.appendChild(purgeBtn);
        controls.appendChild(closeBtn);

        header.appendChild(title);
        header.appendChild(controls);

        // Create files list container
        this.filesList = document.createElement('div');
        this.filesList.className = 'files-list';

        // Assemble the components
        this.container.appendChild(header);
        this.container.appendChild(this.filesList);
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
        this.loadFiles();
        this.overlay.classList.add('show');
    }

    hide() {
        this.overlay.classList.remove('show');
    }

    async loadFiles() {
        try {
            const response = await fetch('/api/v1/client/files', {
                headers: {
                    'X-Client-API-Key': window.chat.apiKey
                }
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            const text = await response.text();
            console.log('Raw response:', text);

            try {
                const files = JSON.parse(text);
                this.updateFilesList(files);
            } catch (parseError) {
                console.error('JSON Parse Error:', parseError);
                console.log('Response that failed to parse:', text);
                this.showEmptyState('Error parsing file list');
            }
        } catch (error) {
            console.error('Failed to load files:', error);
            this.showEmptyState('Error loading files');
        }
    }

    formatFileSize(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    formatDate(timestamp) {
        const date = new Date(timestamp);
        return date.toLocaleString();
    }

    getFileEmoji(mime) {
        const emojis = {
            'image/jpeg': '🖼️',
            'image/png': '🖼️',
            'image/gif': '🖼️',
            'video/mp4': '🎬',
            'video/webm': '🎬',
            'audio/mpeg': '🎵',
            'application/pdf': '📄',
            'text/plain': '📝'
        };
        return emojis[mime] || '📎';
    }

    updateFilesList(files) {
        this.filesList.innerHTML = '';

        if (!files || files.length === 0) {
            this.showEmptyState('No shared files');
            return;
        }

        files.forEach((file, index) => {
            const fileElement = document.createElement('div');
            fileElement.className = 'file-item';
            fileElement.style.animationDelay = `${index * 0.1}s`;
            fileElement.style.cursor = 'pointer';

            // Add click handler to re-share the file
            fileElement.onclick = async () => {
                try {
                    const response = await fetch('/api/v1/client/message', {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Client-API-Key': window.chat.apiKey
                        },
                        body: JSON.stringify({
                            type: 'text',
                            content: JSON.stringify({
                                type: 'file',
                                file_id: file.FileID,
                                name: file.Filename,
                                size: file.Size,
                                mime: file.MimeType
                            }),
                            scope: 'broadcast'
                        })
                    });

                    if (!response.ok) {
                        throw new Error('Failed to send file message');
                    }

                    this.hide(); // Close the viewer after sharing
                } catch (error) {
                    console.error('Error sharing file:', error);
                }
            };

            fileElement.innerHTML = `
                <div class="file-info">
                    <div class="file-icon">${this.getFileEmoji(file.MimeType)}</div>
                    <div class="file-details">
                        <div class="file-name">${file.Filename}</div>
                        <div class="file-meta">${file.FileID}</div>
                    </div>
                </div>
                <div class="file-stats">
                    <div class="file-size">📊 ${this.formatFileSize(file.Size)}</div>
                    <div class="file-date">🕒 ${this.formatDate(file.CreatedAt)}</div>
                </div>
            `;

            this.filesList.appendChild(fileElement);
        });
    }

    showEmptyState(message) {
        this.filesList.innerHTML = `
            <div class="files-empty-state">
                <div>${message}</div>
            </div>
        `;
    }

    async purgeFiles() {
        if (!confirm('Are you sure you want to remove all shared files?')) {
            return;
        }

        // Add purging class to container for animation
        this.filesList.classList.add('purging');

        // Animate each file item
        const items = this.filesList.querySelectorAll('.file-item');
        items.forEach((item, index) => {
            setTimeout(() => {
                item.classList.add('purging');
            }, index * 100);
        });

        try {
            const response = await fetch('/api/v1/client/file/truncate', {
                method: 'POST',
                headers: {
                    'X-Client-API-Key': window.chat.apiKey
                }
            });

            if (response.ok) {
                // Wait for animations to complete
                setTimeout(() => {
                    this.filesList.classList.remove('purging');
                    this.showEmptyState('All files have been purged');
                }, items.length * 100 + 500);
            } else {
                throw new Error('Failed to purge files');
            }
        } catch (error) {
            console.error('Failed to purge files:', error);
            this.filesList.classList.remove('purging');
            this.showEmptyState('Error purging files');
        }
    }
}

// Initialize the viewer after DOM is loaded
document.addEventListener('DOMContentLoaded', () => {
    // Create the shared files viewer instance
    window.sharedFilesViewer = new FilesViewer();

    // Set up the shared files viewer button
    const sharedFilesBtn = document.getElementById('shared-files-btn');
    if (sharedFilesBtn) {
        sharedFilesBtn.onclick = () => window.sharedFilesViewer.show();
    }
});