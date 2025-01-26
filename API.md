# CyberChat API Documentation

## Overview
CyberChat uses a hybrid API architecture:
1. Core API for peer-to-peer server communication
2. Client API for web client operations
3. WebSocket for real-time updates
4. mDNS for peer discovery

All HTTPS endpoints use TLS with self-signed certificates. While browsers will show security warnings, all traffic is encrypted.

## Security Model
- Transport Layer: HTTPS/WSS with TLS 1.2+
- Message Layer: RSA encryption for peer-to-peer messages
- Client Authentication: API key required for client endpoints
- Certificates: Self-signed (generated per peer)

## Core API Endpoints (Peer-to-Peer)

| Endpoint | API.md | server.go | Status |
|----------|---------|-----------|---------|
| GET /api/v1/whoami | ✓ Documented | ✓ Implemented | Aligned |
| GET /api/v1/discovery | ✓ Documented | ✓ Implemented | Aligned |
| POST /api/v1/message | ✓ Documented | ✓ Implemented | Aligned |
| GET /api/v1/file/{file_id} | ✓ Documented | ✓ Implemented | Aligned |


### Identity
#### GET /api/v1/whoami
Returns the server's identity information.

**Response:**
```json
{
    "guid": "string",
    "public_key": "string (PEM format)",
    "name": "string"
}
```

### Peer Discovery
#### GET /api/v1/discovery
Returns list of currently discovered peers (real-time network discovery).

**Response:**
```json
[
    {
        "GUID": "string",
        "Port": number,
        "Name": "string",
        "IPAddress": "string",
        "LastSeen": "string (ISO)"
    }
]
```

### Messages
#### POST /api/v1/message
Internal endpoint for server-to-server message forwarding. Not for client use.

**Request Body:**
```json
{
    "type": "string",
    "content": "string (encrypted)",
    "sender_guid": "string",
    "receiver_guid": "string"
}
```

## Client API Endpoints

| Endpoint | API.md | server.go | Status |
|----------|---------|-----------|---------|
| GET /api/v1/client/auth | ✓ Documented | ✓ Implemented | Aligned |
| GET /api/v1/client/message | ✓ Documented | ✓ Implemented | Aligned |
| POST /api/v1/client/message | ✓ Documented | ✓ Implemented | Aligned |
| POST /api/v1/client/message/truncate | ✓ Documented | ✓ Implemented | Aligned |
| GET /api/v1/client/peers | ✓ Documented | ✓ Implemented | Aligned |
| POST /api/v1/client/file | ✓ Documented | ✓ Implemented | Aligned |
| POST /api/v1/client/name | ✗ Not Documented | ✓ Implemented | Need Doc |
| GET /api/v1/client/filesystem | ✗ Not Documented | ✓ Implemented | Need Doc |
| POST /api/v1/client/file/truncate | ✗ Not Documented | ✓ Implemented | Need Doc |

#### POST /api/v1/client/name
Updates the client's display name.

**Headers:**
- X-Client-API-Key: string (required)

**Request Body:**
```json
{
    "name": "string"
}
```

#### GET /api/v1/client/filesystem
Returns the list of available files.

**Headers:**
- X-Client-API-Key: string (required)

**Response:**
```json
[
    {
        "file_id": "string",
        "filename": "string",
        "size": number,
        "mime_type": "string",
        "created_at": "string (ISO)",
        "sender_guid": "string",
        "receiver_guid": "string"
    }
]
```

#### POST /api/v1/client/file/truncate
Clears all files from storage.

**Headers:**
- X-Client-API-Key: string (required)

### Authentication
#### GET /api/v1/client/auth
Returns the client API key required for other client endpoints.

**Response:**
```json
{
    "api_key": "string"
}
```

### Messages
#### GET /api/v1/client/message
Retrieves messages for the web client.

**Query Parameters:**
- since: ISO timestamp (optional)
- limit: number (optional, default 100)

