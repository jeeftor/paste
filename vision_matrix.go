package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MatrixCell is the result of one (preset × image × prompt) combination.
type MatrixCell struct {
	Success    bool   `json:"success"`
	DurationMs int64  `json:"duration_ms"`
	Preview    string `json:"preview,omitempty"` // first 200 chars of extracted text
	Error      string `json:"error,omitempty"`
}

// MatrixPresetResult holds all cells for one preset.
type MatrixPresetResult struct {
	Preset    string                            `json:"preset"`
	Model     string                            `json:"model"`
	Endpoint  string                            `json:"endpoint"`
	Cells     map[string]map[string]*MatrixCell `json:"cells"` // [image_type][prompt_name]
	TotalRuns int                               `json:"total_runs"`
	Successes int                               `json:"successes"`
}

// MatrixResult is the full response from POST /api/vision/test-matrix.
type MatrixResult struct {
	Presets    []string             `json:"presets"`
	ImageTypes []string             `json:"image_types"`
	Prompts    []string             `json:"prompts"`
	Results    []MatrixPresetResult `json:"results"`
	TotalCells int                  `json:"total_cells"`
	Successes  int                  `json:"successes"`
}

var allSampleImageTypes = []string{"terminal", "code", "document", "diagram", "screenshot"}

// apiVisionTestMatrixHandler handles POST /api/vision/test-matrix.
// Runs every configured preset × every built-in sample image × every configured
// prompt. Presets are tested sequentially (one model at a time) to avoid
// overwhelming local LLMs with concurrent load; prompts × images within a
// preset are parallelised. After each preset is done, an Ollama-compatible
// keep_alive=0 request is attempted to unload the model from memory.
func apiVisionTestMatrixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !visionEnabled {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "vision processing is disabled",
		})
		return
	}

	presets := listVisionPresets()
	prompts := listPromptsSorted()

	if len(presets) == 0 {
		http.Error(w, `{"error":"no presets configured"}`, http.StatusBadRequest)
		return
	}
	if len(prompts) == 0 {
		http.Error(w, `{"error":"no prompts configured"}`, http.StatusBadRequest)
		return
	}

	imageTypes := allSampleImageTypes
	promptNames := make([]string, len(prompts))
	for i, p := range prompts {
		promptNames[i] = p.Name
	}
	presetNames := make([]string, len(presets))
	for i, p := range presets {
		presetNames[i] = p.Name
	}

	totalCells := len(presets) * len(imageTypes) * len(prompts)
	log.Printf("vision matrix: %d presets × %d images × %d prompts = %d cells",
		len(presets), len(imageTypes), len(prompts), totalCells)

	var allResults []MatrixPresetResult
	totalSuccesses := 0

	// Write all sample images to temp files once (shared across presets).
	type tempImg struct {
		id   string
		path string
	}
	tempImgs := make(map[string]tempImg, len(imageTypes))
	for _, imgType := range imageTypes {
		data, _ := getSampleImage(imgType)
		id := fmt.Sprintf("matrix_%s_%d", imgType, time.Now().UnixNano())
		path := filepath.Join(dataDir, fileDir, id)
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, data, 0644); err != nil {
			log.Printf("vision matrix: failed to write temp image %s: %v", imgType, err)
			continue
		}
		tempImgs[imgType] = tempImg{id: id, path: path}
	}
	defer func() {
		for _, t := range tempImgs {
			os.Remove(t.path)
		}
	}()

	// Process presets sequentially.
	for _, preset := range presets {
		log.Printf("vision matrix: running preset %q (%s)", preset.Name, preset.Model)
		pr := MatrixPresetResult{
			Preset:   preset.Name,
			Model:    preset.Model,
			Endpoint: preset.Endpoint,
			Cells:    make(map[string]map[string]*MatrixCell),
		}

		// Parallel execution within this preset.
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, imgType := range imageTypes {
			for _, prompt := range prompts {
				t, ok := tempImgs[imgType]
				if !ok {
					continue
				}
				wg.Add(1)
				go func(imgT string, p *VisionPrompt, tmpID string) {
					defer wg.Done()
					cell := runMatrixCell(tmpID, preset, p)
					mu.Lock()
					if pr.Cells[imgT] == nil {
						pr.Cells[imgT] = make(map[string]*MatrixCell)
					}
					pr.Cells[imgT][p.Name] = cell
					pr.TotalRuns++
					if cell.Success {
						pr.Successes++
					}
					mu.Unlock()
				}(imgType, prompt, t.id) // prompt is *VisionPrompt from listPromptsSorted
			}
		}
		wg.Wait()

		allResults = append(allResults, pr)
		totalSuccesses += pr.Successes

		// Attempt to unload the model from memory (Ollama-compatible).
		go tryUnloadModel(preset)
	}

	writeJSON(w, MatrixResult{
		Presets:    presetNames,
		ImageTypes: imageTypes,
		Prompts:    promptNames,
		Results:    allResults,
		TotalCells: totalCells,
		Successes:  totalSuccesses,
	})
}

// runMatrixCell runs one (preset × image × prompt) and returns a MatrixCell.
func runMatrixCell(tmpID string, preset *VisionPreset, prompt *VisionPrompt) *MatrixCell {
	start := time.Now()
	var result PresetResult
	if prompt.Mode == "scan" {
		result = analyzeWithPresetScan(tmpID, preset, prompt.Prompt)
	} else {
		result = analyzeWithPreset(tmpID, preset, prompt.Prompt)
	}
	elapsed := time.Since(start).Milliseconds()

	cell := &MatrixCell{
		Success:    result.Success,
		DurationMs: elapsed,
	}
	if !result.Success {
		cell.Error = result.Error
	} else {
		text := result.Text
		if len(text) > 200 {
			text = text[:200]
		}
		cell.Preview = text
	}
	return cell
}

// tryUnloadModel sends an Ollama-compatible keep_alive=0 request to ask the
// server to evict the model from memory. Fails silently — not all backends
// support this.
func tryUnloadModel(preset *VisionPreset) {
	body := map[string]interface{}{
		"model":      preset.Model,
		"messages":   []interface{}{},
		"keep_alive": 0,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", preset.Endpoint, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if preset.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+preset.APIKey)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	log.Printf("vision matrix: unload signal sent to %q (HTTP %d)", preset.Name, resp.StatusCode)
}
