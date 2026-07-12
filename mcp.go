package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MCP JSON-RPC types
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type MCPToolResult struct {
	Content []MCPContent `json:"content"`
}

type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var mcpTools = []MCPTool{
	{
		Name:        "list_files",
		Description: "List all pasted files and text snippets with metadata (id, name, type, size, created, expires, persistent, url). Use persistent=true to list only persistent items.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"persistent": map[string]any{
					"type":        "boolean",
					"description": "If true, list only persistent items. If false, list only non-persistent items. If omitted, list all.",
				},
			},
		},
	},
	{
		Name:        "get_file",
		Description: "Get the content of a file or text snippet by ID. Returns base64-encoded content for files, plain text for text snippets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the item",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "get_file_url",
		Description: "Get the public URL for a file or text snippet by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the item",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "upload_file",
		Description: "Upload a file (base64-encoded content + filename). Returns the item ID and URL.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type":        "string",
					"description": "Name of the file",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Base64-encoded file content",
				},
				"mime_type": map[string]any{
					"type":        "string",
					"description": "MIME type of the file (optional)",
				},
				"ttl": map[string]any{
					"type":        "string",
					"description": "Time to live: 1h, 1d, 7d, 30d, or never (default: 7d)",
				},
				"persistent": map[string]any{
					"type":        "boolean",
					"description": "If true, item will never expire (default: false)",
				},
			},
			"required": []string{"filename", "content"},
		},
	},
	{
		Name:        "create_text",
		Description: "Create a text snippet. Returns the item ID and URL.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "The text content",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Optional name for the snippet",
				},
				"ttl": map[string]any{
					"type":        "string",
					"description": "Time to live: 1h, 1d, 7d, 30d, or never (default: 7d)",
				},
				"persistent": map[string]any{
					"type":        "boolean",
					"description": "If true, item will never expire (default: false)",
				},
			},
			"required": []string{"content"},
		},
	},
	{
		Name:        "get_text",
		Description: "Get the raw text content of a text snippet by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the text snippet",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "delete_file",
		Description: "Delete a file or text snippet by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the item to delete",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "persist_file",
		Description: "Pin or unpin an item to keep it forever (persistent items never expire).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the item",
				},
				"persistent": map[string]any{
					"type":        "boolean",
					"description": "true to pin (keep forever), false to unpin (restore default TTL)",
				},
			},
			"required": []string{"id", "persistent"},
		},
	},
	{
		Name:        "describe_image",
		Description: "Get the vision analysis (extracted text and description) for an image item. Returns the stored analysis if available. Optionally specify a prompt name to get a specific analysis.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the image item",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Optional: prompt name (e.g. 'default', 'terminal', 'code', 'document', 'diagram', or a custom prompt). If omitted, returns all analyses.",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "analyze_image",
		Description: "Trigger or re-trigger vision analysis for an image item. Extracts text and generates a description using the configured vision model. Optionally specify a prompt name to use a specific prompt template.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The unique ID of the image item",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Optional: prompt name (e.g. 'default', 'terminal', 'code', 'document', 'diagram', or a custom prompt). Defaults to 'default'.",
				},
			},
			"required": []string{"id"},
		},
	},
	{
		Name:        "list_prompts",
		Description: "List all available vision prompt templates (built-in and custom).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

func mcpHandler(w http.ResponseWriter, r *http.Request) {
	// Handle SSE/streaming for MCP HTTP transport
	if r.Method == http.MethodGet {
		// Return server capabilities for SSE connection
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		// Send endpoint event
		fmt.Fprintf(w, "event: endpoint\ndata: /mcp\n\n")
		flusher.Flush()
		// Keep connection open briefly
		time.Sleep(100 * time.Millisecond)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var req MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMCPError(w, nil, -32700, "Parse error")
		return
	}

	switch req.Method {
	case "initialize":
		writeMCPResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "paste",
				"version": "1.0.0",
			},
		})

	case "tools/list":
		writeMCPResult(w, req.ID, map[string]any{
			"tools": mcpTools,
		})

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeMCPError(w, req.ID, -32602, "Invalid params")
			return
		}
		result, mcpErr := handleMCPToolCall(params.Name, params.Arguments)
		if mcpErr != nil {
			writeMCPError(w, req.ID, mcpErr.Code, mcpErr.Message)
			return
		}
		writeMCPResult(w, req.ID, result)

	case "ping":
		writeMCPResult(w, req.ID, map[string]any{})

	default:
		writeMCPError(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

func handleMCPToolCall(name string, args map[string]any) (interface{}, *MCPError) {
	switch name {
	case "list_files":
		persistent, hasPersistent := args["persistent"].(bool)
		if hasPersistent {
			return mcpListFilesFiltered(persistent)
		}
		return mcpListFiles()

	case "get_file":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		return mcpGetFile(id)

	case "get_file_url":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		return mcpGetFileURL(id)

	case "upload_file":
		filename, _ := args["filename"].(string)
		content, _ := args["content"].(string)
		mimeType, _ := args["mime_type"].(string)
		ttl, _ := args["ttl"].(string)
		persistent, _ := args["persistent"].(bool)
		if filename == "" || content == "" {
			return nil, &MCPError{Code: -32602, Message: "filename and content are required"}
		}
		return mcpUploadFile(filename, content, mimeType, ttl, persistent)

	case "create_text":
		content, _ := args["content"].(string)
		itemName, _ := args["name"].(string)
		ttl, _ := args["ttl"].(string)
		persistent, _ := args["persistent"].(bool)
		if content == "" {
			return nil, &MCPError{Code: -32602, Message: "content is required"}
		}
		return mcpCreateText(content, itemName, ttl, persistent)

	case "get_text":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		return mcpGetText(id)

	case "delete_file":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		return mcpDeleteFile(id)

	case "persist_file":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		persistent, _ := args["persistent"].(bool)
		return mcpPersistFile(id, persistent)

	case "describe_image":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		promptName, _ := args["prompt"].(string)
		return mcpDescribeImage(id, promptName)

	case "analyze_image":
		id, ok := args["id"].(string)
		if !ok || id == "" {
			return nil, &MCPError{Code: -32602, Message: "id is required"}
		}
		promptName, _ := args["prompt"].(string)
		if promptName == "" {
			promptName = "default"
		}
		return mcpAnalyzeImage(id, promptName)

	case "list_prompts":
		return mcpListPrompts()

	default:
		return nil, &MCPError{Code: -32601, Message: "Unknown tool: " + name}
	}
}

func mcpListFiles() (interface{}, *MCPError) {
	items := listItems()
	return formatItemList(items)
}

func mcpListFilesFiltered(persistent bool) (interface{}, *MCPError) {
	all := listItems()
	var items []Item
	for _, item := range all {
		if item.Persistent == persistent {
			items = append(items, item)
		}
	}
	return formatItemList(items)
}

func formatItemList(items []Item) (interface{}, *MCPError) {
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d items:", len(items)))
	for _, item := range items {
		url := fmt.Sprintf("%s/%s/%s", baseURL, itemTypePath(item.Type), item.ID)
		expiry := "never"
		if !item.Expires.IsZero() {
			expiry = item.Expires.Format("2006-01-02 15:04:05")
		}
		pinTag := ""
		if item.Persistent {
			pinTag = " [persistent]"
		}
		lines = append(lines, fmt.Sprintf("  [%s] %s (%s, %d bytes, expires: %s%s) — %s",
			item.ID, item.Name, item.Type, item.Size, expiry, pinTag, url))
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: strings.Join(lines, "\n")}},
	}, nil
}

