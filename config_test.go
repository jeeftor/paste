package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Config test helpers ---

func setupConfigTestDir(t *testing.T) string {
	t.Helper()
	dir := setupTestDir(t)
	visionConfigFile = filepath.Join(dir, visionConfigFileName)
	// Clear env vars that could interfere
	os.Unsetenv("VISION_ENDPOINT")
	os.Unsetenv("VISION_MODEL")
	os.Unsetenv("VISION_ENABLED")
	return dir
}

// --- Tests ---

func TestInitVisionConfigDefaults(t *testing.T) {
	setupConfigTestDir(t)

	visionConfig = VisionConfigFile{}
	visionEnvOverridden = false
	initVisionConfig()

	// Should have 3 default presets
	expected := []string{"lemonade", "ollama", "openai"}
	for _, name := range expected {
		p, ok := visionConfig.Presets[name]
		if !ok {
			t.Errorf("default preset %q not found", name)
			continue
		}
		if p.Endpoint == "" {
			t.Errorf("preset %q has empty endpoint", name)
		}
		if p.Model == "" {
			t.Errorf("preset %q has empty model", name)
		}
	}

	// Active preset should be lemonade
	if visionConfig.ActivePreset != "lemonade" {
		t.Errorf("active preset = %q, expected 'lemonade'", visionConfig.ActivePreset)
	}

	// Should not be env overridden
	if visionEnvOverridden {
		t.Error("env_overridden should be false when no env vars set")
	}

	// Global vars should be set from active preset
	if visionEndpoint != defaultVisionPresets["lemonade"].Endpoint {
		t.Errorf("visionEndpoint = %q", visionEndpoint)
	}
	if visionModel != defaultVisionPresets["lemonade"].Model {
		t.Errorf("visionModel = %q", visionModel)
	}
}

func TestInitVisionConfigPersists(t *testing.T) {
	dir := setupConfigTestDir(t)

	visionConfig = VisionConfigFile{}
	initVisionConfig()

	// Config file should exist on disk
	if _, err := os.Stat(filepath.Join(dir, visionConfigFileName)); err != nil {
		t.Errorf("config file not written: %v", err)
	}
}

func TestInitVisionConfigLoadsExisting(t *testing.T) {
	dir := setupConfigTestDir(t)

	// Write a config file first
	existing := VisionConfigFile{
		Presets: map[string]*VisionPreset{
			"custom": {
				Name:        "custom",
				Endpoint:    "http://custom:9999/v1/chat/completions",
				Model:       "custom-model",
				Description: "Custom preset",
			},
		},
		ActivePreset: "custom",
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(dir, visionConfigFileName), data, 0644)

	// Load it
	visionConfig = VisionConfigFile{}
	initVisionConfig()

	// Custom preset should be loaded
	p, ok := visionConfig.Presets["custom"]
	if !ok {
		t.Fatal("custom preset not loaded")
	}
	if p.Endpoint != "http://custom:9999/v1/chat/completions" {
		t.Errorf("endpoint = %q", p.Endpoint)
	}

	// Active preset should be custom
	if visionConfig.ActivePreset != "custom" {
		t.Errorf("active = %q, expected 'custom'", visionConfig.ActivePreset)
	}

	// Default presets should be merged in
	if _, ok := visionConfig.Presets["lemonade"]; !ok {
		t.Error("default preset 'lemonade' not merged in")
	}
}

func TestInitVisionConfigEnvOverride(t *testing.T) {
	setupConfigTestDir(t)

	os.Setenv("VISION_ENDPOINT", "http://env-override:8080/v1/chat/completions")
	os.Setenv("VISION_MODEL", "env-model")
	defer os.Unsetenv("VISION_ENDPOINT")
	defer os.Unsetenv("VISION_MODEL")

	visionConfig = VisionConfigFile{}
	initVisionConfig()

	// Should be env overridden
	if !visionEnvOverridden {
		t.Error("env_overridden should be true")
	}

	// Should have an "env" preset
	p, ok := visionConfig.Presets["env"]
	if !ok {
		t.Fatal("env preset not created")
	}
	if p.Endpoint != "http://env-override:8080/v1/chat/completions" {
		t.Errorf("env endpoint = %q", p.Endpoint)
	}
	if p.Model != "env-model" {
		t.Errorf("env model = %q", p.Model)
	}

	// Active should be "env"
	if visionConfig.ActivePreset != "env" {
		t.Errorf("active = %q, expected 'env'", visionConfig.ActivePreset)
	}

	// Global vars should use env values
	if visionEndpoint != "http://env-override:8080/v1/chat/completions" {
		t.Errorf("visionEndpoint = %q", visionEndpoint)
	}
}

