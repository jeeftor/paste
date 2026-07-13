package app

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const updateCheckCacheTTL = 6 * time.Hour

var (
	updateReleaseURL = "https://api.github.com/repos/jeeftor/klipbord/releases/latest"
	updateHTTPClient = &http.Client{Timeout: 5 * time.Second}
	updateCache      cachedUpdateCheck
	updateCacheMu    sync.Mutex
)

var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

type updateCheck struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

type cachedUpdateCheck struct {
	value     updateCheck
	expiresAt time.Time
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

func updateCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	updateCacheMu.Lock()
	defer updateCacheMu.Unlock()

	if time.Now().Before(updateCache.expiresAt) && updateCache.value.CurrentVersion == version {
		writeJSON(w, updateCache.value)
		return
	}

	result := updateCheck{CurrentVersion: version}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, updateReleaseURL, nil)
	if err == nil {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "klipbord-update-check")
		response, requestErr := updateHTTPClient.Do(request)
		if requestErr == nil {
			defer response.Body.Close()
			if response.StatusCode == http.StatusOK {
				var release githubRelease
				if decodeErr := json.NewDecoder(response.Body).Decode(&release); decodeErr == nil {
					result.LatestVersion = release.TagName
					result.ReleaseURL = release.HTMLURL
					result.UpdateAvailable = isNewerVersion(version, release.TagName)
				}
			}
		}
	}

	updateCache = cachedUpdateCheck{value: result, expiresAt: time.Now().Add(updateCheckCacheTTL)}
	writeJSON(w, result)
}

// isNewerVersion reports whether latest is a stable semantic version newer than current.
func isNewerVersion(current, latest string) bool {
	currentParts, ok := versionParts(current)
	if !ok {
		return false
	}
	latestParts, ok := versionParts(latest)
	if !ok {
		return false
	}
	for index := range currentParts {
		if latestParts[index] != currentParts[index] {
			return latestParts[index] > currentParts[index]
		}
	}
	return false
}

func versionParts(value string) ([3]int, bool) {
	match := semverPattern.FindStringSubmatch(value)
	if match == nil {
		return [3]int{}, false
	}

	var parts [3]int
	for index, part := range match[1:] {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		parts[index] = parsed
	}
	return parts, true
}
