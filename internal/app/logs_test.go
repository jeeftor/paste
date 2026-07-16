package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPILogsRedactsSensitiveValues(t *testing.T) {
	appLogs.mu.Lock()
	previousLines := append([]string(nil), appLogs.lines...)
	previousPartial := appLogs.partial
	appLogs.lines = nil
	appLogs.partial = ""
	appLogs.mu.Unlock()
	t.Cleanup(func() {
		appLogs.mu.Lock()
		appLogs.lines = previousLines
		appLogs.partial = previousPartial
		appLogs.mu.Unlock()
	})

	_, err := appLogs.Write([]byte("Authorization: Bearer super-secret\nrequest failed: https://example.test/v1?api_key=another-secret\n"))
	if err != nil {
		t.Fatalf("write logs: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	resp := httptest.NewRecorder()
	apiLogsHandler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want %d", resp.Code, http.StatusOK)
	}

	var body struct {
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	got := strings.Join(body.Lines, "\n")
	for _, secret := range []string{"super-secret", "another-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("logs contain secret %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("logs did not contain redaction marker: %s", got)
	}
}