func TestInitVisionConfigEnabledEnv(t *testing.T) {
	setupConfigTestDir(t)

	os.Setenv("VISION_ENABLED", "false")
	defer os.Unsetenv("VISION_ENABLED")

	visionConfig = VisionConfigFile{}
	visionEnabled = true
	initVisionConfig()

	if visionEnabled {
		t.Error("visionEnabled should be false from env")
	}
}

func TestListVisionPresetsSorted(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	// Add a custom preset
	addVisionPreset(&VisionPreset{
		Name:     "aaa_custom",
		Endpoint: "http://test",
		Model:    "test",
	})

	presets := listVisionPresets()
	if len(presets) < 4 {
		t.Errorf("expected at least 4 presets, got %d", len(presets))
	}

	// All should have names
	for _, p := range presets {
		if p.Name == "" {
			t.Error("preset with empty name")
		}
	}
}

func TestAddVisionPresetDuplicate(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	// Try to add a preset with existing name
	ok := addVisionPreset(&VisionPreset{
		Name:     "lemonade",
		Endpoint: "http://dup",
		Model:    "dup",
	})
	if ok {
		t.Error("addVisionPreset returned true for duplicate name")
	}
}

func TestUpdateVisionPreset(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	ok := updateVisionPreset("lemonade", "http://new-endpoint", "new-model", "", "")
	if !ok {
		t.Fatal("updateVisionPreset returned false")
	}

	p, _ := getVisionPreset("lemonade")
	if p.Endpoint != "http://new-endpoint" {
		t.Errorf("endpoint = %q", p.Endpoint)
	}
	if p.Model != "new-model" {
		t.Errorf("model = %q", p.Model)
	}

	// Since lemonade is active, global vars should update
	if visionEndpoint != "http://new-endpoint" {
		t.Errorf("visionEndpoint = %q, expected 'http://new-endpoint'", visionEndpoint)
	}

	// Update non-existent
	ok = updateVisionPreset("nonexistent", "x", "y", "", "")
	if ok {
		t.Error("updateVisionPreset returned true for non-existent")
	}
}

func TestDeleteVisionPreset(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	// Add a custom preset to delete
	addVisionPreset(&VisionPreset{
		Name:     "to_delete",
		Endpoint: "http://del",
		Model:    "del",
	})

	err := deleteVisionPreset("to_delete")
	if err != nil {
		t.Errorf("deleteVisionPreset failed: %v", err)
	}

	// Verify gone
	_, ok := getVisionPreset("to_delete")
	if ok {
		t.Error("preset still exists after delete")
	}
}

func TestDeleteActivePresetFails(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	// lemonade is active by default
	err := deleteVisionPreset("lemonade")
	if err != errCannotDeleteActive {
		t.Errorf("expected errCannotDeleteActive, got %v", err)
	}
}

func TestDeleteEnvPresetFails(t *testing.T) {
	setupConfigTestDir(t)
	os.Setenv("VISION_ENDPOINT", "http://env:8080")
	defer os.Unsetenv("VISION_ENDPOINT")
	initVisionConfig()

	err := deleteVisionPreset("env")
	if err != errCannotDeleteEnv {
		t.Errorf("expected errCannotDeleteEnv, got %v", err)
	}
}

func TestSetActiveVisionPreset(t *testing.T) {
	setupConfigTestDir(t)
	initVisionConfig()

	ok := setActiveVisionPreset("ollama")
	if !ok {
		t.Fatal("setActiveVisionPreset returned false")
	}

	if visionConfig.ActivePreset != "ollama" {
		t.Errorf("active = %q, expected 'ollama'", visionConfig.ActivePreset)
	}

	// Global vars should update
	if visionEndpoint != defaultVisionPresets["ollama"].Endpoint {
		t.Errorf("visionEndpoint = %q", visionEndpoint)
	}
	if visionModel != defaultVisionPresets["ollama"].Model {
		t.Errorf("visionModel = %q", visionModel)
	}

	// Non-existent
	ok = setActiveVisionPreset("nonexistent")
	if ok {
		t.Error("setActiveVisionPreset returned true for non-existent")
	}
}

