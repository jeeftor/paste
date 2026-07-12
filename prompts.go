package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// VisionPrompt represents a configurable prompt template for vision analysis
type VisionPrompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	BuiltIn     bool   `json:"built_in"`
}

var (
	prompts     map[string]*VisionPrompt
	promptsMu   sync.RWMutex
	promptsFile string
)

const promptsFileName = "prompts.json"

// Built-in prompt templates
var builtinPrompts = []VisionPrompt{
	{
		Name:        "default",
		Description: "General-purpose image analysis with OCR and description",
		BuiltIn:     true,
		Prompt: `Analyze this image and extract its content. Return a JSON object with this exact structure:
{
  "image_type": "terminal|code|screenshot|document|diagram|photo|other",
  "text": "all visible text extracted verbatim, preserving structure and line breaks",
  "description": "brief 1-2 sentence description of what the image shows"
}
For terminal screenshots: extract all text including commands, output, and errors.
For code screenshots: extract the code preserving indentation, identify the language.
For documents: OCR the text preserving layout.
For diagrams/photos: describe what's shown and extract any visible text.
Return ONLY the JSON object, no other text.`,
	},
	{
		Name:        "terminal",
		Description: "Terminal screenshot OCR — extracts commands, output, errors with structure",
		BuiltIn:     true,
		Prompt: `This is a terminal screenshot. Extract the visible text with these requirements:
1. Extract ALL text exactly as it appears — commands, output, errors, paths
2. Preserve command/output structure and line breaks
3. Distinguish between user commands and system output
4. Keep the prompt character ($, >, #) at the start of command lines

Return a JSON object:
{
  "image_type": "terminal",
  "text": "the complete terminal output, preserving line breaks and structure",
  "description": "brief description of what the terminal shows"
}
Return ONLY the JSON object, no other text.`,
	},
	{
		Name:        "code",
		Description: "Code screenshot extraction — preserves indentation, identifies language",
		BuiltIn:     true,
		Prompt: `This is a code screenshot. Extract the source code with these requirements:
1. Preserve exact indentation, whitespace, comments, string literals
2. Identify the programming language based on syntax
3. Exclude line numbers if visible
4. If multiple code blocks are shown, extract each one

Return a JSON object:
{
  "image_type": "code",
  "text": "the extracted code, preserving indentation and structure",
  "description": "brief description including the detected programming language"
}
Return ONLY the JSON object, no other text.`,
	},
	{
		Name:        "document",
		Description: "Document/receipt OCR — structured extraction with layout preservation",
		BuiltIn:     true,
		Prompt: `Extract structured data from this document image.

Return a JSON object:
{
  "image_type": "document",
  "text": "complete OCR text preserving layout and reading order",
  "description": "brief description of the document type and key information"
}
For receipts: focus on merchant, date, items, totals.
For invoices: include invoice number, due date, vendor.
For forms: extract all filled fields and their values.
Return ONLY the JSON object, no other text.`,
	},
	{
		Name:        "diagram",
		Description: "Diagram/chart analysis — describes structure, connections, flow",
		BuiltIn:     true,
		Prompt: `Analyze this diagram or chart and provide a comprehensive textual description.

Return a JSON object:
{
  "image_type": "diagram",
  "text": "all readable text extracted verbatim from the diagram",
  "description": "detailed description of the diagram structure, connections, flow direction, and relationships between elements"
}
For flowcharts: describe the flow and decision points.
For architecture diagrams: describe components and their connections.
For charts: describe the data, axes, and trends.
Return ONLY the JSON object, no other text.`,
	},
}

// initPrompts loads prompts from disk or initializes with built-in defaults
func initPrompts() {
	promptsFile = filepath.Join(dataDir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)

	// Load from disk
	if data, err := os.ReadFile(promptsFile); err == nil {
		var loaded map[string]*VisionPrompt
		if err := json.Unmarshal(data, &loaded); err == nil {
			prompts = loaded
		}
	}

	// Ensure all built-in prompts exist (don't overwrite user modifications)
	for _, bp := range builtinPrompts {
		if _, exists := prompts[bp.Name]; !exists {
			p := bp // copy
			prompts[bp.Name] = &p
		}
	}

	savePrompts()
}

func savePrompts() {
	data, err := json.MarshalIndent(prompts, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(promptsFile, data, 0644)
}

func getPrompt(name string) (*VisionPrompt, bool) {
	promptsMu.RLock()
	defer promptsMu.RUnlock()
	p, ok := prompts[name]
	return p, ok
}

func listPromptsSorted() []*VisionPrompt {
	promptsMu.RLock()
	defer promptsMu.RUnlock()
	list := make([]*VisionPrompt, 0, len(prompts))
	for _, p := range prompts {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		// Built-ins first, then alphabetical
		if list[i].BuiltIn != list[j].BuiltIn {
			return list[i].BuiltIn
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// --- REST API handlers ---

func apiPromptsHandler(w http.ResponseWriter, r *http.Request) {
	// GET /api/prompts — list all
	// POST /api/prompts — create new
	switch r.Method {
	case http.MethodGet:
		list := listPromptsSorted()
		writeJSON(w, map[string]interface{}{"prompts": list})

	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Prompt      string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if req.Name == "" || req.Prompt == "" {
			http.Error(w, `{"error":"name and prompt are required"}`, http.StatusBadRequest)
			return
		}

		promptsMu.Lock()
		defer promptsMu.Unlock()

		if _, exists := prompts[req.Name]; exists {
			http.Error(w, `{"error":"prompt already exists"}`, http.StatusConflict)
			return
		}

		p := &VisionPrompt{
			Name:        req.Name,
			Description: req.Description,
			Prompt:      req.Prompt,
			BuiltIn:     false,
		}
		prompts[req.Name] = p
		savePrompts()
		writeJSON(w, p)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func apiPromptHandler(w http.ResponseWriter, r *http.Request) {
	// /api/prompts/{name}
	// GET — get one
	// PUT — update
	// DELETE — delete (built-ins can't be deleted)
	name := strings.TrimPrefix(r.URL.Path, "/api/prompts/")
	if name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		promptsMu.RLock()
		defer promptsMu.RUnlock()
		p, ok := prompts[name]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, p)

	case http.MethodPut:
		var req struct {
			Description string `json:"description"`
			Prompt      string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		promptsMu.Lock()
		defer promptsMu.Unlock()
		p, ok := prompts[name]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		if req.Description != "" {
			p.Description = req.Description
		}
		if req.Prompt != "" {
			p.Prompt = req.Prompt
		}
		savePrompts()
		writeJSON(w, p)

	case http.MethodDelete:
		promptsMu.Lock()
		defer promptsMu.Unlock()
		p, ok := prompts[name]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		if p.BuiltIn {
			http.Error(w, `{"error":"cannot delete built-in prompt"}`, http.StatusBadRequest)
			return
		}
		delete(prompts, name)
		savePrompts()
		writeJSON(w, map[string]interface{}{"deleted": name})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}
