package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Vision test helpers ---

// mockVisionServer creates a test HTTP server that simulates a vision LLM API
func mockVisionServer(t *testing.T, responseText string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a chat completion request
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req visionChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Verify it has an image
		hasImage := false
		for _, msg := range req.Messages {
			for _, content := range msg.Content {
				if content.Type == "image_url" && content.ImageURL != nil {
					if strings.HasPrefix(content.ImageURL.URL, "data:image") {
						hasImage = true
					}
				}
			}
		}
		if !hasImage {
			t.Error("vision request missing image_url content")
		}

		// Return the mock response
		resp := fmt.Sprintf(`{
			"choices": [{"message": {"content": %q}}]
		}`, responseText)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	t.Cleanup(server.Close)
	return server
}

func setupVisionTestServer(t *testing.T, mockResponse string) (*httptest.Server, string, *httptest.Server) {
	t.Helper()
	dir := setupTestDir(t)
	baseURL = "http://test.local"

	// Set up prompts
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	// Set up vision config
	visionConfigFile = filepath.Join(dir, visionConfigFileName)
	os.Unsetenv("VISION_ENDPOINT")
	os.Unsetenv("VISION_MODEL")
	os.Unsetenv("VISION_ENABLED")
	visionConfig = VisionConfigFile{}
	initVisionConfig()

	// Set up vision with mock server
	mockServer := mockVisionServer(t, mockResponse)
	visionEndpoint = mockServer.URL + "/v1/chat/completions"
	visionModel = "test-vision-model"
	visionEnabled = true

	// Create the app server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", apiFilesHandler)
	mux.HandleFunc("/api/files/", apiFileHandler)
	mux.HandleFunc("/api/analyze/", apiAnalyzeHandler)
	mux.HandleFunc("/api/prompts", apiPromptsHandler)
	mux.HandleFunc("/api/prompts/", apiPromptHandler)
	mux.HandleFunc("/api/config/vision", apiVisionConfigHandler)
	mux.HandleFunc("/api/config/vision/", apiVisionConfigHandler)
	mux.HandleFunc("/api/text", apiTextHandler)
	mux.HandleFunc("/api/text/", apiTextItemHandler)
	mux.HandleFunc("/api/upload", apiUploadHandler)
	mux.HandleFunc("/mcp", mcpHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		mockServer.Close()
		teardownTestDir(t, dir)
	})
	return server, dir, mockServer
}

