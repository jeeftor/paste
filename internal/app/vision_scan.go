package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// analyzeImageScan tiles a tall image into overlapping sections and OCRs each
// tile separately, then joins the results. For images that aren't particularly
// tall (height ≤ 2× width), it falls back to a single-pass call.
func analyzeImageScan(itemID string, preset *VisionPreset, promptText string) (*visionAnalysisResult, error) {
	fpath := filepath.Join(dataDir, fileDir, itemID)
	rawData, err := os.ReadFile(fpath)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(rawData))
	if err != nil {
		// Can't decode (unknown format, etc.) — fall back to single pass.
		log.Printf("scan: could not decode image %s, falling back to single-pass: %v", itemID, err)
		return callVisionAPIBytes(rawData, preset, promptText)
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Single-pass when image isn't tall enough to need tiling.
	if h <= w*2 {
		return callVisionAPIBytes(rawData, preset, promptText)
	}

	// Tile size: aim for roughly square tiles ≥ 512px, capped at 1024px.
	tileH := w
	if tileH < 512 {
		tileH = 512
	}
	if tileH > 1024 {
		tileH = 1024
	}
	overlap := tileH / 6 // ~17% overlap to avoid missing text at tile boundaries

	tiles := buildTiles(img, tileH, overlap)
	log.Printf("scan: image %s is %dx%d, tiling into %d sections (tileH=%d overlap=%d)",
		itemID, w, h, len(tiles), tileH, overlap)

	var texts []string
	for i, tile := range tiles {
		var buf bytes.Buffer
		if err := png.Encode(&buf, tile); err != nil {
			log.Printf("scan: tile %d encode failed: %v", i, err)
			continue
		}
		result, err := callVisionAPIBytes(buf.Bytes(), preset, promptText)
		if err != nil {
			log.Printf("scan: tile %d analysis failed: %v", i, err)
			continue
		}
		t := strings.TrimSpace(result.Text)
		if t != "" {
			texts = append(texts, t)
		}
	}

	if len(texts) == 0 {
		return nil, fmt.Errorf("scan: all %d tiles failed or returned empty text", len(tiles))
	}

	return &visionAnalysisResult{
		ImageType:   "document",
		Text:        strings.Join(texts, "\n"),
		Description: fmt.Sprintf("Tiled scan: %d sections processed, %d with content", len(tiles), len(texts)),
	}, nil
}

// analyzeWithPresetScan runs a tiled scan and returns a PresetResult (for use
// in compare and matrix pipelines).
func analyzeWithPresetScan(itemID string, preset *VisionPreset, promptText string) PresetResult {
	r := PresetResult{
		Preset:   preset.Name,
		Model:    preset.Model,
		Endpoint: preset.Endpoint,
	}
	start := time.Now()
	result, err := analyzeImageScan(itemID, preset, promptText)
	r.Latency = time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		applyVisionRequestFailure(&r, err)
		r.Error = err.Error()
		return r
	}
	r.Success = true
	r.Text = result.Text
	r.Description = result.Description
	r.ImageType = result.ImageType
	return r
}

// buildTiles slices img into overlapping vertical strips.
func buildTiles(img image.Image, tileH, overlap int) []image.Image {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	step := tileH - overlap

	var tiles []image.Image
	for y := bounds.Min.Y; y < bounds.Min.Y+h; y += step {
		bottom := y + tileH
		if bottom > bounds.Min.Y+h {
			bottom = bounds.Min.Y + h
		}
		tile := image.NewRGBA(image.Rect(0, 0, w, bottom-y))
		draw.Draw(tile, tile.Bounds(), img, image.Pt(bounds.Min.X, y), draw.Src)
		tiles = append(tiles, tile)
		if bottom >= bounds.Min.Y+h {
			break
		}
	}
	return tiles
}

// callVisionAPIBytes sends raw image bytes to a specific preset and returns
// the parsed analysis result. This is the shared low-level call used by both
// single-pass and tiled scan paths.
func callVisionAPIBytes(imgData []byte, preset *VisionPreset, promptText string) (*visionAnalysisResult, error) {
	b64 := base64.StdEncoding.EncodeToString(imgData)
	mimeType := http.DetectContentType(imgData)
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/png"
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	reqBody := visionChatRequest{
		Model: preset.Model,
		Messages: []visionChatMessage{{
			Role: "user",
			Content: []visionContent{
				{Type: "text", Text: promptText},
				{Type: "image_url", ImageURL: &visionImageURL{URL: dataURL}},
			},
		}},
		MaxTokens: 2000,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", preset.Endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if preset.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+preset.APIKey)
	}

	client := &http.Client{Timeout: visionRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, &visionRequestFailure{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
			Headers:    responseDebugHeaders(resp.Header),
		}
	}

	var chatResp visionChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if chatResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	content := stripMarkdownCodeFence(chatResp.Choices[0].Message.Content)
	var result visionAnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		// Non-JSON response — treat entire content as raw text.
		return &visionAnalysisResult{Text: content}, nil
	}
	return &result, nil
}
