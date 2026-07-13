package app

import (
	"encoding/json"
	"net/http"
)

// openapiSpecHandler serves an OpenAPI 3.0 spec for all REST endpoints
func openapiSpecHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	spec := buildOpenAPISpec()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(spec)
}

func buildOpenAPISpec() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "Klipbord API",
			"description": "File drop and paste service with vision LLM integration",
			"version":     version,
		},
		"servers": []map[string]interface{}{
			{"url": baseURL, "description": "Current server"},
		},
		"paths": map[string]interface{}{
			"/api/files": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "List all items",
					"parameters": []map[string]interface{}{
						{"name": "persistent", "in": "query", "schema": map[string]interface{}{"type": "boolean"}, "description": "Filter by persistent state"},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "List of items", "content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "array", "items": map[string]interface{}{"$ref": "#/components/schemas/Item"}}}}},
					},
				},
			},
			"/api/files/{id}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "Download a file or get text content",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "File content or item metadata"}},
				},
				"delete": map[string]interface{}{
					"summary": "Delete an item",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Item deleted"}},
				},
				"patch": map[string]interface{}{
					"summary": "Pin/unpin item (persist forever)",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"persistent": map[string]interface{}{"type": "boolean"}}}}}},
					"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "Item updated"}},
				},
			},
			"/api/upload": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Upload a file (multipart, <5MB recommended)",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"multipart/form-data": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"file": map[string]interface{}{"type": "string", "format": "binary"},
						"ttl":  map[string]interface{}{"type": "string", "description": "1h, 1d, 7d, 30d, never"},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Upload successful"}},
				},
			},
			"/api/upload/init": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Initialize chunked upload",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"filename":  map[string]interface{}{"type": "string"},
						"size":      map[string]interface{}{"type": "integer"},
						"mime_type": map[string]interface{}{"type": "string"},
						"ttl":       map[string]interface{}{"type": "string"},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Upload initialized with upload_id"}},
				},
			},
			"/api/upload/chunk": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Upload a chunk (multipart: upload_id, chunk_index, chunk)",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"multipart/form-data": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"upload_id":   map[string]interface{}{"type": "string"},
						"chunk_index": map[string]interface{}{"type": "integer"},
						"chunk":       map[string]interface{}{"type": "string", "format": "binary"},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Chunk received"}},
				},
			},
			"/api/upload/complete": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Finalize chunked upload (reassembles file)",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"upload_id": map[string]interface{}{"type": "string"}}}}}},
					"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "Upload complete"}},
				},
			},
			"/api/upload/status/{upload_id}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "Check which chunks have been received",
					"parameters": []map[string]interface{}{
						{"name": "upload_id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Chunk status"}},
				},
			},
			"/api/text": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Create a text snippet",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"content":    map[string]interface{}{"type": "string"},
						"name":       map[string]interface{}{"type": "string"},
						"ttl":        map[string]interface{}{"type": "string"},
						"persistent": map[string]interface{}{"type": "boolean"},
					}, "required": []string{"content"}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Text snippet created"}},
				},
			},
			"/api/text/{id}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "Get raw text snippet",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Raw text content", "content": map[string]interface{}{"text/plain": map[string]interface{}{"schema": map[string]interface{}{"type": "string"}}}}},
				},
			},
			"/link/{id}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "Open a stored file, image, or text snippet",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Stored item content"}},
				},
			},
			"/api/analyze/{id}": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Trigger vision analysis on an image",
					"parameters": []map[string]interface{}{
						{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
						{"name": "prompt", "in": "query", "schema": map[string]interface{}{"type": "string"}, "description": "Prompt name (default, terminal, code, document, diagram, screenshot, or custom)"},
					},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Analysis result"}},
				},
			},
			"/api/prompts": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":   "List all vision prompts (built-in and custom)",
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "List of prompts"}},
				},
				"post": map[string]interface{}{
					"summary": "Create a custom vision prompt",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"name":        map[string]interface{}{"type": "string"},
						"description": map[string]interface{}{"type": "string"},
						"prompt":      map[string]interface{}{"type": "string"},
					}, "required": []string{"name", "prompt"}}}}},
					"responses": map[string]interface{}{"201": map[string]interface{}{"description": "Prompt created"}},
				},
			},
			"/api/prompts/{name}": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":    "Get a specific prompt",
					"parameters": []map[string]interface{}{{"name": "name", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}},
					"responses":  map[string]interface{}{"200": map[string]interface{}{"description": "Prompt details"}},
				},
				"put": map[string]interface{}{
					"summary":    "Update a prompt (built-in or custom)",
					"parameters": []map[string]interface{}{{"name": "name", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}},
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"description": map[string]interface{}{"type": "string"},
						"prompt":      map[string]interface{}{"type": "string"},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Prompt updated"}},
				},
				"delete": map[string]interface{}{
					"summary":    "Delete a custom prompt (built-ins cannot be deleted)",
					"parameters": []map[string]interface{}{{"name": "name", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}}},
					"responses":  map[string]interface{}{"200": map[string]interface{}{"description": "Prompt deleted"}},
				},
			},
			"/api/config/vision": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":   "Get vision configuration (presets, active, enabled)",
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Vision config"}},
				},
			},
			"/api/config/vision/active": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Set active vision preset",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"preset": map[string]interface{}{"type": "string"}}}}}},
					"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "Active preset changed"}},
				},
			},
			"/api/config/vision/enabled": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Enable/disable vision processing",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"enabled": map[string]interface{}{"type": "boolean"}}}}}},
					"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "Vision enabled toggled"}},
				},
			},
			"/api/config/vision/presets": map[string]interface{}{
				"get":  map[string]interface{}{"summary": "List all presets", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "List of presets"}}},
				"post": map[string]interface{}{"summary": "Add a preset", "responses": map[string]interface{}{"201": map[string]interface{}{"description": "Preset added"}}},
			},
			"/api/config/vision/presets/{name}": map[string]interface{}{
				"put":    map[string]interface{}{"summary": "Update a preset", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Preset updated"}}},
				"delete": map[string]interface{}{"summary": "Delete a preset", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Preset deleted"}}},
			},
			"/api/config/vision/test": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Test connectivity to a vision preset (text-only, no image)",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"preset": map[string]interface{}{"type": "string"}}}}}},
					"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "Test result"}},
				},
			},
			"/api/vision/test": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Run full vision pipeline on a built-in sample image",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"image_type": map[string]interface{}{"type": "string", "description": "terminal (default), code, document, diagram, screenshot"},
						"prompt": map[string]interface{}{"type": "string", "description": "Prompt name override (default: matches image_type)"}}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Vision analysis result with extracted text"}},
				},
			},
			"/api/vision/compare": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Run an image through all presets, compare results, and rank by quality",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"image_type": map[string]interface{}{"type": "string", "description": "Sample image type: terminal, code, document, diagram, screenshot. If omitted, requires item_id."},
						"item_id":    map[string]interface{}{"type": "string", "description": "Existing image item ID to analyze. If omitted, uses sample image_type."},
						"prompt":     map[string]interface{}{"type": "string", "description": "Prompt name to use (default: 'default')"},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Ranked comparison results from all presets"}},
				},
			},
			"/api/vision/compare-prompts": map[string]interface{}{
				"post": map[string]interface{}{
					"summary": "Run one image through ALL prompts, compare and rank by quality",
					"requestBody": map[string]interface{}{"content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"image_type": map[string]interface{}{"type": "string", "description": "Sample image type: terminal (default), code, document, diagram, screenshot. Ignored if item_id is provided."},
						"item_id":    map[string]interface{}{"type": "string", "description": "Existing image item ID to test. If omitted, uses a sample image."},
					}}}}},
					"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Ranked prompt comparison results"}},
				},
			},
			"/api/vision/runtime": map[string]interface{}{
				"get": map[string]interface{}{"summary": "Get configured vision providers' loaded-model and resource status", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Runtime status by vision preset"}}},
			},
			"/health": map[string]interface{}{
				"get": map[string]interface{}{"summary": "Health check", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Server healthy"}}},
			},
			"/api/version": map[string]interface{}{
				"get": map[string]interface{}{"summary": "Get server version", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Version info"}}},
			},
			"/api/update-check": map[string]interface{}{
				"get": map[string]interface{}{"summary": "Check whether a newer Klipbord release is available", "responses": map[string]interface{}{"200": map[string]interface{}{"description": "Current and latest release versions"}}},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"Item": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string"},
						"name":       map[string]interface{}{"type": "string"},
						"type":       map[string]interface{}{"type": "string", "description": "file or text"},
						"mime_type":  map[string]interface{}{"type": "string"},
						"size":       map[string]interface{}{"type": "integer"},
						"created":    map[string]interface{}{"type": "string", "format": "date-time"},
						"expires":    map[string]interface{}{"type": "string", "format": "date-time"},
						"persistent": map[string]interface{}{"type": "boolean"},
						"url":        map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
}