func uploadTestImage(t *testing.T, server *httptest.Server, pngData []byte) string {
	t.Helper()
	body := &strings.Builder{}
	writer := multipart.NewWriter(body)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="test.png"`}
	h["Content-Type"] = []string{"image/png"}
	part, _ := writer.CreatePart(h)
	part.Write(pngData)
	writer.WriteField("ttl", "7d")
	writer.Close()

	req, _ := http.NewRequest("POST", server.URL+"/api/upload", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result["id"].(string)
}

// --- Prompt tests ---

func TestInitPrompts(t *testing.T) {
	dir := setupTestDir(t)
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	// Should have 5 built-in prompts
	expected := []string{"default", "terminal", "code", "document", "diagram"}
	for _, name := range expected {
		p, ok := prompts[name]
		if !ok {
			t.Errorf("built-in prompt %q not found", name)
			continue
		}
		if !p.BuiltIn {
			t.Errorf("prompt %q should be built-in", name)
		}
		if p.Prompt == "" {
			t.Errorf("prompt %q has empty prompt text", name)
		}
	}

	// Verify prompts.json was written to disk
	if _, err := os.Stat(promptsFile); err != nil {
		t.Errorf("prompts.json not written to disk: %v", err)
	}
}

func TestInitPromptsPersistsCustomPrompts(t *testing.T) {
	dir := setupTestDir(t)
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	// Add a custom prompt
	prompts["custom_test"] = &VisionPrompt{
		Name:        "custom_test",
		Description: "Test custom prompt",
		Prompt:      "Test prompt text",
		BuiltIn:     false,
	}
	savePrompts()

	// Reload
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	// Custom prompt should still exist
	p, ok := prompts["custom_test"]
	if !ok {
		t.Fatal("custom prompt not persisted")
	}
	if p.Prompt != "Test prompt text" {
		t.Errorf("custom prompt text = %q", p.Prompt)
	}
}

func TestGetPrompt(t *testing.T) {
	dir := setupTestDir(t)
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	p, ok := getPrompt("terminal")
	if !ok {
		t.Fatal("getPrompt('terminal') not found")
	}
	if p.Name != "terminal" {
		t.Errorf("name = %q", p.Name)
	}

	_, ok = getPrompt("nonexistent")
	if ok {
		t.Error("getPrompt found non-existent prompt")
	}
}

func TestListPromptsSorted(t *testing.T) {
	dir := setupTestDir(t)
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()

	// Add a custom prompt
	prompts["aaa_custom"] = &VisionPrompt{
		Name:    "aaa_custom",
		Prompt:  "test",
		BuiltIn: false,
	}

	list := listPromptsSorted()
	// Built-ins should come first
	if !list[0].BuiltIn {
		t.Error("expected built-in prompt first")
	}
	// Custom should be after all built-ins
	lastBuiltIn := -1
	firstCustom := -1
	for i, p := range list {
		if p.BuiltIn {
			lastBuiltIn = i
		} else if firstCustom == -1 {
			firstCustom = i
		}
	}
	if firstCustom == -1 || lastBuiltIn >= firstCustom {
		t.Error("custom prompts should come after built-ins")
	}
}

// --- Prompt REST API tests ---

func TestPromptsAPI(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test text","description":"test desc"}`)

	// GET /api/prompts
	resp, err := http.Get(server.URL + "/api/prompts")
	if err != nil {
		t.Fatalf("GET /api/prompts failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET prompts status = %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	promptsList, ok := result["prompts"].([]interface{})
	if !ok {
		t.Fatalf("prompts not an array: %T", result["prompts"])
	}
	if len(promptsList) < 5 {
		t.Errorf("expected at least 5 prompts, got %d", len(promptsList))
	}

	// POST /api/prompts — create custom
	createBody := strings.NewReader(`{"name":"my_prompt","description":"My custom prompt","prompt":"Describe this"}`)
	resp, err = http.Post(server.URL+"/api/prompts", "application/json", createBody)
	if err != nil {
		t.Fatalf("POST /api/prompts failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST prompt status = %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if result["name"] != "my_prompt" {
		t.Errorf("created prompt name = %v", result["name"])
	}

	// GET /api/prompts/{name}
	resp, err = http.Get(server.URL + "/api/prompts/my_prompt")
	if err != nil {
		t.Fatalf("GET /api/prompts/my_prompt failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET prompt status = %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if result["prompt"] != "Describe this" {
		t.Errorf("prompt text = %v", result["prompt"])
	}

	// PUT /api/prompts/{name} — update
	updateBody := strings.NewReader(`{"description":"Updated description"}`)
	req, _ := http.NewRequest("PUT", server.URL+"/api/prompts/my_prompt", updateBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if result["description"] != "Updated description" {
		t.Errorf("updated description = %v", result["description"])
	}

	// DELETE /api/prompts/{name}
	req, _ = http.NewRequest("DELETE", server.URL+"/api/prompts/my_prompt", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify it's gone
	_, ok = getPrompt("my_prompt")
	if ok {
		t.Error("prompt still exists after delete")
	}
}

func TestDeleteBuiltinPromptFails(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	req, _ := http.NewRequest("DELETE", server.URL+"/api/prompts/terminal", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("delete built-in status = %d, expected 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateDuplicatePromptFails(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	// Try to create a prompt with a built-in name
	createBody := strings.NewReader(`{"name":"terminal","description":"dup","prompt":"dup"}`)
	resp, err := http.Post(server.URL+"/api/prompts", "application/json", createBody)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("create duplicate status = %d, expected 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Vision analysis tests ---

func TestAnalyzeImageWithMockServer(t *testing.T) {
	mockResponse := `{"image_type":"terminal","text":"$ hello world","description":"A terminal showing hello world"}`
	server, dir, _ := setupVisionTestServer(t, mockResponse)

	// Upload an image
	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)

	// Wait for async analysis to complete
	// (the upload triggers analyzeImageAsync)
	time.Sleep(500 * time.Millisecond)

	// Verify analysis was stored
	item, ok := findItem(id)
	if !ok {
		t.Fatal("item not found")
	}
	analysis, exists := item.Analyses["default"]
	if !exists {
		t.Fatal("no default analysis found")
	}
	if analysis.Status != "complete" {
		t.Errorf("analysis status = %q, expected 'complete'", analysis.Status)
	}
	if analysis.Text != "$ hello world" {
		t.Errorf("analysis text = %q", analysis.Text)
	}
	if analysis.Backend != "test-vision-model" {
		t.Errorf("analysis backend = %q", analysis.Backend)
	}

	_ = dir
}

func TestAnalyzeImageWithPromptParam(t *testing.T) {
	mockResponse := `{"image_type":"terminal","text":"$ ls -la","description":"Terminal with ls command"}`
	server, _, _ := setupVisionTestServer(t, mockResponse)

	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)

	// Manually trigger analysis with "terminal" prompt
	resp, err := http.Post(server.URL+"/api/analyze/"+id+"?prompt=terminal", "application/json", nil)
	if err != nil {
		t.Fatalf("analyze request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("analyze status = %d, body: %s", resp.StatusCode, string(body))
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if result["prompt"] != "terminal" {
		t.Errorf("prompt = %v, expected 'terminal'", result["prompt"])
	}
	analysis := result["analysis"].(map[string]interface{})
	if analysis["status"] != "complete" {
		t.Errorf("status = %v", analysis["status"])
	}
	if analysis["prompt_name"] != "terminal" {
		t.Errorf("prompt_name = %v", analysis["prompt_name"])
	}
}

func TestAnalyzeImageWithNonExistentPrompt(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)

	resp, err := http.Post(server.URL+"/api/analyze/"+id+"?prompt=nonexistent_prompt", "application/json", nil)
	if err != nil {
		t.Fatalf("analyze failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("analyze with bad prompt status = %d, expected 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMultiPromptAnalysis(t *testing.T) {
	mockResponse := `{"image_type":"terminal","text":"$ echo test","description":"Terminal output"}`
	server, _, _ := setupVisionTestServer(t, mockResponse)

	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)
	time.Sleep(200 * time.Millisecond)

	// Analyze with terminal prompt
	http.Post(server.URL+"/api/analyze/"+id+"?prompt=terminal", "application/json", nil)

	// Analyze with code prompt
	http.Post(server.URL+"/api/analyze/"+id+"?prompt=code", "application/json", nil)

	// Verify both analyses exist
	item, ok := findItem(id)
	if !ok {
		t.Fatal("item not found")
	}
	if len(item.Analyses) < 2 {
		t.Errorf("expected at least 2 analyses, got %d", len(item.Analyses))
	}
	if _, ok := item.Analyses["terminal"]; !ok {
		t.Error("terminal analysis missing")
	}
	if _, ok := item.Analyses["code"]; !ok {
		t.Error("code analysis missing")
	}
}

func TestStripMarkdownCodeFence(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`{"text":"hello"}`, `{"text":"hello"}`},
		{"```json\n{\"text\":\"hello\"}\n```", `{"text":"hello"}`},
		{"```\n{\"text\":\"hello\"}\n```", `{"text":"hello"}`},
		{"  ```json\n{\"text\":\"hello\"}\n```  ", `{"text":"hello"}`},
	}
	for _, tt := range tests {
		got := stripMarkdownCodeFence(tt.input)
		if got != tt.expected {
			t.Errorf("stripMarkdownCodeFence(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

// --- MCP tests ---

func TestMCPToolsList(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	resp, err := http.Post(server.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatalf("MCP tools/list failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	tools, ok := result["result"].(map[string]interface{})["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools not an array")
	}
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		tm := tool.(map[string]interface{})
		toolNames[tm["name"].(string)] = true
	}
	expected := []string{"list_files", "get_file", "upload_file", "create_text", "delete_file", "persist_file", "describe_image", "analyze_image", "list_prompts", "create_prompt", "update_prompt", "delete_prompt", "list_vision_presets", "set_vision_preset", "test_vision_preset"}
	for _, name := range expected {
		if !toolNames[name] {
			t.Errorf("tool %q not found in tools/list", name)
		}
	}
}

func TestMCPListPrompts(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_prompts","arguments":{}}}`)
	resp, err := http.Post(server.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "default") {
		t.Errorf("list_prompts text doesn't contain 'default': %s", text)
	}
	if !strings.Contains(text, "terminal") {
		t.Errorf("list_prompts text doesn't contain 'terminal': %s", text)
	}
}

func TestMCPDescribeImage(t *testing.T) {
	mockResponse := `{"image_type":"terminal","text":"$ test","description":"Test terminal"}`
	server, _, _ := setupVisionTestServer(t, mockResponse)

	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)
	time.Sleep(300 * time.Millisecond)

	// Call describe_image via MCP
	body := strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_image","arguments":{"id":"%s"}}}`, id))
	resp, err := http.Post(server.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, id) {
		t.Errorf("describe_image text doesn't contain item id: %s", text)
	}
	if !strings.Contains(text, "$ test") {
		t.Errorf("describe_image text doesn't contain extracted text: %s", text)
	}
}

func TestMCPAnalyzeImage(t *testing.T) {
	mockResponse := `{"image_type":"code","text":"print('hello')","description":"Python code"}`
	server, _, _ := setupVisionTestServer(t, mockResponse)

	pngData := createMinimalPNG(t)
	id := uploadTestImage(t, server, pngData)

	// Call analyze_image with "code" prompt
	body := strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"analyze_image","arguments":{"id":"%s","prompt":"code"}}}`, id))
	resp, err := http.Post(server.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "print('hello')") {
		t.Errorf("analyze_image text doesn't contain extracted text: %s", text)
	}
	if !strings.Contains(text, "[code]") {
		t.Errorf("analyze_image text doesn't contain prompt name: %s", text)
	}
}

func TestMCPDescribeImageNonImage(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	// Create a text item
	body := strings.NewReader(`{"content":"hello","name":"test.txt"}`)
	http.Post(server.URL+"/api/text", "application/json", body)

	// Find the text item
	items := listItems()
	var textID string
	for _, item := range items {
		if item.Type == "text" {
			textID = item.ID
			break
		}
	}
	if textID == "" {
		t.Fatal("no text item found")
	}

	// Try to describe it as an image
	mcpBody := strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_image","arguments":{"id":"%s"}}}`, textID))
	resp, err := http.Post(server.URL+"/mcp", "application/json", mcpBody)
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Error("expected error for describing text item as image")
	} else {
		msg := errObj["message"].(string)
		if !strings.Contains(msg, "not an image") {
			t.Errorf("error message = %q, expected 'not an image'", msg)
		}
	}
}

func TestMCPDescribeImageNotFound(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_image","arguments":{"id":"nonexistent"}}}`)
	resp, err := http.Post(server.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for non-existent item")
	}
	if !strings.Contains(errObj["message"].(string), "not found") {
		t.Errorf("error message doesn't contain 'not found'")
	}
}

// --- MCP prompt CRUD tests ---

func mcpCall(t *testing.T, server *httptest.Server, toolName string, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	params := map[string]interface{}{"name": toolName, "arguments": args}
	reqBody, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	return result
}

func TestMCPCreatePrompt(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "create_prompt", map[string]interface{}{
		"name":        "mcp_test_prompt",
		"description": "Created via MCP",
		"prompt":      "Analyze this image and return JSON.",
	})

	if result["error"] != nil {
		t.Fatalf("create_prompt returned error: %v", result["error"])
	}
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "mcp_test_prompt") {
		t.Errorf("response doesn't contain prompt name: %s", text)
	}

	// Verify it exists
	p, ok := getPrompt("mcp_test_prompt")
	if !ok {
		t.Fatal("created prompt not found")
	}
	if p.Description != "Created via MCP" {
		t.Errorf("description = %q", p.Description)
	}
}

func TestMCPCreatePromptDuplicate(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "create_prompt", map[string]interface{}{
		"name":   "default",
		"prompt": "test",
	})

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for duplicate prompt")
	}
	if !strings.Contains(errObj["message"].(string), "already exists") {
		t.Errorf("error message: %v", errObj["message"])
	}
}

func TestMCPUpdatePrompt(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "update_prompt", map[string]interface{}{
		"name":        "default",
		"description": "Updated via MCP",
		"prompt":      "New prompt text",
	})

	if result["error"] != nil {
		t.Fatalf("update_prompt returned error: %v", result["error"])
	}

	p, _ := getPrompt("default")
	if p.Description != "Updated via MCP" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Prompt != "New prompt text" {
		t.Errorf("prompt not updated")
	}
}

func TestMCPUpdatePromptNotFound(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "update_prompt", map[string]interface{}{
		"name": "nonexistent",
	})

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for non-existent prompt")
	}
	if !strings.Contains(errObj["message"].(string), "not found") {
		t.Errorf("error message: %v", errObj["message"])
	}
}

