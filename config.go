package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// VisionPreset is a named LLM endpoint configuration
type VisionPreset struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key,omitempty"`
	Description string `json:"description,omitempty"`
}

// VisionConfigFile is the on-disk format for vision configuration
type VisionConfigFile struct {
	Presets       map[string]*VisionPreset `json:"presets"`
	ActivePreset  string                   `json:"active_preset"`
	Enabled       bool                     `json:"enabled"`
}

var (
	visionConfigMu     sync.RWMutex
	visionConfig       VisionConfigFile
	visionConfigFile   string
	visionEnvOverridden bool // true if env vars are overriding config
)

const visionConfigFileName = "vision_config.json"

// defaultVisionPresets are the built-in presets created on first boot
var defaultVisionPresets = map[string]*VisionPreset{
	"lemonade": {
		Name:        "lemonade",
		Endpoint:    "http://localhost:13305/v1/chat/completions",
		Model:       "Qwen3-VL-4B-Instruct-GGUF",
		Description: "Local Lemonade (GPU)",
	},
	"ollama": {
		Name:        "ollama",
		Endpoint:    "http://localhost:11434/v1/chat/completions",
		Model:       "llama3.2-vision",
		Description: "Local Ollama",
	},
	"openai": {
		Name:        "openai",
		Endpoint:    "https://api.openai.com/v1/chat/completions",
		Model:       "gpt-4o-mini",
		APIKey:      "",
		Description: "OpenAI cloud (requires API key)",
	},
}

// initVisionConfig loads the vision config from disk, applying env var overrides
func initVisionConfig() {
	visionConfigFile = filepath.Join(dataDir, visionConfigFileName)
	visionConfigMu.Lock()
	defer visionConfigMu.Unlock()

	// Load from disk
	visionConfig = VisionConfigFile{
		Presets:      make(map[string]*VisionPreset),
		ActivePreset: "",
	}

	data, err := os.ReadFile(visionConfigFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: failed to read vision config: %v", err)
		}
		// First boot — populate with defaults
		for k, v := range defaultVisionPresets {
			visionConfig.Presets[k] = &VisionPreset{
				Name:        v.Name,
				Endpoint:    v.Endpoint,
				Model:       v.Model,
				APIKey:      v.APIKey,
				Description: v.Description,
			}
		}
		visionConfig.ActivePreset = "lemonade"
		saveVisionConfigLocked()
	} else {
		if err := json.Unmarshal(data, &visionConfig); err != nil {
			log.Printf("Warning: failed to parse vision config: %v", err)
			for k, v := range defaultVisionPresets {
				visionConfig.Presets[k] = v
			}
			visionConfig.ActivePreset = "lemonade"
		}
		// Merge in any new default presets that don't exist yet
		for k, v := range defaultVisionPresets {
			if _, exists := visionConfig.Presets[k]; !exists {
				visionConfig.Presets[k] = v
			}
		}
	}

	// Apply env var overrides (env vars always win)
	visionEnvOverridden = false
	envEndpoint := os.Getenv("VISION_ENDPOINT")
	envModel := os.Getenv("VISION_MODEL")
	envEnabled := os.Getenv("VISION_ENABLED")

	if envEndpoint != "" || envModel != "" {
		visionEnvOverridden = true
		// Create/update an "env" preset to reflect the env var values
		active := getActivePresetLocked()
		endpoint := envEndpoint
		if endpoint == "" {
			endpoint = active.Endpoint
		}
		model := envModel
		if model == "" {
			model = active.Model
		}
		visionConfig.Presets["env"] = &VisionPreset{
			Name:        "env",
			Endpoint:    endpoint,
			Model:       model,
			Description: "Set by environment variables (overrides UI config)",
		}
		visionConfig.ActivePreset = "env"
	}

	// Set global vars from active config
	applyActiveConfigLocked()

	// Load enabled state from config file, then override with env var if set
	visionEnabled = visionConfig.Enabled
	if envEnabled != "" {
		visionEnabled = envEnabled == "true"
	}
}

// applyActiveConfigLocked sets the global vision vars from the active preset.
// Must be called with visionConfigMu held.
func applyActiveConfigLocked() {
	p := getActivePresetLocked()
	visionEndpoint = p.Endpoint
	visionModel = p.Model
}

// getActivePresetLocked returns the active preset, or the first available.
// Must be called with visionConfigMu held.
func getActivePresetLocked() *VisionPreset {
	if p, ok := visionConfig.Presets[visionConfig.ActivePreset]; ok {
		return p
	}
	// Fall back to first preset
	for _, p := range visionConfig.Presets {
		return p
	}
	// No presets at all — return defaults
	return defaultVisionPresets["lemonade"]
}

// saveVisionConfigLocked writes the config to disk. Must be called with visionConfigMu held.
func saveVisionConfigLocked() {
	data, err := json.MarshalIndent(visionConfig, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal vision config: %v", err)
		return
	}
	if err := os.WriteFile(visionConfigFile, data, 0644); err != nil {
		log.Printf("Failed to write vision config: %v", err)
	}
}

// --- Public API (thread-safe) ---

