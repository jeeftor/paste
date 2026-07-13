# paste

Self-hosted paste/file-drop service with web UI, REST API, MCP server, and AI vision pre-processing.

## Features

- **Web UI** — Ctrl+V image paste, drag-and-drop, text snippets, syntax highlighting, browse gallery
- **REST API** — list, upload, download, delete, pin files and text snippets
- **MCP Server** — 11 tool calls for AI agents (Hermes, Devin, Claude Code, etc.)
- **Vision Pre-processing** — automatically OCR/describe uploaded images using a local or remote vision LLM
- **Configurable Prompts** — 5 built-in vision prompt templates (terminal, code, document, diagram, default) plus custom user-defined prompts via REST API
- **Multi-Prompt Analysis** — analyze the same image with multiple prompts, all results stored side-by-side
- **No auth** — open access on all interfaces
- **Auto-expire** — configurable TTL per item (1h, 1d, 7d, 30d, never)
- **Persistent pinning** — mark items as persistent to exempt them from expiry
- **Files on disk** — plain files, directly readable by agents with filesystem access
- **Unique IDs** — short 6-character IDs for every item
- **Single Go binary** — no runtime dependencies
- **Tested** — 69 tests with 50.3% code coverage

## Quick Start

```bash
docker run -d \
  -p 8080:8080 \
  -v ./data:/data \
  -e BASE_URL=https://paste.example.com \
  ghcr.io/jeeftor/paste:latest
```

## Configuration

### Environment Variables

| Env Var | Default | Description |
|---------|---------|-------------|
| `PORT` | `8080` | HTTP port |
| `DATA_DIR` | `/data` | Storage directory |
| `BASE_URL` | `http://localhost:8080` | Public URL for generating links |
| `MAX_UPLOAD_MB` | `100` | Max upload size in MB |
| `VISION_ENABLED` | `true` | Enable automatic image analysis on upload |
| `VISION_ENDPOINT` | *(see presets)* | OpenAI-compatible vision LLM endpoint (overrides UI config) |
| `VISION_MODEL` | *(see presets)* | Vision model name to use (overrides UI config) |

### Vision LLM Configuration

The vision endpoint can be configured in two ways:

1. **UI Config Tab** — Click the "Config" tab in the web UI to manage everything vision-related:
   - **LLM Presets** — Create, edit, delete, and switch between named endpoint presets. Test each preset's connection with a single click.
   - **Vision Prompts** — View, edit, add, and delete prompt templates that control how the vision model analyzes images. Built-in prompts (`default`, `terminal`, `code`, `document`, `diagram`) can be edited but not deleted.
   - **Enable/Disable** — Toggle vision processing on or off.

2. **Environment Variables** — Set `VISION_ENDPOINT` and/or `VISION_MODEL` to override the UI config. When env vars are set, an "env" preset appears in the UI and is locked as active. This is useful for Docker deployments where you want to lock the config.

**Env vars always win** — if `VISION_ENDPOINT` or `VISION_MODEL` are set, they override any UI configuration. The UI will show a banner indicating env vars are active.

### Built-In Presets

Three presets are pre-configured on first boot:

| Preset | Endpoint | Model | Description |
|--------|----------|-------|-------------|
| `lemonade` | `http://localhost:13305/v1/chat/completions` | `Qwen3-VL-4B-Instruct-GGUF` | Local Lemonade (GPU) |
| `ollama` | `http://localhost:11434/v1/chat/completions` | `llama3.2-vision` | Local Ollama |
| `openai` | `https://api.openai.com/v1/chat/completions` | `gpt-4o-mini` | OpenAI cloud (requires API key) |

To disable vision entirely, set `VISION_ENABLED=false` or toggle it off in the UI settings.

## Vision Pre-Processing

When an image is uploaded, paste automatically sends it to the configured vision LLM for analysis. The analysis result (extracted text + description) is stored alongside the image and made available to AI agents via MCP tools and the REST API.

### Built-In Prompts

| Prompt | Use Case |
|--------|----------|
| `default` | General-purpose image analysis with OCR and description |
| `terminal` | Terminal screenshots — extracts commands, output, errors with structure |
| `code` | Code screenshots — preserves indentation, identifies language |
| `document` | Documents/receipts — structured extraction with layout preservation |
| `diagram` | Diagrams/charts — describes structure, connections, flow |

### Custom Prompts

Create, update, and delete custom vision prompts via the REST API:

```bash
# Create a custom prompt
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"ui_mockup","description":"UI mockup analysis","prompt":"Analyze this UI mockup..."}' \
  /api/prompts

# List all prompts (built-in + custom)
curl /api/prompts

# Update a prompt
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"description":"Updated description"}' \
  /api/prompts/ui_mockup

# Delete a custom prompt (built-in prompts cannot be deleted)
curl -X DELETE /api/prompts/ui_mockup
```

Custom prompts are stored in `{DATA_DIR}/prompts.json` and persist across restarts.

### Multi-Prompt Analysis

Each image can be analyzed with multiple prompts, with all results stored side-by-side:

```bash
# Analyze with the "terminal" prompt
curl -X POST /api/analyze/{id}?prompt=terminal

# Analyze the same image with the "code" prompt
curl -X POST /api/analyze/{id}?prompt=code

# Both results are stored and returned in the item metadata
curl /api/files/{id}
```

