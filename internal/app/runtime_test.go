package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeRuntimeLemonade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"all_models_loaded":[{"model_name":"qwen","device":"gpu"}]}`))
		case "/v1/system-stats":
			_, _ = w.Write([]byte(`{"memory_gb":32.5,"gpu_percent":74,"vram_gb":12.25}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := httptest.NewRequest(http.MethodGet, "/api/vision/runtime", nil)
	status := probeRuntime(request, &VisionPreset{Endpoint: server.URL + "/v1/chat/completions"})
	if status.Provider != "lemonade" || status.State != "ready" {
		t.Fatalf("status = %#v, want Lemonade ready", status)
	}
	if len(status.Models) != 1 || status.Models[0].Name != "qwen" {
		t.Errorf("models = %#v", status.Models)
	}
	if status.System == nil || status.System.VRAMGB != 12.25 {
		t.Errorf("system = %#v", status.System)
	}
}