// getActiveVisionPreset returns the currently active preset
func getActiveVisionPreset() *VisionPreset {
	visionConfigMu.RLock()
	defer visionConfigMu.RUnlock()
	return getActivePresetLocked()
}

// listVisionPresets returns all presets sorted by name
func listVisionPresets() []*VisionPreset {
	visionConfigMu.RLock()
	defer visionConfigMu.RUnlock()
	result := make([]*VisionPreset, 0, len(visionConfig.Presets))
	for _, p := range visionConfig.Presets {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		// "env" preset always first, then alphabetical
		if result[i].Name == "env" {
			return true
		}
		if result[j].Name == "env" {
			return false
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// getVisionPreset returns a specific preset by name
func getVisionPreset(name string) (*VisionPreset, bool) {
	visionConfigMu.RLock()
	defer visionConfigMu.RUnlock()
	p, ok := visionConfig.Presets[name]
	return p, ok
}

// addVisionPreset adds a new preset. Returns false if name already exists.
func addVisionPreset(p *VisionPreset) bool {
	visionConfigMu.Lock()
	defer visionConfigMu.Unlock()
	if _, exists := visionConfig.Presets[p.Name]; exists {
		return false
	}
	visionConfig.Presets[p.Name] = p
	saveVisionConfigLocked()
	return true
}

// updateVisionPreset updates an existing preset. Returns false if not found.
func updateVisionPreset(name string, endpoint, model, apiKey, description string) bool {
	visionConfigMu.Lock()
	defer visionConfigMu.Unlock()
	p, ok := visionConfig.Presets[name]
	if !ok {
		return false
	}
	if endpoint != "" {
		p.Endpoint = endpoint
	}
	if model != "" {
		p.Model = model
	}
	if apiKey != "" {
		p.APIKey = apiKey
	}
	if description != "" {
		p.Description = description
	}
	saveVisionConfigLocked()
	// If this is the active preset, reapply
	if visionConfig.ActivePreset == name {
		applyActiveConfigLocked()
	}
	return true
}

// deleteVisionPreset removes a preset. Returns false if not found or if it's the active preset.
func deleteVisionPreset(name string) error {
	visionConfigMu.Lock()
	defer visionConfigMu.Unlock()
	if _, ok := visionConfig.Presets[name]; !ok {
		return errNotFound
	}
	if name == "env" {
		return errCannotDeleteEnv
	}
	if name == visionConfig.ActivePreset {
		return errCannotDeleteActive
	}
	delete(visionConfig.Presets, name)
	saveVisionConfigLocked()
	return nil
}

// setActiveVisionPreset switches the active preset. Returns false if not found.
func setActiveVisionPreset(name string) bool {
	visionConfigMu.Lock()
	defer visionConfigMu.Unlock()
	if _, ok := visionConfig.Presets[name]; !ok {
		return false
	}
	visionConfig.ActivePreset = name
	saveVisionConfigLocked()
	applyActiveConfigLocked()
	return true
}

// isEnvOverridden returns true if env vars are overriding the UI config
func isEnvOverridden() bool {
	visionConfigMu.RLock()
	defer visionConfigMu.RUnlock()
	return visionEnvOverridden
}

// --- Errors ---

type configError string

func (e configError) Error() string { return string(e) }

const (
	errNotFound          = configError("preset not found")
	errCannotDeleteActive = configError("cannot delete the active preset")
	errCannotDeleteEnv    = configError("cannot delete the env preset")
)

// --- REST API handlers ---

// apiVisionConfigHandler handles GET /api/config/vision and POST/PUT/DELETE /api/config/vision/presets
func apiVisionConfigHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/config/vision")

	if path == "" || path == "/" {
		// GET /api/config/vision — return current config
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		visionConfigMu.RLock()
		defer visionConfigMu.RUnlock()
		active := getActivePresetLocked()
		writeJSON(w, map[string]interface{}{
			"active_preset":  visionConfig.ActivePreset,
			"active":         active,
			"presets":        listVisionPresets(),
			"enabled":        visionEnabled,
			"env_overridden": visionEnvOverridden,
		})
		return
	}

	// Handle /api/config/vision/active
	if path == "/active" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Preset string `json:"preset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if req.Preset == "" {
			http.Error(w, `{"error":"preset field required"}`, http.StatusBadRequest)
			return
		}
		if visionEnvOverridden {
			http.Error(w, `{"error":"cannot change active preset — env vars are overriding config"}`, http.StatusConflict)
			return
		}
		if !setActiveVisionPreset(req.Preset) {
			http.Error(w, fmt.Sprintf(`{"error":"preset %q not found"}`, req.Preset), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]interface{}{
			"active_preset": req.Preset,
			"active":        getActiveVisionPreset(),
		})
		return
	}

	// Handle /api/config/vision/enabled
	if path == "/enabled" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if req.Enabled == nil {
			http.Error(w, `{"error":"enabled field required"}`, http.StatusBadRequest)
			return
		}
		if os.Getenv("VISION_ENABLED") != "" {
			http.Error(w, `{"error":"cannot change enabled state — VISION_ENABLED env var is set"}`, http.StatusConflict)
			return
		}
		visionConfigMu.Lock()
		visionEnabled = *req.Enabled
		visionConfig.Enabled = *req.Enabled
		saveVisionConfigLocked()
		visionConfigMu.Unlock()
		writeJSON(w, map[string]interface{}{
			"enabled": *req.Enabled,
		})
		return
	}

	// Handle /api/config/vision/test
	if path == "/test" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Preset string `json:"preset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		var preset *VisionPreset
		if req.Preset == "" {
			preset = getActiveVisionPreset()
		} else {
			p, ok := getVisionPreset(req.Preset)
			if !ok {
				http.Error(w, fmt.Sprintf(`{"error":"preset %q not found"}`, req.Preset), http.StatusNotFound)
				return
			}
			preset = p
		}
		result := testVisionPreset(preset)
		writeJSON(w, result)
		return
	}

	// Handle /api/config/vision/presets and /api/config/vision/presets/{name}
	if strings.HasPrefix(path, "/presets") {
		presetPath := strings.TrimPrefix(path, "/presets")
		if presetPath == "" || presetPath == "/" {
			// POST /api/config/vision/presets — create
			if r.Method != http.MethodPost {
				http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
				return
			}
			var req VisionPreset
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
				return
			}
			if req.Name == "" {
				http.Error(w, `{"error":"name field required"}`, http.StatusBadRequest)
				return
			}
			if req.Endpoint == "" {
				http.Error(w, `{"error":"endpoint field required"}`, http.StatusBadRequest)
				return
			}
			if req.Model == "" {
				http.Error(w, `{"error":"model field required"}`, http.StatusBadRequest)
				return
			}
			if !addVisionPreset(&req) {
				http.Error(w, fmt.Sprintf(`{"error":"preset %q already exists"}`, req.Name), http.StatusConflict)
				return
			}
			writeJSON(w, req)
			return
		}

		// /api/config/vision/presets/{name}
		name := strings.TrimPrefix(presetPath, "/")
		if name == "" {
			http.Error(w, `{"error":"preset name required"}`, http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodGet {
			p, ok := getVisionPreset(name)
			if !ok {
				http.Error(w, `{"error":"preset not found"}`, http.StatusNotFound)
				return
			}
			writeJSON(w, p)
			return
		}

		if r.Method == http.MethodPut {
			var req struct {
				Endpoint    string `json:"endpoint"`
				Model       string `json:"model"`
				APIKey      string `json:"api_key"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
				return
			}
			if name == "env" {
				http.Error(w, `{"error":"cannot edit the env preset"}`, http.StatusForbidden)
				return
			}
			if !updateVisionPreset(name, req.Endpoint, req.Model, req.APIKey, req.Description) {
				http.Error(w, `{"error":"preset not found"}`, http.StatusNotFound)
				return
			}
			p, _ := getVisionPreset(name)
			writeJSON(w, p)
			return
		}

		if r.Method == http.MethodDelete {
			if err := deleteVisionPreset(name); err != nil {
				if err == errNotFound {
					http.Error(w, `{"error":"preset not found"}`, http.StatusNotFound)
				} else if err == errCannotDeleteActive {
					http.Error(w, `{"error":"cannot delete the active preset"}`, http.StatusBadRequest)
				} else if err == errCannotDeleteEnv {
					http.Error(w, `{"error":"cannot delete the env preset"}`, http.StatusForbidden)
				} else {
					http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
				}
				return
			}
			writeJSON(w, map[string]interface{}{"deleted": name})
			return
		}

		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	http.NotFound(w, r)
}

// testVisionResult is the response from a connection test
type testVisionResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Latency  string `json:"latency,omitempty"`
	Model    string `json:"model,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// testVisionPreset sends a minimal text-only chat request to verify the endpoint is reachable
// and the model responds. No image is needed — just a "Hello" prompt.
func testVisionPreset(p *VisionPreset) *testVisionResult {
	result := &testVisionResult{
		Model:    p.Model,
		Endpoint: p.Endpoint,
	}

	reqBody := map[string]interface{}{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with exactly: OK"},
		},
		"max_tokens": 10,
	}
	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		result.Message = fmt.Sprintf("Failed to build request: %v", err)
		return result
	}

	req, err := http.NewRequest("POST", p.Endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		result.Message = fmt.Sprintf("Failed to create request: %v", err)
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	start := time.Now()
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	latency := time.Since(start)
	result.Latency = latency.Round(time.Millisecond).String()

	if err != nil {
		result.Message = fmt.Sprintf("Connection failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		result.Message = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return result
	}

	var chatResp visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		result.Message = fmt.Sprintf("Bad response format: %v", err)
		return result
	}

	if chatResp.Error != nil {
		result.Message = fmt.Sprintf("API error: %s", chatResp.Error.Message)
		return result
	}

	if len(chatResp.Choices) == 0 {
		result.Message = "API returned no choices"
		return result
	}

	result.Success = true
	result.Message = fmt.Sprintf("Connected successfully — model replied: %q", chatResp.Choices[0].Message.Content)
	return result
}