func mcpGetFile(id string) (interface{}, *MCPError) {
	item, ok := findItem(id)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	if item.Type == "text" {
		data, err := os.ReadFile(filepath.Join(dataDir, textDir, id))
		if err != nil {
			return nil, &MCPError{Code: -32603, Message: "file not found on disk"}
		}
		return MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: string(data)}},
		}, nil
	}
	// For files, return base64
	data, err := os.ReadFile(filepath.Join(dataDir, fileDir, id))
	if err != nil {
		return nil, &MCPError{Code: -32603, Message: "file not found on disk"}
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: fmt.Sprintf("File: %s\nMIME: %s\nSize: %d bytes\nURL: %s/f/%s\nBase64 content:\n%s",
				item.Name, item.MimeType, item.Size, baseURL, id, encoded),
		}},
	}, nil
}

func mcpGetFileURL(id string) (interface{}, *MCPError) {
	item, ok := findItem(id)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	url := fmt.Sprintf("%s/%s/%s", baseURL, itemTypePath(item.Type), item.ID)
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: url}},
	}, nil
}

func mcpUploadFile(filename, content, mimeType, ttlStr string, persistent bool) (interface{}, *MCPError) {
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, &MCPError{Code: -32602, Message: "invalid base64 content"}
	}
	if int64(len(data)) > maxUploadBytes {
		return nil, &MCPError{Code: -32602, Message: fmt.Sprintf("file too large (max %d MB)", maxUploadMB)}
	}

	ttl, err := parseTTL(ttlStr)
	if err != nil {
		ttl = defaultTTL
	}

	id := genID()
	fpath := filepath.Join(dataDir, fileDir, id)
	if err := os.WriteFile(fpath, data, 0644); err != nil {
		return nil, &MCPError{Code: -32603, Message: "failed to write file"}
	}

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	var expires time.Time
	if ttl > 0 && !persistent {
		expires = time.Now().Add(ttl)
	}

	item := Item{
		ID:         id,
		Name:       filename,
		Type:       "file",
		MimeType:   mimeType,
		Size:       int64(len(data)),
		Created:    time.Now(),
		Expires:    expires,
		TTL:        ttlString(ttl),
		Persistent: persistent,
	}
	if persistent {
		item.TTL = "never"
	}
	addItem(item)

	url := fmt.Sprintf("%s/f/%s", baseURL, id)
	return MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: fmt.Sprintf("Uploaded %s (%d bytes). ID: %s, URL: %s", filename, len(data), id, url),
		}},
	}, nil
}

