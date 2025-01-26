class FileTransferManager {
    constructor() {
        console.log('FileTransferManager initializing...');
        this.transfers = new Map();
        this.container = null;
        this.isMinimized = false;
        this.initializeUI();
        this.setIdleState(true); // Start in idle state
        console.log('FileTransferManager initialization complete');
    }

    initializeUI() {
        console.log('Creating file transfer UI container...');
        // Create container
        this.container = document.createElement('div');
        this.container.className = 'file-transfer-container';

        // Create header
        const header = document.createElement('div');
        header.className = 'file-transfer-header';

        const title = document.createElement('div');
        title.className = 'file-transfer-title';
        title.textContent = 'FILE TRANSFERS';

        const minimizeBtn = document.createElement('button');
        minimizeBtn.className = 'file-transfer-minimize';
        minimizeBtn.textContent = '−';
        minimizeBtn.onclick = () => this.toggleMinimize();

        header.appendChild(title);
        header.appendChild(minimizeBtn);
        this.container.appendChild(header);

        document.body.appendChild(this.container);
        console.log('File transfer UI container created and added to DOM');
    }

    toggleMinimize() {
        this.isMinimized = !this.isMinimized;
        this.container.classList.toggle('minimized');
        const btn = this.container.querySelector('.file-transfer-minimize');
        btn.textContent = this.isMinimized ? '+' : '−';
    }

    formatBytes(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    formatSpeed(bytesPerSecond) {
        return this.formatBytes(bytesPerSecond) + '/s';
    }

    setIdleState(isIdle) {
        if (isIdle && this.transfers.size === 0) {
            this.container.classList.add('idle');
        } else {
            this.container.classList.remove('idle');
        }
    }

    handleTransfer(data) {
        const { content } = data;
        const { transferId } = content;

        // Remove idle state when there's activity
        this.setIdleState(false);

        if (!this.transfers.has(transferId)) {
            // Create new transfer item
            const item = document.createElement('div');
            item.className = 'file-transfer-item';
            item.id = `transfer-${transferId}`;

            const info = document.createElement('div');
            info.className = 'file-transfer-info';

            const statusIcon = document.createElement('span');
            statusIcon.className = `transfer-status-icon status-${content.status}`;

            const nameSpan = document.createElement('span');
            nameSpan.className = 'transfer-name';
            nameSpan.textContent = `${content.filename} from `;

            const clientSpan = document.createElement('span');
            clientSpan.className = 'transfer-client';
            clientSpan.textContent = content.client_ip || 'unknown';

            const sizeSpan = document.createElement('span');
            sizeSpan.className = 'transfer-size';
            sizeSpan.textContent = ` (${this.formatBytes(content.size)})`;

            info.appendChild(statusIcon);
            info.appendChild(nameSpan);
            info.appendChild(clientSpan);
            info.appendChild(sizeSpan);

            const progress = document.createElement('div');
            progress.className = 'file-transfer-progress';
            const progressBar = document.createElement('div');
            progressBar.className = 'progress-bar';
            progress.appendChild(progressBar);

            const stats = document.createElement('div');
            stats.className = 'file-transfer-stats';

            item.appendChild(info);
            item.appendChild(progress);
            item.appendChild(stats);

            this.container.appendChild(item);
            this.transfers.set(transferId, { item, startTime: content.start_time });

            // Show container if minimized
            if (this.isMinimized) {
                this.toggleMinimize();
            }
        }

        const transfer = this.transfers.get(transferId);
        const item = transfer.item;

        // Update status
        const statusIcon = item.querySelector('.transfer-status-icon');
        statusIcon.className = `transfer-status-icon status-${content.status}`;

        // Update progress bar and stats
        if (content.status === 'transferring') {
            const progressBar = item.querySelector('.progress-bar');
            progressBar.style.width = `${content.progress || 0}%`;

            const stats = item.querySelector('.file-transfer-stats');
            const speed = content.speed || 0;
            const bytesRead = content.bytes_read || 0;
            stats.innerHTML = `
                <span>${content.progress || 0}%</span>
                <span class="transfer-speed">${this.formatSpeed(speed)}</span>
                <span>${this.formatBytes(bytesRead)} / ${this.formatBytes(content.size)}</span>
            `;
        } else if (content.status === 'completed') {
            const progressBar = item.querySelector('.progress-bar');
            progressBar.style.width = '100%';
            item.classList.add('completed');

            const stats = item.querySelector('.file-transfer-stats');
            const avgSpeed = content.avg_speed || 0;
            stats.innerHTML = `
                <span>Completed in ${(content.duration || 0).toFixed(1)}s</span>
                <span class="transfer-speed">Avg: ${this.formatSpeed(avgSpeed)}</span>
            `;

            // Remove item after 5 seconds
            setTimeout(() => {
                item.style.opacity = '0';
                setTimeout(() => {
                    item.remove();
                    this.transfers.delete(transferId);
                    this.checkIdleState();
                }, 500);
            }, 5000);
        } else if (content.status === 'failed') {
            item.classList.add('failed');
            const stats = item.querySelector('.file-transfer-stats');
            stats.innerHTML = `<span class="transfer-error">Error: ${content.error || 'Unknown error'}</span>`;

            // Remove failed transfer after 5 seconds
            setTimeout(() => {
                item.style.opacity = '0';
                setTimeout(() => {
                    item.remove();
                    this.transfers.delete(transferId);
                    this.checkIdleState();
                }, 500);
            }, 5000);
        }

        // Check if we should go idle (no active transfers)
        this.checkIdleState();
    }

    checkIdleState() {
        // If no more transfers, start idle timer
        if (this.transfers.size === 0) {
            setTimeout(() => this.setIdleState(true), 2000);
        }
    }
}

// Initialize the manager after DOM is loaded
document.addEventListener('DOMContentLoaded', () => {
    console.log('DOM loaded, initializing FileTransferManager...');
    window.fileTransferManager = new FileTransferManager();
});

// Add WebSocket message handler
window.addEventListener('load', () => {
    const originalMessageHandler = window.chat.handleWebSocketMessage;
    window.chat.handleWebSocketMessage = function(event) {
        const data = JSON.parse(event.data);
        if (data.type === 'file_transfer') {
            window.fileTransferManager.handleTransfer(data);
        }
        originalMessageHandler.call(window.chat, event);
    };
});

// Add error handling for file transfer messages
window.addEventListener('error', (event) => {
    console.error('File transfer error:', event.error);
});