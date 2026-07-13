package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatrixRunReplaysEventsToReconnectedClients(t *testing.T) {
	run := newMatrixRun()
	run.emit("start", map[string]string{"status": "running"})

	events, unsubscribe := run.subscribe()
	defer unsubscribe()
	first := <-events
	if first.eventType != "start" {
		t.Fatalf("first event = %q, want start", first.eventType)
	}

	run.emit("cell_start", map[string]int{"cell": 1})
	second := <-events
	if second.eventType != "cell_start" {
		t.Errorf("second event = %q, want cell_start", second.eventType)
	}

	reconnectedEvents, reconnectUnsubscribe := run.subscribe()
	defer reconnectUnsubscribe()
	if event := <-reconnectedEvents; event.eventType != "start" {
		t.Errorf("reconnect first event = %q, want start", event.eventType)
	}
	if event := <-reconnectedEvents; event.eventType != "cell_start" {
		t.Errorf("reconnect second event = %q, want cell_start", event.eventType)
	}

	run.finish()
	if run.isRunning() {
		t.Error("matrix run is still marked running after finish")
	}
}

func TestTryUnloadModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		var request struct {
			Model     string `json:"model"`
			KeepAlive int    `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "matrix-model" {
			t.Errorf("model = %q, want matrix-model", request.Model)
		}
		if request.KeepAlive != 0 {
			t.Errorf("keep_alive = %d, want 0", request.KeepAlive)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	status, err := tryUnloadModel(&VisionPreset{
		Name:     "matrix",
		Model:    "matrix-model",
		Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("tryUnloadModel() error = %v", err)
	}
	if status != http.StatusNoContent {
		t.Errorf("status = %d, want %d", status, http.StatusNoContent)
	}
}

func TestTryUnloadModelReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not supported", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	status, err := tryUnloadModel(&VisionPreset{
		Name:     "matrix",
		Model:    "matrix-model",
		Endpoint: server.URL,
	})
	if err == nil {
		t.Fatal("tryUnloadModel() error = nil, want an HTTP status error")
	}
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestVisionMatrixStreamsCellAndLifecycleEvents(t *testing.T) {
	dir := setupTestDir(t)
	t.Cleanup(func() { teardownTestDir(t, dir) })

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"matrix result"}}]}`))
	}))
	t.Cleanup(mockLLM.Close)

	originalVisionEnabled := visionEnabled
	originalImageTypes := allSampleImageTypes
	visionEnabled = true
	allSampleImageTypes = []string{"terminal"}
	t.Cleanup(func() {
		visionEnabled = originalVisionEnabled
		allSampleImageTypes = originalImageTypes
	})

	visionConfigMu.Lock()
	originalConfig := visionConfig
	visionConfig = VisionConfigFile{Presets: map[string]*VisionPreset{
		"matrix": {Name: "matrix", Model: "matrix-model", Endpoint: mockLLM.URL},
	}}
	visionConfigMu.Unlock()
	t.Cleanup(func() {
		visionConfigMu.Lock()
		visionConfig = originalConfig
		visionConfigMu.Unlock()
	})

	promptsMu.Lock()
	originalPrompts := prompts
	prompts = map[string]*VisionPrompt{
		"default": {Name: "default", Prompt: "Describe this image."},
	}
	promptsMu.Unlock()
	t.Cleanup(func() {
		promptsMu.Lock()
		prompts = originalPrompts
		promptsMu.Unlock()
	})

	server := httptest.NewServer(http.HandlerFunc(apiVisionTestMatrixHandler))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET matrix stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read matrix stream: %v", err)
	}
	for _, eventType := range []string{"start", "cell_start", "cell_complete", "model_unloading", "model_unloaded", "done"} {
		if !strings.Contains(string(body), "event: "+eventType) {
			t.Errorf("matrix stream is missing %q event:\n%s", eventType, body)
		}
	}
}