func TestMCPDeletePrompt(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	// Create a custom prompt first
	mcpCall(t, server, "create_prompt", map[string]interface{}{
		"name":   "to_delete",
		"prompt": "test",
	})

	// Delete it
	result := mcpCall(t, server, "delete_prompt", map[string]interface{}{
		"name": "to_delete",
	})

	if result["error"] != nil {
		t.Fatalf("delete_prompt returned error: %v", result["error"])
	}

	// Verify it's gone
	if _, ok := getPrompt("to_delete"); ok {
		t.Error("prompt still exists after delete")
	}
}

func TestMCPDeletePromptBuiltinFails(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "delete_prompt", map[string]interface{}{
		"name": "default",
	})

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for deleting built-in prompt")
	}
	if !strings.Contains(errObj["message"].(string), "built-in") {
		t.Errorf("error message: %v", errObj["message"])
	}
}

// --- MCP vision preset tests ---

func TestMCPListVisionPresets(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "list_vision_presets", map[string]interface{}{})

	if result["error"] != nil {
		t.Fatalf("list_vision_presets returned error: %v", result["error"])
	}
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "lemonade") {
		t.Errorf("text doesn't contain 'lemonade': %s", text)
	}
	if !strings.Contains(text, "Active preset:") {
		t.Errorf("text doesn't show active preset: %s", text)
	}
}

