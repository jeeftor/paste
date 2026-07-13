package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const runtimeStatusTimeout = 5 * time.Second

type runtimeStatus struct {
	Provider string         `json:"provider"`
	State    string         `json:"state"`
	Models   []runtimeModel `json:"models,omitempty"`
	System   *runtimeSystem `json:"system,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type runtimeModel struct {
	Name       string `json:"name"`
	Device     string `json:"device,omitempty"`
	VRAMBytes  int64  `json:"vram_bytes,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	ContextLen int    `json:"context_length,omitempty"`
}

type runtimeSystem struct {
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemoryGB   float64 `json:"memory_gb,omitempty"`
	GPUPercent float64 `json:"gpu_percent,omitempty"`
	VRAMGB     float64 `json:"vram_gb,omitempty"`
}

// apiVisionRuntimeHandler reports provider-backed model lifecycle and resource status.
func apiVisionRuntimeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	statuses := make(map[string]runtimeStatus)
	byEndpoint := make(map[string]runtimeStatus)
	for _, preset := range listVisionPresets() {
		status, ok := byEndpoint[preset.Endpoint]
		if !ok {
			status = probeRuntime(r, preset)
			byEndpoint[preset.Endpoint] = status
		}
		statuses[preset.Name] = status
	}
	writeJSON(w, map[string]interface{}{"runtimes": statuses})
}

func probeRuntime(r *http.Request, preset *VisionPreset) runtimeStatus {
	base := strings.TrimSuffix(preset.Endpoint, "/v1/chat/completions")
	client := &http.Client{Timeout: runtimeStatusTimeout}
	if health, ok := getRuntimeJSON[struct {
		Models []struct {
			Name   string `json:"model_name"`
			Device string `json:"device"`
		} `json:"all_models_loaded"`
	}](r, client, base+"/v1/health", preset.APIKey); ok {
		status := runtimeStatus{Provider: "lemonade", State: "ready"}
		for _, model := range health.Models {
			status.Models = append(status.Models, runtimeModel{Name: model.Name, Device: model.Device})
		}
		if system, ok := getRuntimeJSON[runtimeSystem](r, client, base+"/v1/system-stats", preset.APIKey); ok {
			status.System = &system
		}
		return status
	}
	if ps, ok := getRuntimeJSON[struct {
		Models []struct {
			Name       string `json:"name"`
			VRAMBytes  int64  `json:"size_vram"`
			ExpiresAt  string `json:"expires_at"`
			ContextLen int    `json:"context_length"`
		} `json:"models"`
	}](r, client, base+"/api/ps", preset.APIKey); ok {
		status := runtimeStatus{Provider: "ollama", State: "ready"}
		for _, model := range ps.Models {
			status.Models = append(status.Models, runtimeModel{Name: model.Name, VRAMBytes: model.VRAMBytes, ExpiresAt: model.ExpiresAt, ContextLen: model.ContextLen})
		}
		return status
	}
	return runtimeStatus{Provider: "generic", State: "unknown", Error: "provider does not expose runtime status"}
}

func getRuntimeJSON[T any](r *http.Request, client *http.Client, url, apiKey string) (T, bool) {
	var value T
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return value, false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(req)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		return value, false
	}
	defer response.Body.Close()
	return value, json.NewDecoder(response.Body).Decode(&value) == nil
}
