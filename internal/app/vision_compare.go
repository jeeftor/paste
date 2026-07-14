package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// PresetResult is the analysis result from one preset
type PresetResult struct {
	Preset          string            `json:"preset"`
	Model           string            `json:"model"`
	Endpoint        string            `json:"endpoint"`
	Success         bool              `json:"success"`
	Error           string            `json:"error,omitempty"`
	Latency         string            `json:"latency"`
	ImageType       string            `json:"image_type,omitempty"`
	Text            string            `json:"text,omitempty"`
	Description     string            `json:"description,omitempty"`
	Score           float64           `json:"score"`
	Rank            int               `json:"rank"`
	JudgeRationale  string            `json:"judge_rationale,omitempty"`
	HTTPStatus      int               `json:"http_status,omitempty"`
	HTTPStatusText  string            `json:"http_status_text,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// visionRequestFailure preserves safe HTTP response details for a failed vision request.
type visionRequestFailure struct {
	StatusCode int
	Status     string
	Body       string
	Headers    map[string]string
}

func (e *visionRequestFailure) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Status)
}

func responseDebugHeaders(headers http.Header) map[string]string {
	allowed := []string{"Server", "X-Request-Id", "X-Correlation-Id", "Retry-After"}
	result := make(map[string]string)
	for _, name := range allowed {
		if value := headers.Get(name); value != "" {
			result[name] = value
		}
	}
	return result
}

func applyVisionRequestFailure(result *PresetResult, err error) bool {
	var failure *visionRequestFailure
	if !errors.As(err, &failure) {
		return false
	}
	result.HTTPStatus = failure.StatusCode
	result.HTTPStatusText = failure.Status
	result.ResponseHeaders = failure.Headers
	return true
}

// CompareResult is the full comparison response
type CompareResult struct {
	TotalPresets int            `json:"total_presets"`
	SuccessCount int            `json:"success_count"`
	JudgeUsed    string         `json:"judge_used"`
	JudgeModel   string         `json:"judge_model,omitempty"`
	Results      []PresetResult `json:"results"`
	Winner       string         `json:"winner,omitempty"`
	ImageB64     string         `json:"image_b64,omitempty"`
	PromptUsed   string         `json:"prompt_used"`
	SampleType   string         `json:"sample_type,omitempty"`
}

// apiVisionCompareHandler runs an image through all presets and ranks results
func apiVisionCompareHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if !visionEnabled {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "Vision processing is disabled",
		})
		return
	}

	// Parse request
	imageType := "terminal"
	itemID := ""
	promptName := "default"
	var itemIDProvided bool

	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil && len(bodyBytes) > 0 {
			var req struct {
				ImageType string `json:"image_type"`
				ItemID    string `json:"item_id"`
				Prompt    string `json:"prompt"`
			}
			if json.Unmarshal(bodyBytes, &req) == nil {
				if req.ImageType != "" {
					imageType = req.ImageType
				}
				if req.ItemID != "" {
					itemID = req.ItemID
					itemIDProvided = true
				}
				if req.Prompt != "" {
					promptName = req.Prompt
				}
			}
		}
	}

	// Get the image data
	var imgData []byte
	if itemIDProvided {
		item, ok := findItem(itemID)
		if !ok {
			http.Error(w, `{"error":"item not found"}`, http.StatusNotFound)
			return
		}
		if item.Type != "file" || !strings.HasPrefix(item.MimeType, "image/") {
			http.Error(w, `{"error":"item is not an image"}`, http.StatusBadRequest)
			return
		}
		var err error
		imgData, err = os.ReadFile(filepath.Join(dataDir, fileDir, itemID))
		if err != nil {
			http.Error(w, `{"error":"failed to read image file"}`, http.StatusInternalServerError)
			return
		}
	} else {
		imgData, _ = getSampleImage(imageType)
	}

	// Get the prompt
	prompt, ok := getPrompt(promptName)
	if !ok {
		prompt, ok = getPrompt("default")
		if !ok {
			http.Error(w, `{"error":"no suitable prompt found"}`, http.StatusBadRequest)
			return
		}
		promptName = "default"
	}

	// Write image to temp file for analysis
	tmpID := fmt.Sprintf("compare_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)
	if err := os.WriteFile(tmpPath, imgData, 0644); err != nil {
		http.Error(w, `{"error":"failed to write temp image"}`, http.StatusInternalServerError)
		return
	}

	// Get all presets
	presets := listVisionPresets()
	log.Printf("Vision compare: running %d presets on %s image (%s), prompt=%q", len(presets), imageType, tmpID, promptName)

	// Run all presets in parallel
	results := make([]PresetResult, len(presets))
	var wg sync.WaitGroup

	for i, p := range presets {
		wg.Add(1)
		go func(idx int, preset *VisionPreset) {
			defer wg.Done()
			results[idx] = analyzeWithPreset(tmpID, preset, prompt.Prompt)
			log.Printf("Vision compare: preset %q → success=%v, latency=%s, %d chars",
				preset.Name, results[idx].Success, results[idx].Latency, len(results[idx].Text))
		}(i, p)
	}
	wg.Wait()

	// Score results
	successCount := 0
	for i := range results {
		if results[i].Success {
			successCount++
		}
	}

	judgeUsed := "heuristic"
	judgeModel := ""

	// Try LLM pairwise judging if we have 2+ successful results
	if successCount >= 2 {
		active := getActiveVisionPreset()
		judgeModel = active.Model
		judged, err := judgeResultsPairwise(imgData, results, active)
		if err != nil {
			log.Printf("Vision compare: LLM judge failed (%v), falling back to heuristics", err)
			scoreResultsHeuristic(results)
		} else {
			results = judged
			judgeUsed = "llm-pairwise"
		}
	} else {
		scoreResultsHeuristic(results)
	}

	// Rank by score (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	for i := range results {
		results[i].Rank = i + 1
	}

	winner := ""
	if len(results) > 0 && results[0].Success {
		winner = results[0].Preset
	}

	resp := CompareResult{
		TotalPresets: len(presets),
		SuccessCount: successCount,
		JudgeUsed:    judgeUsed,
		JudgeModel:   judgeModel,
		Results:      results,
		Winner:       winner,
		PromptUsed:   promptName,
		ImageB64:     base64.StdEncoding.EncodeToString(imgData),
	}
	if !itemIDProvided {
		resp.SampleType = imageType
	}

	log.Printf("Vision compare: complete — %d/%d succeeded, winner=%q, judge=%s",
		successCount, len(presets), winner, judgeUsed)

	writeJSON(w, resp)
}

// analyzeWithPreset runs analysis on a specific preset
func analyzeWithPreset(itemID string, preset *VisionPreset, promptText string) PresetResult {
	result := PresetResult{
		Preset:   preset.Name,
		Model:    preset.Model,
		Endpoint: preset.Endpoint,
	}

	start := time.Now()

	// Read the image file
	fpath := filepath.Join(dataDir, fileDir, itemID)
	imgData, err := os.ReadFile(fpath)
	if err != nil {
		result.Error = fmt.Sprintf("failed to read image: %v", err)
		result.Latency = time.Since(start).Round(time.Millisecond).String()
		return result
	}

	b64 := base64.StdEncoding.EncodeToString(imgData)
	mimeType := http.DetectContentType(imgData)
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/png"
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	reqBody := visionChatRequest{
		Model: preset.Model,
		Messages: []visionChatMessage{
			{
				Role: "user",
				Content: []visionContent{
					{Type: "text", Text: promptText},
					{Type: "image_url", ImageURL: &visionImageURL{URL: dataURL}},
				},
			},
		},
		MaxTokens: 2000,
	}

	bodyJSON, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", preset.Endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		result.Latency = time.Since(start).Round(time.Millisecond).String()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if preset.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+preset.APIKey)
	}

	client := &http.Client{Timeout: visionRequestTimeout}
	resp, err := client.Do(req)
	result.Latency = time.Since(start).Round(time.Millisecond).String()

	if err != nil {
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		failure := &visionRequestFailure{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
			Headers:    responseDebugHeaders(resp.Header),
		}
		applyVisionRequestFailure(&result, failure)
		result.Error = failure.Error()
		return result
	}

	var chatResp visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		result.Error = fmt.Sprintf("bad response: %v", err)
		return result
	}

	if chatResp.Error != nil {
		result.Error = chatResp.Error.Message
		return result
	}

	if len(chatResp.Choices) == 0 {
		result.Error = "no choices in response"
		return result
	}

	content := stripMarkdownCodeFence(chatResp.Choices[0].Message.Content)
	var analysis visionAnalysisResult
	if err := json.Unmarshal([]byte(content), &analysis); err != nil {
		// Not JSON — use raw content
		result.Success = true
		result.ImageType = "other"
		result.Text = content
		result.Description = "Raw model output (JSON parsing failed)"
		return result
	}

	result.Success = true
	result.ImageType = analysis.ImageType
	result.Text = analysis.Text
	result.Description = analysis.Description
	return result
}

// scoreResultsHeuristic scores results based on simple heuristics
func scoreResultsHeuristic(results []PresetResult) {
	for i := range results {
		if !results[i].Success {
			results[i].Score = 0
			continue
		}
		score := 0.0

		// Text length (up to 40 points)
		textLen := len(results[i].Text)
		if textLen > 0 {
			score += min(float64(textLen)/50.0, 40.0)
		}

		// Has description (10 points)
		if results[i].Description != "" && results[i].Description != "Raw model output (JSON parsing failed)" {
			score += 10
		}

		// Has a meaningful image_type (not "other") (10 points)
		if results[i].ImageType != "" && results[i].ImageType != "other" {
			score += 10
		}

		// No refusal markers (20 points)
		lower := strings.ToLower(results[i].Text)
		if !strings.Contains(lower, "i cannot") && !strings.Contains(lower, "i can't") &&
			!strings.Contains(lower, "unable to") && !strings.Contains(lower, "i'm unable") {
			score += 20
		}

		// JSON validity (already parsed if we got here) (20 points)
		score += 20

		results[i].Score = score
	}
}

// judgeResultsPairwise uses an LLM to compare results pairwise and rank them
func judgeResultsPairwise(imgData []byte, results []PresetResult, judge *VisionPreset) ([]PresetResult, error) {
	// Collect successful results
	var successIdx []int
	for i, r := range results {
		if r.Success {
			successIdx = append(successIdx, i)
		}
	}
	if len(successIdx) < 2 {
		return results, nil
	}

	// Build a single judging prompt: show all results and ask the model to rank them
	var sb strings.Builder
	sb.WriteString("You are a judge evaluating vision OCR results. An image was analyzed by multiple vision models. ")
	sb.WriteString("Rank the results by quality (1 = best). Consider: text extraction accuracy, completeness, and usefulness.\n\n")

	for _, idx := range successIdx {
		r := results[idx]
		sb.WriteString(fmt.Sprintf("--- Result from preset %q (model: %s) ---\n", r.Preset, r.Model))
		sb.WriteString(fmt.Sprintf("Image type: %s\n", r.ImageType))
		sb.WriteString(fmt.Sprintf("Description: %s\n", r.Description))
		sb.WriteString(fmt.Sprintf("Extracted text:\n%s\n\n", r.Text))
	}

	sb.WriteString("Respond with ONLY a JSON array ranking the presets from best to worst.\n")
	sb.WriteString("Format: [{\"preset\":\"name\",\"rank\":1,\"rationale\":\"brief reason\"},...]\n")

	b64 := base64.StdEncoding.EncodeToString(imgData)
	mimeType := http.DetectContentType(imgData)
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/png"
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	reqBody := visionChatRequest{
		Model: judge.Model,
		Messages: []visionChatMessage{
			{
				Role: "user",
				Content: []visionContent{
					{Type: "text", Text: sb.String()},
					{Type: "image_url", ImageURL: &visionImageURL{URL: dataURL}},
				},
			},
		},
		MaxTokens: 1000,
	}

	bodyJSON, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", judge.Endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return results, err
	}
	req.Header.Set("Content-Type", "application/json")
	if judge.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+judge.APIKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return results, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return results, fmt.Errorf("judge returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var chatResp visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return results, err
	}

	if len(chatResp.Choices) == 0 {
		return results, fmt.Errorf("judge returned no choices")
	}

	content := stripMarkdownCodeFence(chatResp.Choices[0].Message.Content)

	// Parse the ranking
	var rankings []struct {
		Preset    string `json:"preset"`
		Rank      int    `json:"rank"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(content), &rankings); err != nil {
		return results, fmt.Errorf("failed to parse judge response: %v", err)
	}

	// Apply rankings to results
	rankMap := make(map[string]int)
	rationaleMap := make(map[string]string)
	for _, r := range rankings {
		rankMap[r.Preset] = r.Rank
		rationaleMap[r.Preset] = r.Rationale
	}

	for i := range results {
		if results[i].Success {
			if rank, ok := rankMap[results[i].Preset]; ok {
				// Higher rank number = worse. Score = inverse: (num_results - rank + 1) * 100 / num_results
				numSuccess := len(successIdx)
				results[i].Score = float64(numSuccess-rank+1) * 100.0 / float64(numSuccess)
				results[i].JudgeRationale = rationaleMap[results[i].Preset]
			} else {
				// Not ranked by judge — give it a low score
				results[i].Score = 10
			}
		}
	}

	return results, nil
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// mcpCompareVision runs the comparison via MCP
func mcpCompareVision(imageType, itemID, promptName string) (interface{}, *MCPError) {
	if !visionEnabled {
		return nil, &MCPError{Code: -32603, Message: "vision processing is disabled"}
	}

	if imageType == "" {
		imageType = "terminal"
	}
	if promptName == "" {
		promptName = "default"
	}

	// Get image data
	var imgData []byte
	if itemID != "" {
		item, ok := findItem(itemID)
		if !ok {
			return nil, &MCPError{Code: -32602, Message: "item not found"}
		}
		if item.Type != "file" || !strings.HasPrefix(item.MimeType, "image/") {
			return nil, &MCPError{Code: -32602, Message: "item is not an image"}
		}
		imgData, _ = os.ReadFile(filepath.Join(dataDir, fileDir, itemID))
	} else {
		imgData, _ = getSampleImage(imageType)
	}

	// Get prompt
	prompt, ok := getPrompt(promptName)
	if !ok {
		prompt, ok = getPrompt("default")
		if !ok {
			return nil, &MCPError{Code: -32603, Message: "no suitable prompt found"}
		}
		promptName = "default"
	}

	// Write temp image
	tmpID := fmt.Sprintf("compare_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)
	os.WriteFile(tmpPath, imgData, 0644)

	// Run all presets in parallel
	presets := listVisionPresets()
	results := make([]PresetResult, len(presets))
	var wg sync.WaitGroup

	for i, p := range presets {
		wg.Add(1)
		go func(idx int, preset *VisionPreset) {
			defer wg.Done()
			results[idx] = analyzeWithPreset(tmpID, preset, prompt.Prompt)
		}(i, p)
	}
	wg.Wait()

	// Score
	successCount := 0
	for i := range results {
		if results[i].Success {
			successCount++
		}
	}

	judgeUsed := "heuristic"
	if successCount >= 2 {
		active := getActiveVisionPreset()
		judged, err := judgeResultsPairwise(imgData, results, active)
		if err != nil {
			scoreResultsHeuristic(results)
		} else {
			results = judged
			judgeUsed = "llm-pairwise"
		}
	} else {
		scoreResultsHeuristic(results)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	for i := range results {
		results[i].Rank = i + 1
	}

	winner := ""
	if len(results) > 0 && results[0].Success {
		winner = results[0].Preset
	}

	// Format text output
	var lines []string
	lines = append(lines, fmt.Sprintf("Vision comparison: %d presets, %d succeeded, judge=%s", len(presets), successCount, judgeUsed))
	lines = append(lines, fmt.Sprintf("Winner: %s", winner))
	lines = append(lines, "")
	for _, r := range results {
		status := "OK"
		if !r.Success {
			status = "FAIL"
		}
		lines = append(lines, fmt.Sprintf("  #%d [%s] %s (%s) — score=%.1f, latency=%s", r.Rank, status, r.Preset, r.Model, r.Score, r.Latency))
		if r.Error != "" {
			lines = append(lines, fmt.Sprintf("       Error: %s", r.Error))
		}
		if r.JudgeRationale != "" {
			lines = append(lines, fmt.Sprintf("       Judge: %s", r.JudgeRationale))
		}
		if r.Success && len(r.Text) > 0 {
			preview := r.Text
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			lines = append(lines, fmt.Sprintf("       Text: %s", preview))
		}
		lines = append(lines, "")
	}

	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: strings.Join(lines, "\n")}},
	}, nil
}