func TestMCPSetVisionPreset(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "set_vision_preset", map[string]interface{}{
		"preset": "ollama",
	})

	if result["error"] != nil {
		t.Fatalf("set_vision_preset returned error: %v", result["error"])
	}
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "ollama") {
		t.Errorf("text doesn't contain 'ollama': %s", text)
	}

	if visionConfig.ActivePreset != "ollama" {
		t.Errorf("active preset = %q, expected 'ollama'", visionConfig.ActivePreset)
	}
}

func TestMCPSetVisionPresetNotFound(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "set_vision_preset", map[string]interface{}{
		"preset": "nonexistent",
	})

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for non-existent preset")
	}
	if !strings.Contains(errObj["message"].(string), "not found") {
		t.Errorf("error message: %v", errObj["message"])
	}
}

func TestMCPTestVisionPreset(t *testing.T) {
	// Use a mock LLM that responds to the test request (no image needed)
	dir := setupTestDir(t)
	baseURL = "http://test.local"
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()
	visionConfigFile = filepath.Join(dir, visionConfigFileName)
	os.Unsetenv("VISION_ENDPOINT")
	os.Unsetenv("VISION_MODEL")
	os.Unsetenv("VISION_ENABLED")
	visionConfig = VisionConfigFile{}
	initVisionConfig()

	// Mock LLM for the test request (text-only, no image)
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	t.Cleanup(mockLLM.Close)

	// Update the lemonade preset to point at our mock
	updateVisionPreset("lemonade", mockLLM.URL+"/v1/chat/completions", "test-model", "", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		teardownTestDir(t, dir)
	})

	result := mcpCall(t, server, "test_vision_preset", map[string]interface{}{
		"preset": "lemonade",
	})

	if result["error"] != nil {
		t.Fatalf("test_vision_preset returned error: %v", result["error"])
	}
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "OK") {
		t.Errorf("text doesn't contain 'OK': %s", text)
	}
	if !strings.Contains(text, "Latency:") {
		t.Errorf("text doesn't contain latency: %s", text)
	}
}