// --- REST API tests ---

func TestGetVisionConfigAPI(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	// Create a test server with the config handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	resp, err := http.Get(server.URL + "/api/config/vision")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if result["active_preset"] != "lemonade" {
		t.Errorf("active_preset = %v", result["active_preset"])
	}
	presets, ok := result["presets"].([]interface{})
	if !ok {
		t.Fatalf("presets not an array")
	}
	if len(presets) < 3 {
		t.Errorf("expected at least 3 presets, got %d", len(presets))
	}
	if result["env_overridden"] != false {
		t.Error("env_overridden should be false")
	}
}

func TestPresetCRUDAPI(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	// Create
	body := strings.NewReader(`{"name":"test_preset","endpoint":"http://test:8080/v1/chat/completions","model":"test-model","description":"Test"}`)
	resp, err := http.Post(server.URL+"/api/config/vision/presets", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("create status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify it exists
	p, ok := getVisionPreset("test_preset")
	if !ok {
		t.Fatal("created preset not found")
	}
	if p.Endpoint != "http://test:8080/v1/chat/completions" {
		t.Errorf("endpoint = %q", p.Endpoint)
	}

	// Update
	updateBody := strings.NewReader(`{"endpoint":"http://updated:9090/v1/chat/completions","model":"updated-model"}`)
	req, _ := http.NewRequest("PUT", server.URL+"/api/config/vision/presets/test_preset", updateBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("update status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	p, _ = getVisionPreset("test_preset")
	if p.Endpoint != "http://updated:9090/v1/chat/completions" {
		t.Errorf("updated endpoint = %q", p.Endpoint)
	}

	// Delete
	req, _ = http.NewRequest("DELETE", server.URL+"/api/config/vision/presets/test_preset", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	_, ok = getVisionPreset("test_preset")
	if ok {
		t.Error("preset still exists after delete")
	}
}

func TestSetActivePresetAPI(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	body := strings.NewReader(`{"preset":"ollama"}`)
	resp, err := http.Post(server.URL+"/api/config/vision/active", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if result["active_preset"] != "ollama" {
		t.Errorf("active_preset = %v", result["active_preset"])
	}
}

func TestSetActivePresetAPIWithEnvOverride(t *testing.T) {
	dir := setupConfigTestDir(t)
	os.Setenv("VISION_ENDPOINT", "http://env:8080")
	defer os.Unsetenv("VISION_ENDPOINT")
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	// Should fail with 409 Conflict
	body := strings.NewReader(`{"preset":"ollama"}`)
	resp, err := http.Post(server.URL+"/api/config/vision/active", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, expected 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestToggleEnabledAPI(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	body := strings.NewReader(`{"enabled":false}`)
	resp, err := http.Post(server.URL+"/api/config/vision/enabled", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	if visionEnabled {
		t.Error("visionEnabled should be false")
	}

	// Toggle back on
	body = strings.NewReader(`{"enabled":true}`)
	resp, err = http.Post(server.URL+"/api/config/vision/enabled", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()

	if !visionEnabled {
		t.Error("visionEnabled should be true")
	}
}

func TestCreatePresetDuplicateAPI(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	// Try to create a preset with existing name
	body := strings.NewReader(`{"name":"lemonade","endpoint":"http://dup","model":"dup"}`)
	resp, err := http.Post(server.URL+"/api/config/vision/presets", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, expected 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreatePresetMissingFields(t *testing.T) {
	dir := setupConfigTestDir(t)
	initVisionConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	server := newTestServer(mux)
	defer server.Close()
	defer teardownTestDir(t, dir)

	// Missing endpoint
	body := strings.NewReader(`{"name":"test","model":"test"}`)
	resp, err := http.Post(server.URL+"/api/config/vision/presets", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, expected 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test helper ---

func newTestServer(mux *http.ServeMux) *httptest.Server {
	return httptest.NewServer(mux)
}
