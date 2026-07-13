package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebRoutesServeTheUI(t *testing.T) {
	originalAssets := assets
	originalVersion := version
	t.Cleanup(func() {
		assets = originalAssets
		version = originalVersion
	})

	assets = Assets{IndexHTML: []byte("<html>Klipbord {{VERSION}}</html>")}
	version = "test"
	handler := NewHandler()

	for _, path := range []string{"/clip", "/persist", "/config", "/mcp-web", "/rest-web"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

		if response.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		if got := response.Body.String(); got != "<html>Klipbord test</html>" {
			t.Errorf("GET %s body = %q", path, got)
		}
	}
}

func TestRootRedirectsToClip(t *testing.T) {
	handler := NewHandler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusFound {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusFound)
	}
	if got := response.Header().Get("Location"); got != "/clip" {
		t.Errorf("redirect location = %q, want /clip", got)
	}
}

func TestProtocolRoutesDoNotServeTheUI(t *testing.T) {
	originalAssets := assets
	t.Cleanup(func() { assets = originalAssets })
	assets = Assets{IndexHTML: []byte("Klipbord UI")}
	handler := NewHandler()

	for _, path := range []string{"/api/version", "/mcp"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

		if response.Body.String() == "Klipbord UI" {
			t.Errorf("GET %s unexpectedly served the UI", path)
		}
	}
}
