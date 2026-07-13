package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed testdata/sample_terminal.png
var sampleTerminalImage []byte

//go:embed testdata/sample_code.png
var sampleCodeImage []byte

//go:embed testdata/sample_document.png
var sampleDocumentImage []byte

//go:embed testdata/sample_diagram.png
var sampleDiagramImage []byte

//go:embed testdata/sample_screenshot.png
var sampleScreenshotImage []byte

func getSampleImage(imageType string) ([]byte, string) {
	switch imageType {
	case "code":
		return sampleCodeImage, "image/png"
	case "document":
		return sampleDocumentImage, "image/png"
	case "diagram":
		return sampleDiagramImage, "image/png"
	case "screenshot":
		return sampleScreenshotImage, "image/png"
	default:
		return sampleTerminalImage, "image/png"
	}
}

// apiVisionTestHandler runs the full vision pipeline on an embedded sample image
func apiVisionTestHandler(w http.ResponseWriter, r *http.Request) {
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

	// Parse optional image_type and prompt from body
	imageType := "terminal"
	promptOverride := ""
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil && len(bodyBytes) > 0 {
			var req struct {
				ImageType string `json:"image_type"`
				Prompt    string `json:"prompt"`
			}
			if json.Unmarshal(bodyBytes, &req) == nil {
				if req.ImageType != "" {
					imageType = req.ImageType
				}
				promptOverride = req.Prompt
			}
		}
	}

	sampleData, _ := getSampleImage(imageType)

	// Use prompt override if specified, otherwise match prompt to image type
	promptName := promptOverride
	if promptName == "" {
		promptName = imageType
	}

	prompt, ok := getPrompt(promptName)
	if !ok {
		// Fall back to default
		prompt, ok = getPrompt("default")
		if !ok {
			writeJSON(w, map[string]interface{}{
				"success": false,
				"message": "no suitable prompt found",
			})
			return
		}
		promptName = "default"
	}

	preset := getActiveVisionPreset()

	// Write the sample image to the data dir so analyzeImage can read it
	tmpID := fmt.Sprintf("vision_test_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, sampleData, 0644); err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to write temp image: %v", err),
		})
		return
	}

	// Run the analysis
	start := time.Now()
	result, err := analyzeImage(tmpID, prompt.Prompt)
	latency := time.Since(start)

	if err != nil {
		writeJSON(w, map[string]interface{}{
			"success":  false,
			"message":  fmt.Sprintf("Analysis failed: %v", err),
			"latency":  latency.Round(time.Millisecond).String(),
			"preset":   preset.Name,
			"model":    preset.Model,
			"endpoint": preset.Endpoint,
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"success":      true,
		"message":      "Vision analysis completed",
		"latency":      latency.Round(time.Millisecond).String(),
		"preset":       preset.Name,
		"model":        preset.Model,
		"endpoint":     preset.Endpoint,
		"image_type":   result.ImageType,
		"prompt_used":  promptName,
		"sample_type":  imageType,
		"text":         result.Text,
		"description":  result.Description,
		"image_b64":    base64.StdEncoding.EncodeToString(sampleData),
	})
}

// mcpVisionTest runs the full vision pipeline on an embedded sample image via MCP
func mcpVisionTest(imageType string) (interface{}, *MCPError) {
	if !visionEnabled {
		return nil, &MCPError{Code: -32603, Message: "vision processing is disabled"}
	}

	if imageType == "" {
		imageType = "terminal"
	}

	sampleData, _ := getSampleImage(imageType)

	// Use matching prompt
	promptName := imageType
	prompt, ok := getPrompt(promptName)
	if !ok {
		prompt, ok = getPrompt("default")
		if !ok {
			return nil, &MCPError{Code: -32603, Message: "no suitable prompt found"}
		}
		promptName = "default"
	}

	preset := getActiveVisionPreset()

	tmpID := fmt.Sprintf("vision_test_%d", time.Now().UnixNano())
	tmpPath := filepath.Join(dataDir, fileDir, tmpID)
	os.MkdirAll(filepath.Dir(tmpPath), 0755)
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, sampleData, 0644); err != nil {
		return nil, &MCPError{Code: -32603, Message: "failed to write temp image: " + err.Error()}
	}

	start := time.Now()
	result, err := analyzeImage(tmpID, prompt.Prompt)
	latency := time.Since(start).Round(time.Millisecond)

	if err != nil {
		return MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("Vision test FAILED (%s): %v", latency, err)}},
		}, nil
	}

	text := fmt.Sprintf("Vision test OK (%s) using preset %q [%s], prompt %q:\n", latency, preset.Name, preset.Model, promptName)
	text += fmt.Sprintf("Sample image type: %s\n", imageType)
	text += fmt.Sprintf("Image type: %s\n", result.ImageType)
	text += fmt.Sprintf("Description: %s\n", result.Description)
	text += fmt.Sprintf("Extracted text:\n%s", result.Text)

	return MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: text}},
	}, nil
}