## REST API

### Items

```bash
# List all items (optional: ?persistent=true or ?persistent=false to filter)
curl /api/files

# Upload a file
curl -F 'file=@screenshot.png' -F 'ttl=7d' /api/upload

# Download a file
curl /api/files/{id} -o file.png

# Create a text snippet
curl -X POST -H 'Content-Type: application/json' \
  -d '{"content":"hello world","name":"note.txt","ttl":"7d"}' \
  /api/text

# Get text content (returns raw text)
curl /api/text/{id}

# Delete an item
curl -X DELETE /api/files/{id}

# Pin/unpin an item (persistent items never expire)
curl -X PATCH -H 'Content-Type: application/json' \
  -d '{"persistent":true}' \
  /api/files/{id}
```

### Vision Analysis

```bash
# Trigger analysis on an image (optional: ?prompt=terminal)
curl -X POST /api/analyze/{id}?prompt=terminal

# Analysis results are included in the item metadata returned by:
curl /api/files/{id}
```

### Prompts

```bash
# List all prompts
curl /api/prompts

# Get a specific prompt
curl /api/prompts/{name}

# Create a custom prompt
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"my_prompt","description":"My prompt","prompt":"Describe..."}' \
  /api/prompts

# Update a prompt
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"prompt":"Updated prompt text"}' \
  /api/prompts/{name}

# Delete a custom prompt (built-in prompts cannot be deleted)
curl -X DELETE /api/prompts/{name}
```

### Health & Version

```bash
curl /api/health    # → {"status":"ok"}
curl /api/version   # → {"version":"v1.4.1"}
```

### Vision LLM Config

```bash
# Get current config (active preset, all presets, enabled state, env override status)
curl /api/config/vision

# Set active preset
curl -X POST -H 'Content-Type: application/json' \
  -d '{"preset":"ollama"}' \
  /api/config/vision/active

# Toggle vision enabled/disabled
curl -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":false}' \
  /api/config/vision/enabled

# Create a preset
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"my-llm","endpoint":"http://localhost:8080/v1/chat/completions","model":"my-model","api_key":"sk-...","description":"My LLM"}' \
  /api/config/vision/presets

# Update a preset
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"endpoint":"http://new:8080/v1/chat/completions","model":"new-model"}' \
  /api/config/vision/presets/my-llm

# Delete a preset (cannot delete active or env preset)
curl -X DELETE /api/config/vision/presets/my-llm

# Test a preset's connection (sends a minimal chat request)
curl -X POST -H 'Content-Type: application/json' \
  -d '{"preset":"lemonade"}' \
  /api/config/vision/test
# → {"success":true,"message":"Connected successfully — model replied: \"OK\"","latency":"127ms","model":"...","endpoint":"..."}
```

## MCP Tools

MCP endpoint: `https://paste.example.com/mcp`

| Tool | Description |
|------|-------------|
| `list_files` | List all items with metadata |
| `get_file` | Get file content (base64) or text content |
| `get_file_url` | Get public URL for an item |
| `upload_file` | Upload a file (base64 content + filename) |
| `create_text` | Create a text snippet |
| `get_text` | Get raw text snippet content |
| `delete_file` | Delete an item |
| `persist_file` | Pin or unpin an item (persistent items never expire) |
| `describe_image` | Get vision analysis for an image (optional `prompt` parameter) |
| `analyze_image` | Trigger/re-trigger vision analysis (optional `prompt` parameter) |
| `list_prompts` | List all available vision prompts (built-in + custom) |

### Vision MCP Tool Examples

```jsonc
// Describe an image (uses "default" prompt if not specified)
{"name": "describe_image", "arguments": {"id": "abc123"}}

// Describe with a specific prompt
{"name": "describe_image", "arguments": {"id": "abc123", "prompt": "terminal"}}

// Trigger analysis with a specific prompt
{"name": "analyze_image", "arguments": {"id": "abc123", "prompt": "code"}}

// List all available prompts
{"name": "list_prompts", "arguments": {}}
```

## Direct Access

- Files: `https://paste.example.com/f/{id}`
- Text: `https://paste.example.com/t/{id}`

## Storage

| Path | Content |
|------|---------|
| `{DATA_DIR}/files/` | Uploaded files (binary) |
| `{DATA_DIR}/text/` | Text snippets |
| `{DATA_DIR}/chunks/` | Temporary chunked upload chunks |
| `{DATA_DIR}/metadata.json` | Item metadata (IDs, names, MIME types, TTL, analyses) |
| `{DATA_DIR}/prompts.json` | Custom vision prompts (built-in prompts are in code) |
| `{DATA_DIR}/vision_config.json` | Vision LLM presets and active selection |

## Development

### Building

```bash
go build -o paste .
./paste
```

### Testing

```bash
go test -v -cover ./...
```

Tests use a mock vision server (no external LLM required). The test suite covers core CRUD, REST API endpoints, vision analysis, prompt management, and MCP tools.

### CI

GitHub Actions runs tests on every push/tag before building the Docker image. The `test` job gates the `build` job — if tests fail, no image is built.

## License

MIT