**Response:**
```json
[
    {
        "type": "string",
        "content": "string",
        "sender_guid": "string",
        "receiver_guid": "string",
        "timestamp": "string (ISO)",
        "scope": "string"
    }
]
```

#### POST /api/v1/client/message
Sends a message from the web client.

**Headers:**
- X-Client-API-Key: string (required)

**Request Body:**
```json
{
    "type": "string",
    "content": "string",
    "receiver_guid": "string",
    "scope": "string"
}
```

#### POST /api/v1/client/message/truncate
Clears all messages from the database.

**Headers:**
- X-Client-API-Key: string (required)

### Peer Management
#### GET /api/v1/client/peers
Returns all known active peers from the peer manager (includes both discovered and database-persisted peers active within last 10 minutes).

**Headers:**
- X-Client-API-Key: string (required)

**Response:**
```json
[
    {
        "guid": "string",
        "username": "string",
        "ip_address": "string",
        "port": number,
        "last_seen": "string (ISO)"
    }
]
```

**Note:** The peer system uses two complementary mechanisms:
1. **Discovery Service** (`/api/v1/discovery`)
   - Real-time network peer discovery via mDNS
   - Only shows currently broadcasting peers
   - Resets on service restart

2. **Peer Manager** (`/api/v1/client/peers`)
   - Maintains complete peer state
   - Includes peers seen within last 10 minutes
   - Persists peer information in database
   - Survives service restarts
   - Primary source for peer information

For a complete peer system, use the Peer Manager endpoints as your primary peer list, while Discovery Service keeps that list updated with real-time network changes.

### Files
#### POST /api/v1/client/file
Uploads a file.

**Headers:**
- X-Client-API-Key: string (required)

**Request Body:**
- Multipart form data with file

#### GET /api/v1/client/file/{id}
Downloads a file.

**Headers:**
- X-Client-API-Key: string (required)

#### POST /api/v1/client/name
Updates the client's display name.

**Headers:**
- X-Client-API-Key: string (required)

**Request Body:**
```json
{
    "name": "string"
}
```

#### GET /api/v1/client/filesystem
Returns the list of available files.

**Headers:**
- X-Client-API-Key: string (required)

**Response:**
```json
[
    {
        "file_id": "string",
        "filename": "string",
        "size": number,
        "mime_type": "string",
        "created_at": "string (ISO)",
        "sender_guid": "string",
        "receiver_guid": "string"
    }
]
```

#### POST /api/v1/client/file/truncate
Clears all files from storage.

**Headers:**
- X-Client-API-Key: string (required)

## WebSocket
WebSocket connection for real-time updates.

### Endpoint
```
ws(s)://<host>/ws
```

### Message Types
1. message: New message received
2. peer: Peer update
3. file: File transfer update

## REST API Endpoints

### Debug
#### GET /status
Returns server status information. Available on all interfaces (0.0.0.0) for development purposes.

**Response:**
```json
{
    "identity": {
        "guid": "string",
        "public_key": "string (PEM format)"
    },
    "peers": [
        {
            "GUID": "string",
            "Port": number,
            "Name": "string",
            "LastSeen": "string (ISO-8601)",
            "Address": "string"
        }
    ],
    "stats": {
        "message_count": number,
        "uptime": "string",
        "start_time": "string (ISO-8601)",
        "active_connections": number
    },
    "recent_messages": [
        {
            "id": "string",
            "sender_guid": "string",
            "receiver_guid": "string",
            "type": "text|image|file",
            "content": "string",
            "timestamp": "string (ISO-8601)",
            "source_ip": "string"
        }
    ],
    "connections": [
        {
            "id": "string",
            "remote_addr": "string",
            "connected_at": "string (ISO-8601)",
            "messages_sent": number,
            "messages_received": number
        }
    ]
}
```

Note: This endpoint is intended for development and debugging only. It should be disabled in production environments.