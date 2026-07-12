package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// visionChatRequest is the OpenAI-compatible chat completion request
type visionChatRequest struct {
	Model     string              `json:"model"`
	Messages  []visionChatMessage `json:"messages"`
	MaxTokens int                 `json:"max_tokens"`
}

type visionChatMessage struct {
	Role    string           `json:"role"`
	Content []visionContent `json:"content"`
}

type visionContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ImageURL  *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

type visionChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type visionAnalysisResult struct {
	ImageType   string `json:"image_type"`
	Text        string `json:"text"`
	Description string `json:"description"`
}

// analyzeImageAsync runs vision analysis in a background goroutine using the default prompt.
func analyzeImageAsync(itemID string) {
	analyzeImageAsyncWithPrompt(itemID, "default")
}

// analyzeImageAsyncWithPrompt runs vision analysis with a specific prompt in the background.
func analyzeImageAsyncWithPrompt(itemID, promptName string) {
	prompt, ok := getPrompt(promptName)
	if !ok {
		log.Printf("Vision analysis: prompt %q not found for item %s", promptName, itemID)
		return
	}

	// Mark as pending
	updateItem(itemID, func(item *Item) bool {
		if item.Analyses == nil {
			item.Analyses = make(map[string]*ItemAnalysis)
		}
		item.Analyses[promptName] = &ItemAnalysis{
			Status:     "pending",
			Backend:    visionModel,
			PromptName: promptName,
		}
		return true
	})

	result, err := analyzeImage(itemID, prompt.Prompt)
	if err != nil {
		log.Printf("Vision analysis failed for %s [%s]: %v", itemID, promptName, err)
		updateItem(itemID, func(item *Item) bool {
			if item.Analyses == nil {
				item.Analyses = make(map[string]*ItemAnalysis)
			}
			item.Analyses[promptName] = &ItemAnalysis{
				Status:     "failed",
				Backend:    visionModel,
				PromptName: promptName,
				Error:      err.Error(),
			}
			return true
		})
		return
	}

	updateItem(itemID, func(item *Item) bool {
		if item.Analyses == nil {
			item.Analyses = make(map[string]*ItemAnalysis)
		}
		item.Analyses[promptName] = &ItemAnalysis{
			Status:      "complete",
			Text:        result.Text,
			Description: result.Description,
			Backend:     visionModel,
			PromptName:  promptName,
			ProcessedAt: time.Now(),
		}
		return true
	})
	log.Printf("Vision analysis complete for %s [%s]: type=%s, %d chars extracted",
		itemID, promptName, result.ImageType, len(result.Text))
}

// analyzeImage reads the image file, sends it to the vision model with the given prompt, and parses the response.
func analyzeImage(itemID, promptText string) (*visionAnalysisResult, error) {
	fpath := filepath.Join(dataDir, fileDir, itemID)
	imgData, err := os.ReadFile(fpath)
	if err != nil {
		return nil, fmt.Errorf("failed to read image file: %w", err)
	}

	const maxVisionImageSize = 10 * 1024 * 1024
	if len(imgData) > maxVisionImageSize {
		return nil, fmt.Errorf("image too large for vision processing (%d bytes, max %d)", len(imgData), maxVisionImageSize)
	}

	b64 := base64.StdEncoding.EncodeToString(imgData)
	dataURL := fmt.Sprintf("data:image/png;base64,%s", b64)

	reqBody := visionChatRequest{
		Model: visionModel,
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

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", visionEndpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vision API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("vision API returned %d: %s", resp.StatusCode, string(body))
	}

	var chatResp visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode vision response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("vision API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("vision API returned no choices")
	}

	content := chatResp.Choices[0].Message.Content
	content = stripMarkdownCodeFence(content)

	var result visionAnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return &visionAnalysisResult{
			ImageType:   "other",
			Text:        content,
			Description: "Raw model output (JSON parsing failed)",
		}, nil
	}

	return &result, nil
}

// stripMarkdownCodeFence removes ```json ... ``` wrapping if present
func stripMarkdownCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// apiAnalyzeHandler handles POST /api/analyze/{id}?prompt={name}
func apiAnalyzeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/analyze/")
	if id == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}

	// Optional prompt parameter (defaults to "default")
	promptName := r.URL.Query().Get("prompt")
	if promptName == "" {
		promptName = "default"
	}

	item, ok := findItem(id)
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	if item.Type != "file" || !strings.HasPrefix(item.MimeType, "image/") {
		http.Error(w, `{"error":"item is not an image"}`, http.StatusBadRequest)
		return
	}

	if !visionEnabled {
		http.Error(w, `{"error":"vision processing is disabled"}`, http.StatusServiceUnavailable)
		return
	}

	prompt, ok := getPrompt(promptName)
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"prompt %q not found"}`, promptName), http.StatusBadRequest)
		return
	}

	// Run analysis synchronously for manual trigger
	result, err := analyzeImage(id, prompt.Prompt)
	if err != nil {
		updateItem(id, func(it *Item) bool {
			if it.Analyses == nil {
				it.Analyses = make(map[string]*ItemAnalysis)
			}
			it.Analyses[promptName] = &ItemAnalysis{
				Status:     "failed",
				Backend:    visionModel,
				PromptName: promptName,
				Error:      err.Error(),
			}
			return true
		})
		http.Error(w, fmt.Sprintf(`{"error":"analysis failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	updateItem(id, func(it *Item) bool {
		if it.Analyses == nil {
			it.Analyses = make(map[string]*ItemAnalysis)
		}
		it.Analyses[promptName] = &ItemAnalysis{
			Status:      "complete",
			Text:        result.Text,
			Description: result.Description,
			Backend:     visionModel,
			PromptName:  promptName,
			ProcessedAt: time.Now(),
		}
		return true
	})

	updated, _ := findItem(id)
	writeJSON(w, map[string]interface{}{
		"id":       id,
		"prompt":   promptName,
		"analysis": updated.Analyses[promptName],
	})
}
