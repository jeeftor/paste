# paste

Self-hosted paste/file-drop service with web UI, REST API, and MCP server.

## Features

- **Web UI** — Ctrl+V image paste, drag-and-drop, text snippets, syntax highlighting, browse gallery
- **REST API** — list, upload, download, delete files and text snippets
- **MCP Server** — tool calls for AI agents (Hermes, Devin, etc.)
- **No auth** — open access on all interfaces
- **Auto-expire** — configurable TTL per item (1h, 1d, 7d, 30d, never)
- **Files on disk** — plain files, directly readable by agents with filesystem access
- **Unique IDs** — short 6-character IDs for every item
- **Single Go binary** — no runtime dependencies

## Quick Start

```bash
docker run -d \
  -p 8080:8080 \
  -v ./data:/data \
  -e BASE_URL=https://paste.example.com \
  ghcr.io/jeeftor/paste:latest
```

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `PORT` | `8080` | HTTP port |
| `DATA_DIR` | `/data` | Storage directory |
| `BASE_URL` | `http://localhost:8080` | Public URL for generating links |

## REST API

```bash
# List all items
curl /api/files

# Upload a file
curl -F 'file=@screenshot.png' -F 'ttl=7d' /api/upload

# Download a file
curl /api/files/{id} -o file.png

# Create a text snippet
curl -X POST -H 'Content-Type: application/json' \
  -d '{"content":"hello world","name":"note.txt","ttl":"7d"}' \
  /api/text

# Get text content
curl /api/text/{id}

# Delete
curl -X DELETE /api/files/{id}

# Health check
curl /health
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `list_files` | List all items with metadata |
| `get_file` | Get file content (base64) or text content |
| `get_file_url` | Get public URL for an item |
| `upload_file` | Upload a file (base64 content + filename) |
| `create_text` | Create a text snippet |
| `get_text` | Get raw text snippet content |
| `delete_file` | Delete an item |

MCP endpoint: `https://paste.example.com/mcp`

## Direct Access

- Files: `https://paste.example.com/f/{id}`
- Text: `https://paste.example.com/t/{id}`

## Storage

- Files: `{DATA_DIR}/files/`
- Text: `{DATA_DIR}/text/`
- Metadata: `{DATA_DIR}/metadata.json`

## License

MIT
