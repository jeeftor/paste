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
// Optimized for Qwen3-VL-4B-Instruct-GGUF running via llama.cpp/Lemonade:
// - JSON schema comes first so the model anchors to the output format immediately
// - Direct imperative language (small models respond better than hedged phrasing)
// - /no_think at the end disables Qwen3 thinking-mode tokens that break JSON parsing
var builtinPrompts = []VisionPrompt{
	{
		Name:        "default",
		Description: "General-purpose image analysis with OCR and description",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "terminal|code|screenshot|document|diagram|photo|other",
  "text": "all visible text extracted verbatim, preserving structure and line breaks",
  "description": "brief 1-2 sentence description of what the image shows"
}

Look at the image and fill in the JSON:
- terminal: extract every command, output line, and error — keep prompt characters ($, >, #) and all line breaks
- code: extract source code with EXACT indentation; note the programming language in description
- screenshot: extract all visible text — URL bar, buttons, menus, headings, body text
- document: OCR all text in reading order preserving layout
- diagram/chart: extract all labels verbatim; describe structure, connections, and flow in description
- photo: describe what is shown; extract any visible text

Output the JSON only. /no_think`,
	},
	{
		Name:        "terminal",
		Description: "Terminal screenshot OCR — extracts commands, output, errors with structure",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "terminal",
  "text": "complete terminal session text with ALL line breaks and prompt characters preserved",
  "description": "brief description of what the terminal session shows"
}

This is a terminal screenshot. Extract ALL visible text:
- Keep every prompt character ($, >, #, %) at the start of command lines
- Preserve exact line breaks — do not merge or skip any lines
- Include every command, output line, error, warning, and file path verbatim
- Do not summarize, paraphrase, or omit anything

Output the JSON only. /no_think`,
	},
	{
		Name:        "code",
		Description: "Code screenshot extraction — preserves indentation, identifies language",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "code",
  "text": "extracted source code with exact indentation and whitespace preserved",
  "description": "brief description including the detected programming language"
}

This is a code screenshot. Extract the source code:
- Preserve EXACT indentation — tabs and spaces both matter
- Keep all comments, string literals, operators, and punctuation verbatim
- Exclude line numbers if they appear in a gutter
- Identify the programming language from syntax and include it in description

Output the JSON only. /no_think`,
	},
	{
		Name:        "document",
		Description: "Document/receipt OCR — structured extraction with layout preservation",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "document",
  "text": "complete OCR text in reading order, preserving layout",
  "description": "brief description of document type and key information"
}

Extract all text from this document image:
- OCR every word in reading order (top to bottom, left to right)
- For receipts: include merchant name, date, every line item, subtotal, tax, and total
- For invoices: include invoice number, vendor, due date, and all amounts
- For forms: extract every field label and its filled-in value

Output the JSON only. /no_think`,
	},
	{
		Name:        "diagram",
		Description: "Diagram/chart analysis — describes structure, connections, flow",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "diagram",
  "text": "all text labels and annotations extracted verbatim from the diagram",
  "description": "detailed description of structure, connections, flow direction, and relationships between elements"
}

Analyze this diagram or chart:
- Extract ALL text labels, node names, and annotations verbatim into text
- Describe every connection, arrow, and relationship in description
- For flowcharts: describe decision points and flow direction
- For architecture diagrams: list every component and how they connect
- For charts/graphs: describe axes, data series, values, and trends

Output the JSON only. /no_think`,
	},
	{
		Name:        "screenshot",
		Description: "Desktop/browser screenshot — extracts UI elements, page content, URLs, menus",
		BuiltIn:     true,
		Prompt: `Output ONLY this JSON object, nothing else:
{
  "image_type": "screenshot",
  "text": "all visible text — URLs, titles, buttons, labels, menus, headings, body text",
  "description": "brief description of the application, page title, and key UI elements visible"
}

This is a desktop or browser screenshot. Extract ALL visible content:
- Identify the application (browser, IDE, terminal, file manager, etc.)
- Copy the URL bar or window title exactly as shown
- Include every menu item, button label, tab name, and navigation element
- Extract all body text, headings, and content visible on screen
- Note any dialogs, popups, notifications, or error messages

Output the JSON only. /no_think`,
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