func TestMCPTestVisionPresetActive(t *testing.T) {
	dir := setupTestDir(t)
	baseURL = "http://test.local"
	promptsFile = filepath.Join(dir, promptsFileName)
	prompts = make(map[string]*VisionPrompt)
	initPrompts()
	visionConfigFile = filepath.Join(dir, visionConfigFileName)
	os.Unsetenv("VISION_ENDPOINT")
	os.Unsetenv("VISION_MODEL")
	os.Unsetenv("VISION_ENABLED")
	visionConfig = VisionConfigFile{}
	initVisionConfig()

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	t.Cleanup(mockLLM.Close)

	updateVisionPreset("lemonade", mockLLM.URL+"/v1/chat/completions", "test-model", "", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		teardownTestDir(t, dir)
	})

	// No preset specified — should test the active one (lemonade)
	result := mcpCall(t, server, "test_vision_preset", map[string]interface{}{})

	if result["error"] != nil {
		t.Fatalf("test_vision_preset returned error: %v", result["error"])
	}
	text := result["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "OK") {
		t.Errorf("text doesn't contain 'OK': %s", text)
	}
}

func TestMCPTestVisionPresetNotFound(t *testing.T) {
	server, _, _ := setupVisionTestServer(t, `{"image_type":"test","text":"test","description":"test"}`)

	result := mcpCall(t, server, "test_vision_preset", map[string]interface{}{
		"preset": "nonexistent",
	})

	errObj, hasErr := result["error"].(map[string]interface{})
	if !hasErr {
		t.Fatal("expected error for non-existent preset")
	}
	if !strings.Contains(errObj["message"].(string), "not found") {
		t.Errorf("error message: %v", errObj["message"])
	}
}
