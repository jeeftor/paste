package main

import (
	"encoding/base64"
	"encoding/json"
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

// PromptTestResult is the result of running one prompt on an image
type PromptTestResult struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
	BuiltIn     bool   `json:"built_in"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	Latency     string `json:"latency"`
	ImageType   string `json:"image_type,omitempty"`
	Text        string `json:"text,omitempty"`
	TextDesc    string `json:"text_description,omitempty"`
	CharCount   int    `json:"char_count"`
	Score       float64 `json:"score"`
	Rank        int    `json:"rank"`
}

// PromptCompareResult is the full comparison response
type PromptCompareResult struct {
	ImageType    string             `json:"image_type"`
	TotalPrompts int               `json:"total_prompts"`
	SuccessCount int               `json:"success_count"`
	Results      []PromptTestResult `json:"results"`
	Winner       string             `json:"winner,omitempty"`
	ImageB64     string             `json:"image_b64"`
}

// apiVisionComparePromptsHandler runs one image through all available prompts
func apiVisionComparePromptsHandler(w http.ResponseWriter, r *http.Request) {
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

	imageType := "terminal"
	itemID := ""
	var itemIDProvided bool

	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil && len(bodyBytes) > 0 {
			var req struct {
				ImageType string `json:"image_type"`
				ItemID    string `json:"item_id"`
			}
			if json.Unmarshal(bodyBytes, &req) == nil {
				if req.ImageType != "" {
					imageType = req.ImageType
				}
				if req.ItemID != "" {
					itemID = req.ItemID
					itemIDProvided = true
				}
			}
		}
	}

	// Get image data
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
		imgData, _ = os.ReadFile(filepath.Join(dataDir, fileDir, itemID))
	} else {
		imgData, _ = getSampleImage(imageType)
	}

	// Get all prompts
	allPrompts := listPromptsSorted()

	// Write temp image
	tmpID := fmt.Sprintf("promptcmp_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)
	os.WriteFile(tmpPath, imgData, 0644)

	log.Printf("Prompt compare: running %d prompts on %s image (%s)", len(allPrompts), imageType, tmpID)

	// Run all prompts in parallel
	results := make([]PromptTestResult, len(allPrompts))
	var wg sync.WaitGroup

	for i, p := range allPrompts {
		wg.Add(1)
		go func(idx int, prompt *VisionPrompt) {
			defer wg.Done()
			results[idx] = analyzeWithPrompt(tmpID, prompt)
			log.Printf("Prompt compare: prompt %q → success=%v, latency=%s, %d chars",
				prompt.Name, results[idx].Success, results[idx].Latency, results[idx].CharCount)
		}(i, p)
	}
	wg.Wait()

	// Score results
	successCount := 0
	for i := range results {
		if results[i].Success {
			successCount++
			score := 0.0
			// Text length (up to 40 pts)
			if results[i].CharCount > 0 {
				score += min(float64(results[i].CharCount)/50.0, 40.0)
			}
			// Image type detected correctly (30 pts)
			if results[i].ImageType == imageType {
				score += 30
			} else if results[i].ImageType != "" && results[i].ImageType != "other" {
				score += 10
			}
			// Has description (15 pts)
			if results[i].TextDesc != "" && results[i].TextDesc != "Raw model output (JSON parsing failed)" {
				score += 15
			}
			// No refusal markers (15 pts)
			lower := strings.ToLower(results[i].Text)
			if !strings.Contains(lower, "i cannot") && !strings.Contains(lower, "i can't") &&
				!strings.Contains(lower, "unable to") && !strings.Contains(lower, "i'm unable") {
				score += 15
			}
			results[i].Score = score
		} else {
			results[i].Score = 0
		}
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
		winner = results[0].Prompt
	}

	resp := PromptCompareResult{
		ImageType:    imageType,
		TotalPrompts: len(allPrompts),
		SuccessCount: successCount,
		Results:      results,
		Winner:       winner,
		ImageB64:     base64.StdEncoding.EncodeToString(imgData),
	}

	log.Printf("Prompt compare: complete — %d/%d succeeded, winner=%q",
		successCount, len(allPrompts), winner)

	writeJSON(w, resp)
}

// analyzeWithPrompt runs analysis with a specific prompt
func analyzeWithPrompt(itemID string, prompt *VisionPrompt) PromptTestResult {
	result := PromptTestResult{
		Prompt:      prompt.Name,
		Description: prompt.Description,
		BuiltIn:     prompt.BuiltIn,
	}

	start := time.Now()
	analysis, err := analyzeImage(itemID, prompt.Prompt)
	result.Latency = time.Since(start).Round(time.Millisecond).String()

	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.ImageType = analysis.ImageType
	result.Text = analysis.Text
	result.TextDesc = analysis.Description
	result.CharCount = len(analysis.Text)
	return result
}

// mcpComparePrompts runs the prompt comparison via MCP
func mcpComparePrompts(imageType, itemID string) (interface{}, *MCPError) {
	if !visionEnabled {
		return nil, &MCPError{Code: -32603, Message: "vision processing is disabled"}
	}

	if imageType == "" {
		imageType = "terminal"
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

	allPrompts := listPromptsSorted()

	tmpID := fmt.Sprintf("promptcmp_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)
	os.WriteFile(tmpPath, imgData, 0644)

	results := make([]PromptTestResult, len(allPrompts))
	var wg sync.WaitGroup

	for i, p := range allPrompts {
		wg.Add(1)
		go func(idx int, prompt *VisionPrompt) {
			defer wg.Done()
			results[idx] = analyzeWithPrompt(tmpID, prompt)
		}(i, p)
	}
	wg.Wait()

	// Score
	successCount := 0
	for i := range results {
		if results[i].Success {
			successCount++
			score := 0.0
			if results[i].CharCount > 0 {
				score += min(float64(results[i].CharCount)/50.0, 40.0)
			}
			if results[i].ImageType == imageType {
				score += 30
			} else if results[i].ImageType != "" && results[i].ImageType != "other" {
				score += 10
			}
			if results[i].TextDesc != "" && results[i].TextDesc != "Raw model output (JSON parsing failed)" {
				score += 15
			}
			lower := strings.ToLower(results[i].Text)
			if !strings.Contains(lower, "i cannot") && !strings.Contains(lower, "i can't") &&
				!strings.Contains(lower, "unable to") && !strings.Contains(lower, "i'm unable") {
				score += 15
			}
			results[i].Score = score
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	for i := range results {
		results[i].Rank = i + 1
	}

	winner := ""
	if len(results) > 0 && results[0].Success {
		winner = results[0].Prompt
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Prompt comparison on %s image: %d prompts, %d succeeded", imageType, len(allPrompts), successCount))
	lines = append(lines, fmt.Sprintf("Winner: %s", winner))
	lines = append(lines, "")
	for _, r := range results {
		status := "OK"
		if !r.Success {
			status = "FAIL"
		}
		builtIn := ""
		if r.BuiltIn {
			builtIn = " (built-in)"
		}
		lines = append(lines, fmt.Sprintf("  #%d [%s] %s%s — score=%.1f, %d chars, %s", r.Rank, status, r.Prompt, builtIn, r.Score, r.CharCount, r.Latency))
		if r.Error != "" {
			lines = append(lines, fmt.Sprintf("       Error: %s", r.Error))
		}
		if r.Success && r.TextDesc != "" {
			lines = append(lines, fmt.Sprintf("       Desc: %s", r.TextDesc))
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
