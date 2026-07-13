package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateCheckHandlerReportsNewerRelease(t *testing.T) {
	originalVersion := version
	originalURL := updateReleaseURL
	originalCache := updateCache
	t.Cleanup(func() {
		version = originalVersion
		updateReleaseURL = originalURL
		updateCache = originalCache
	})

	version = "v2.3.0"
	updateCache = cachedUpdateCheck{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v2.3.1","html_url":"https://github.com/jeeftor/klipbord/releases/tag/v2.3.1"}`))
	}))
	t.Cleanup(github.Close)
	updateReleaseURL = github.URL

	response := httptest.NewRecorder()
	updateCheckHandler(response, httptest.NewRequest(http.MethodGet, "/api/update-check", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/update-check status = %d, want %d", response.Code, http.StatusOK)
	}

	var result updateCheck
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.UpdateAvailable {
		t.Error("UpdateAvailable = false, want true")
	}
	if result.CurrentVersion != "v2.3.0" || result.LatestVersion != "v2.3.1" {
		t.Errorf("versions = current %q latest %q", result.CurrentVersion, result.LatestVersion)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "new patch", current: "v2.3.0", latest: "v2.3.1", want: true},
		{name: "same version", current: "v2.3.1", latest: "v2.3.1", want: false},
		{name: "older latest", current: "v2.3.1", latest: "v2.3.0", want: false},
		{name: "development build", current: "dev", latest: "v2.3.1", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isNewerVersion(test.current, test.latest); got != test.want {
				t.Errorf("isNewerVersion(%q, %q) = %t, want %t", test.current, test.latest, got, test.want)
			}
		})
	}
}
