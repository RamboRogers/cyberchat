# CyberChat Client API Documentation

## Overview
CyberChat uses a combination of REST APIs and WebSocket connections for real-time communication. Messages are end-to-end encrypted when sent between peers.

## REST API Endpoints

### Identity
```
GET /api/v1/whoami
```
Response:
```json
{
    "guid": "string",
    "public_key": "PEM encoded RSA public key",
    "name": "string"
}
```

### Messages

#### Send Message
```
POST /api/v1/message
Content-Type: application/json

// For web client (https encryption):
{
    "type": "text|image|file",
    "content": "string",
    "receiver_guid": "string",
    "receiver_port": number
}

// For peer-to-peer (https + public key encrypted):
{
    "id": "string",
    "sender_guid": "string",
    "receiver_guid": "string",
    "type": "text|image|file",
    "scope": "private|broadcast",
    "content": "base64 encoded encrypted content",
    "timestamp": "ISO-8601 string"
}
```

#### Get Messages
```
GET /api/v1/message?since={timestamp}&limit={number}
```
- `since`: ISO-8601 timestamp (optional, defaults to 24h ago)
- `limit`: Number of messages (optional, defaults to 100, max 1000)
- Returns array of Message objects

### File Transfer

Files are shared by sending a message containing the file path and metadata. The file path is used to access the file directly through the server's file system.

#### File Message Format
```json
{
    "type": "text",
    "content": {
        "type": "file",
        "file_id": "string (uuid)",
        "name": "string",
        "mime": "string",
        "size": number,
        "filepath": "string"
    },
    "receiver_guid": "string",
    "scope": "private"
}
```

#### GET /api/v1/file/{file_id}
Downloads a file using its unique ID.

**Response:**
- File content with appropriate Content-Type and Content-Disposition headers

### Discovery
```
GET /api/v1/discovery
```
Response:
```json
[
    {
        "GUID": "string",
        "Name": "string",
        "Port": number,
        "IPAddress": "string"
    }
]
```

### Status
```
GET /status
```
Response:
```json
{
    "guid": "string",
    "name": "string",
    "port": number,
    "ip_address": "string",
    "peers": [
        {
            "guid": "string",
            "name": "string",
            "ip_address": "string",
            "port": number,
            "public_key": "truncated base64",
            "last_seen": "ISO-8601 string",
            "group_name": "string"
        }
    ]
}
```

## WebSocket Protocol

### Connection
- Connect to: `ws://{host}:{port}/ws` or `wss://{host}:{port}/ws`
- Supports ping/pong for keepalive (54s interval)
- Maximum message size: 512KB

### Message Types

#### Client → Server

1. Chat Message:
```json
{
    "type": "message",
    "content": {
        "type": "text|image|file",
        "content": "string",
        "receiver_guid": "string"  // empty for broadcast
    }
}
```

2. Connection Management:
```json
{
    "type": "ping"
}
```

#### Server → Client

1. Chat Message:
```json
{
    "type": "message",
    "content": {
        "id": "string",
        "sender_guid": "string",
        "receiver_guid": "string",
        "type": "text|image|file",
        "scope": "private|broadcast",
        "content": "string",
        "timestamp": "ISO-8601 string"
    }
}
```

2. Peer Update:
```json
{
    "type": "peer",
    "content": {
        "guid": "string",
        "name": "string",
        "port": number,
        "ip_address": "string"
    }
}
```

3. Connection Management:
```json
{
    "type": "pong"
}
```

## Implementation Notes

### Message Handling
- Messages are stored in SQLite database
- Messages older than 30 days are automatically cleaned up
- Messages can be text, images, or files
- Maximum message size: 100MB
- Messages between peers are encrypted using RSA-OAEP

### File Transfer
- Files are transferred using custom protocol wrapped in https
- Automatic cleanup of completed transfers

### Security
- All connections use TLS
- RSA key pair generated on first run
- Public keys exchanged via mDNS
- End-to-end encryption for peer messages
- Self-signed certificates are accepted