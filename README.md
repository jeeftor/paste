<div align="center">

<img src="https://raw.githubusercontent.com/jeeftor/klipbord/main/icon.svg" width="120" alt="Klipbord logo" />

# Klipbord

**Self-hosted clipboard for AI agents and humans alike.**
Drop files, paste text, process images with vision LLMs — all through a slick web UI, REST API, and MCP server.

[![Release](https://img.shields.io/github/v/release/jeeftor/klipbord?style=flat-square&color=2f6fed)](https://github.com/jeeftor/klipbord/releases)
[![Docker](https://img.shields.io/badge/docker-ghcr.io%2Fjeeftor%2Fklipbord-2f6fed?style=flat-square&logo=docker&logoColor=white)](https://github.com/jeeftor/klipbord/pkgs/container/klipbord)
[![Go](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Tests](https://img.shields.io/github/actions/workflow/status/jeeftor/klipbord/ci.yml?style=flat-square&label=tests)](https://github.com/jeeftor/klipbord/actions)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](LICENSE)

</div>

---

## Features

| | |
|--|--|
| **Web UI** | Ctrl+V image paste, drag-and-drop, text snippets, syntax highlighting, search |
| **REST API** | List, upload, download, delete, pin files and text snippets |
| **MCP Server** | 20 tool calls for AI agents (Claude Code, Hermes, Devin, etc.) |
| **Vision Pre-processing** | Auto-OCR/describe uploaded images via any OpenAI-compatible vision LLM |
| **Multi-Prompt Analysis** | Analyze one image with multiple prompts — all results stored side-by-side |
| **Preset Comparison** | Run an image through all vision backends in parallel, rank with LLM judging |
| **Client-side Search** | Search filenames, vision OCR text, and paste content across both tabs |
| **Auto-expire** | Configurable TTL per item (1h, 1d, 7d, 30d, never) |
| **Persistent Pinning** | Mark items as persistent to exempt from expiry |
| **OpenAPI 3.0** | Machine-readable spec at `/api/openapi.json` + Swagger UI |
| **Single Go binary** | No runtime dependencies; built-in race-enabled test and static-analysis checks |

---

## Quick Start

```bash
docker run -d \
  -p 8080:8080 \
  -v ./data:/data \
  -e BASE_URL=https://klipbord.example.com \
  ghcr.io/jeeftor/klipbord:latest
```

Then open `http://localhost:8080` — drop a file, paste an image, share a snippet.

---

## Configuration

### Environment Variables

| Env Var | Default | Description |
|---------|---------|-------------|
| `PORT` | `8080` | HTTP port |
| `DATA_DIR` | `/data` | Storage directory |
| `BASE_URL` | `http://localhost:8080` | Public URL for generating links |
| `MAX_UPLOAD_MB` | `2048` | Max upload size in MB |
| `VISION_ENABLED` | `true` | Enable automatic image analysis on upload |
| `VISION_REQUEST_TIMEOUT` | `2m` | Maximum time for each matrix inference request |
| `VISION_UNLOAD_TIMEOUT` | `2m` | Maximum time to wait for unload and observed memory release |
| `VISION_ENDPOINT` | *(see presets)* | OpenAI-compatible vision LLM endpoint (overrides UI config) |
| `VISION_MODEL` | *(see presets)* | Vision model name to use (overrides UI config) |

### Vision LLM

Configure via the **Config tab** in the UI or via environment variables.

**Env vars always win** — if `VISION_ENDPOINT` or `VISION_MODEL` are set they override the UI. A locked "env" preset appears in the UI to indicate this.

Three presets ship on first boot:

| Preset | Endpoint | Model |
|--------|----------|-------|
| `lemonade` | `http://localhost:13305/v1/chat/completions` | `Qwen3-VL-4B-Instruct-GGUF` |
| `ollama` | `http://localhost:11434/v1/chat/completions` | `llama3.2-vision` |
| `openai` | `https://api.openai.com/v1/chat/completions` | `gpt-4o-mini` |

To disable vision entirely: `VISION_ENABLED=false`

---

## Vision Pre-Processing

When an image is uploaded, Klipbord automatically sends it to the configured vision LLM. The result (extracted text + description) is stored alongside the image and surfaced via MCP tools and the REST API.

### Built-In Prompts

| Prompt | Use Case |
|--------|----------|
| `default` | General-purpose analysis — OCR + description |
| `terminal` | Terminal screenshots — commands, output, errors |
| `code` | Code screenshots — preserves indentation, detects language |
| `document` | Documents/receipts — structured layout extraction |
| `diagram` | Diagrams/charts — structure, connections, flow |

### Custom Prompts

```bash
# Create
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"ui_mockup","description":"UI mockup analysis","prompt":"Analyze this UI mockup..."}' \
  /api/prompts

# Update
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"description":"Updated"}' /api/prompts/ui_mockup

# Delete
curl -X DELETE /api/prompts/ui_mockup
```

### Multi-Prompt Analysis

```bash
curl -X POST /api/analyze/{id}?prompt=terminal
curl -X POST /api/analyze/{id}?prompt=code
curl /api/files/{id}   # both results returned in analyses field
```

---

## REST API

<details>
<summary><strong>Items</strong></summary>

```bash
curl /api/files                                          # list all
curl -F 'file=@screenshot.png' -F 'ttl=7d' /api/upload  # upload file
curl /api/files/{id} -o file.png                         # download
curl -X POST -H 'Content-Type: application/json' \
  -d '{"content":"hello","name":"note.txt","ttl":"7d"}' /api/text   # create text snippet
curl /api/text/{id}                                      # get raw text
curl -X DELETE /api/files/{id}                           # delete
curl -X PATCH -H 'Content-Type: application/json' \
  -d '{"persistent":true}' /api/files/{id}              # pin/unpin
```

</details>

<details>
<summary><strong>Vision</strong></summary>

```bash
curl -X POST /api/analyze/{id}?prompt=terminal           # trigger analysis
curl -X POST /api/vision/test                            # test with built-in sample
curl -X POST -H 'Content-Type: application/json' \
  -d '{"image_type":"code"}' /api/vision/test            # test specific image type
curl -X POST -H 'Content-Type: application/json' \
  -d '{"image_type":"terminal"}' /api/vision/compare     # compare all presets
curl -X POST -H 'Content-Type: application/json' \
  -d '{"item_id":"abc123"}' /api/vision/compare          # compare using uploaded image
curl -X POST -H 'Content-Type: application/json' \
  -d '{"image_type":"terminal"}' /api/vision/compare-prompts  # compare all prompts
```

</details>

<details>
<summary><strong>Vision Config</strong></summary>

```bash
curl /api/config/vision                                  # get config
curl -X POST -H 'Content-Type: application/json' \
  -d '{"preset":"ollama"}' /api/config/vision/active     # set active preset
curl -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":false}' /api/config/vision/enabled      # toggle vision
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"my-llm","endpoint":"http://localhost:8080/v1/chat/completions","model":"my-model"}' \
  /api/config/vision/presets                             # create preset
curl -X DELETE /api/config/vision/presets/my-llm        # delete preset
curl -X POST -H 'Content-Type: application/json' \
  -d '{"preset":"lemonade"}' /api/config/vision/test     # test connection
```

</details>

<details>
<summary><strong>Prompts</strong></summary>

```bash
curl /api/prompts                                        # list all
curl /api/prompts/{name}                                 # get one
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"x","description":"y","prompt":"z"}' /api/prompts   # create
curl -X PUT -H 'Content-Type: application/json' \
  -d '{"prompt":"new text"}' /api/prompts/{name}         # update
curl -X DELETE /api/prompts/{name}                       # delete (custom only)
```

</details>

<details>
<summary><strong>Health</strong></summary>

```bash
curl /api/health      # → {"status":"ok"}
curl /api/version     # → {"version":"vX.Y.Z"}
curl /api/openapi.json
```

</details>

---

## MCP Server

```json
{ "mcpServers": { "kb": { "url": "https://klipbord.example.com/mcp" } } }
```

<details>
<summary><strong>All 20 tools</strong></summary>

| Tool | Description |
|------|-------------|
| `list_files` | List all items with metadata |
| `get_file` | Get file content (base64) or text content |
| `get_file_url` | Get public URL for an item |
| `upload_file` | Upload a file (base64 content + filename) |
| `create_text` | Create a text snippet |
| `get_text` | Get raw text snippet content |
| `delete_file` | Delete an item |
| `persist_file` | Pin or unpin an item |
| `describe_image` | Get vision analysis for an image |
| `analyze_image` | Trigger/re-trigger vision analysis |
| `list_prompts` | List all available vision prompts |
| `create_prompt` | Create a new vision prompt template |
| `update_prompt` | Update an existing prompt |
| `delete_prompt` | Delete a custom prompt |
| `list_vision_presets` | List all configured vision LLM presets |
| `set_vision_preset` | Switch the active vision LLM preset |
| `test_vision_preset` | Test connectivity to a preset |
| `test_vision` | Run the full vision pipeline on a sample image |
| `compare_vision` | Run image through ALL presets, rank results |
| `compare_prompts` | Run image through ALL prompts, rank results |

</details>

<details>
<summary><strong>Example tool calls</strong></summary>

```jsonc
// Describe an image
{"name": "describe_image", "arguments": {"id": "abc123"}}

// With a specific prompt
{"name": "describe_image", "arguments": {"id": "abc123", "prompt": "terminal"}}

// Trigger re-analysis
{"name": "analyze_image", "arguments": {"id": "abc123", "prompt": "code"}}

// Compare all presets on a sample image
{"name": "compare_vision", "arguments": {"image_type": "terminal"}}

// Compare all prompts
{"name": "compare_prompts", "arguments": {"image_type": "terminal"}}

// Switch active preset
{"name": "set_vision_preset", "arguments": {"preset": "ollama"}}
```

</details>

---

## Direct Links

```
https://klipbord.example.com/link/{id} # file, image, or text snippet
```

---

## Web UI Routes

Each UI section has a stable URL, so it remains selected after a refresh and can be bookmarked.

| Route | UI section |
|-------|------------|
| `/clip` | Clipboard items and upload controls |
| `/persist` | Persistent items |
| `/config` | Vision configuration |
| `/mcp-web` | MCP setup and tool reference |
| `/rest-web` | REST API reference |

`/` redirects to `/clip`. The browser UI routes are intentionally separate from the machine interfaces: REST remains under `/api/...`, MCP remains at `/mcp`, and direct item links remain under `/link/{id}`.

---

## Storage Layout

| Path | Content |
|------|---------|
| `{DATA_DIR}/files/` | Uploaded files |
| `{DATA_DIR}/text/` | Text snippets |
| `{DATA_DIR}/chunks/` | Chunked upload temp files |
| `{DATA_DIR}/metadata.json` | Item metadata (IDs, names, MIME, TTL, analyses) |
| `{DATA_DIR}/prompts.json` | Custom vision prompts |
| `{DATA_DIR}/vision_config.json` | Vision LLM presets and active selection |

---

## Development

```bash
make build   # go build -o klipbord .
make run     # go run .
make test    # go test ./...
```

Tests use a mock vision server — no external LLM required. CI runs tests before building the Docker image; the `build` job is gated on `test`.

---

<div align="center">
MIT License &nbsp;·&nbsp; <a href="https://github.com/jeeftor/klipbord">github.com/jeeftor/klipbord</a>
</div>