func mcpCreateText(content, name, ttlStr string, persistent bool) (interface{}, *MCPError) {
	ttl, err := parseTTL(ttlStr)
	if err != nil {
		ttl = defaultTTL
	}

	id := genID()
	if name == "" {
		name = fmt.Sprintf("snippet_%s.txt", time.Now().Format("20060102_150405"))
	}

	if err := os.WriteFile(filepath.Join(dataDir, textDir, id), []byte(content), 0644); err != nil {
		return nil, &MCPError{Code: -32603, Message: "failed to save text"}
	}

	var expires time.Time
	if ttl > 0 && !persistent {
		expires = time.Now().Add(ttl)
	}

	item := Item{
		ID:         id,
		Name:       name,
		Type:       "text",
		Size:       int64(len(content)),
		Created:    time.Now(),
		Expires:    expires,
		TTL:        ttlString(ttl),
		Persistent: persistent,
	}
	if persistent {
		item.TTL = "never"
	}
	addItem(item)

	url := fmt.Sprintf("%s/t/%s", baseURL, id)
	return MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: fmt.Sprintf("Created text snippet '%s' (%d chars). ID: %s, URL: %s", name, len(content), id, url),
		}},
	}, nil
}

func mcpGetText(id string) (interface{}, *MCPError) {
	item, ok := findItem(id)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	if item.Type != "text" {
		return nil, &MCPError{Code: -32602, Message: "item is not a text snippet"}
	}
	data, err := os.ReadFile(filepath.Join(dataDir, textDir, id))
	if err != nil {
		return nil, &MCPError{Code: -32603, Message: "file not found on disk"}
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: string(data)}},
	}, nil
}

func mcpDeleteFile(id string) (interface{}, *MCPError) {
	if _, ok := findItem(id); !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	deleteItem(id)
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("Deleted item %s", id)}},
	}, nil
}

func mcpPersistFile(id string, persistent bool) (interface{}, *MCPError) {
	if _, ok := findItem(id); !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	updateItem(id, func(item *Item) bool {
		item.Persistent = persistent
		if persistent {
			item.Expires = time.Time{}
			item.TTL = "never"
		} else {
			item.Expires = time.Now().Add(defaultTTL)
			item.TTL = ttlString(defaultTTL)
		}
		return true
	})
	action := "pinned"
	if !persistent {
		action = "unpinned"
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s item %s", action, id)}},
	}, nil
}

func mcpDescribeImage(id, promptName string) (interface{}, *MCPError) {
	item, ok := findItem(id)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	if item.Type != "file" || !strings.HasPrefix(item.MimeType, "image/") {
		return nil, &MCPError{Code: -32602, Message: "item is not an image"}
	}
	if len(item.Analyses) == 0 {
		return MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("No vision analyses available for image %s. Use analyze_image to trigger analysis.", id)}},
		}, nil
	}

	// If a specific prompt is requested, return just that analysis
	if promptName != "" {
		a, exists := item.Analyses[promptName]
		if !exists {
			return MCPToolResult{
				Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("No analysis with prompt %q for image %s. Available: %s", promptName, id, strings.Join(analysisKeys(item.Analyses), ", "))}},
			}, nil
		}
		return MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: formatAnalysis(id, item.Name, promptName, a)}},
		}, nil
	}

	// Return all analyses
	var lines []string
	lines = append(lines, fmt.Sprintf("Vision analyses for %s (%s):", id, item.Name))
	for _, pn := range analysisKeysSorted(item.Analyses) {
		a := item.Analyses[pn]
		lines = append(lines, "")
		lines = append(lines, formatAnalysis(id, item.Name, pn, a))
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: strings.Join(lines, "\n")}},
	}, nil
}

func mcpAnalyzeImage(id, promptName string) (interface{}, *MCPError) {
	item, ok := findItem(id)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: "item not found"}
	}
	if item.Type != "file" || !strings.HasPrefix(item.MimeType, "image/") {
		return nil, &MCPError{Code: -32602, Message: "item is not an image"}
	}
	if !visionEnabled {
		return nil, &MCPError{Code: -32603, Message: "vision processing is disabled"}
	}
	prompt, ok := getPrompt(promptName)
	if !ok {
		return nil, &MCPError{Code: -32602, Message: fmt.Sprintf("prompt %q not found. Use list_prompts to see available prompts.", promptName)}
	}
	result, err := analyzeImage(id, prompt.Prompt)
	if err != nil {
		return nil, &MCPError{Code: -32603, Message: "analysis failed: " + err.Error()}
	}
	updateItem(id, func(it *Item) bool {
		if it.Analyses == nil {
			it.Analyses = make(map[string]*ItemAnalysis)
		}
		it.Analyses[promptName] = &ItemAnalysis{
			Status:      "complete",
			Text:        result.Text,
			Description: result.Description,
			Backend:     visionModel,
			PromptName:  promptName,
			ProcessedAt: time.Now(),
		}
		return true
	})
	var lines []string
	lines = append(lines, fmt.Sprintf("Analysis complete for %s [%s]:", id, promptName))
	if result.Description != "" {
		lines = append(lines, fmt.Sprintf("  Description: %s", result.Description))
	}
	if result.Text != "" {
		lines = append(lines, "  Extracted text:")
		lines = append(lines, result.Text)
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: strings.Join(lines, "\n")}},
	}, nil
}

func mcpListPrompts() (interface{}, *MCPError) {
	list := listPromptsSorted()
	var lines []string
	lines = append(lines, fmt.Sprintf("Available vision prompts (%d):", len(list)))
	for _, p := range list {
		tag := ""
		if p.BuiltIn {
			tag = " [built-in]"
		}
		lines = append(lines, fmt.Sprintf("  %s%s: %s", p.Name, tag, p.Description))
	}
	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: strings.Join(lines, "\n")}},
	}, nil
}

// formatAnalysis formats a single analysis for MCP text output
func formatAnalysis(id, name, promptName string, a *ItemAnalysis) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("[%s] %s (%s):", promptName, id, name))
	lines = append(lines, fmt.Sprintf("  Status: %s", a.Status))
	lines = append(lines, fmt.Sprintf("  Backend: %s", a.Backend))
	if a.Description != "" {
		lines = append(lines, fmt.Sprintf("  Description: %s", a.Description))
	}
	if a.Text != "" {
		lines = append(lines, "  Extracted text:")
		lines = append(lines, a.Text)
	}
	if a.Error != "" {
		lines = append(lines, fmt.Sprintf("  Error: %s", a.Error))
	}
	return strings.Join(lines, "\n")
}

// analysisKeys returns unsorted keys from an analyses map
func analysisKeys(m map[string]*ItemAnalysis) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// analysisKeysSorted returns sorted keys from an analyses map
func analysisKeysSorted(m map[string]*ItemAnalysis) []string {
	keys := analysisKeys(m)
	sort.Strings(keys)
	return keys
}

func writeMCPResult(w http.ResponseWriter, id interface{}, result interface{}) {
	json.NewEncoder(w).Encode(MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeMCPError(w http.ResponseWriter, id interface{}, code int, msg string) {
	json.NewEncoder(w).Encode(MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: msg},
	})
}
