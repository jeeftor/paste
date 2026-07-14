package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MatrixCell is the result of one (preset × image × prompt) combination.
type MatrixCell struct {
	Success    bool                  `json:"success"`
	DurationMs int64                 `json:"duration_ms"`
	Preview    string                `json:"preview,omitempty"` // first 200 chars of extracted text
	Error      string                `json:"error,omitempty"`
	Failure    *MatrixFailureDetails `json:"failure,omitempty"`
}

// MatrixFailureDetails records the runtime state immediately after a failed cell.
type MatrixFailureDetails struct {
	CapturedAt      string            `json:"captured_at"`
	HTTPStatus      int               `json:"http_status,omitempty"`
	HTTPStatusText  string            `json:"http_status_text,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	Runtime         runtimeStatus     `json:"runtime"`
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

// MatrixResult is the final summary sent in the "done" SSE event.
type MatrixResult struct {
	Presets    []string             `json:"presets"`
	ImageTypes []string             `json:"image_types"`
	Prompts    []string             `json:"prompts"`
	Results    []MatrixPresetResult `json:"results"`
	TotalCells int                  `json:"total_cells"`
	Successes  int                  `json:"successes"`
}

type matrixEvent struct {
	eventType string
	data      []byte
}

// matrixRun keeps a replayable in-memory event stream for the active matrix.
// A browser reload can subscribe again without starting duplicate model work.
type matrixRun struct {
	mu          sync.Mutex
	events      []matrixEvent
	subscribers map[chan matrixEvent]struct{}
	running     bool
}

func newMatrixRun() *matrixRun {
	return &matrixRun{
		subscribers: make(map[chan matrixEvent]struct{}),
		running:     true,
	}
}

func (run *matrixRun) emit(eventType string, data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("vision matrix event encoding failed", "event", eventType, "error", err)
		return
	}

	event := matrixEvent{eventType: eventType, data: payload}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.events = append(run.events, event)
	for subscriber := range run.subscribers {
		subscriber <- event
	}
}

func (run *matrixRun) subscribe() (<-chan matrixEvent, func()) {
	subscriber := make(chan matrixEvent, 512)
	run.mu.Lock()
	for _, event := range run.events {
		subscriber <- event
	}
	if run.running {
		run.subscribers[subscriber] = struct{}{}
	} else {
		close(subscriber)
	}
	run.mu.Unlock()

	return subscriber, func() {
		run.mu.Lock()
		delete(run.subscribers, subscriber)
		run.mu.Unlock()
	}
}

func (run *matrixRun) finish() {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.running = false
	for subscriber := range run.subscribers {
		close(subscriber)
		delete(run.subscribers, subscriber)
	}
}

func (run *matrixRun) isRunning() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.running
}

var (
	matrixRunMu     sync.Mutex
	activeMatrixRun *matrixRun
)

// matrixConcurrency keeps matrix results comparable and avoids competing for model memory.
const matrixConcurrency = 1

// modelWarmupTimeout is how long we wait for a model to load on first request.
var modelWarmupTimeout = 120 * time.Second

var allSampleImageTypes = []string{"terminal", "code", "document", "diagram", "screenshot"}

// apiVisionTestMatrixHandler starts one matrix or reconnects a client to the
// active matrix's replayable SSE stream.
// GET /api/vision/test-matrix
func apiVisionTestMatrixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if !visionEnabled {
		writeMatrixError(w, "vision processing is disabled")
		return
	}

	presets := listVisionPresets()
	prompts := listPromptsSorted()

	if len(presets) == 0 {
		writeMatrixError(w, "no presets configured")
		return
	}
	if len(prompts) == 0 {
		writeMatrixError(w, "no prompts configured")
		return
	}

	imageTypes := allSampleImageTypes
	run, started := getOrStartMatrixRun(presets, prompts, imageTypes)
	if !started {
		slog.Info("vision matrix client reconnected to active run")
	}
	streamMatrixEvents(w, r, run)
}

// apiVisionTestMatrixStatusHandler reports whether a matrix can be resumed.
// GET /api/vision/test-matrix/status
func apiVisionTestMatrixStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	matrixRunMu.Lock()
	run := activeMatrixRun
	matrixRunMu.Unlock()
	writeJSON(w, map[string]bool{"running": run != nil && run.isRunning()})
}

func getOrStartMatrixRun(presets []*VisionPreset, prompts []*VisionPrompt, imageTypes []string) (*matrixRun, bool) {
	matrixRunMu.Lock()
	defer matrixRunMu.Unlock()
	if activeMatrixRun != nil && activeMatrixRun.isRunning() {
		return activeMatrixRun, false
	}

	run := newMatrixRun()
	activeMatrixRun = run
	go func() {
		runVisionMatrix(run, presets, prompts, imageTypes)
		run.finish()
		matrixRunMu.Lock()
		if activeMatrixRun == run {
			activeMatrixRun = nil
		}
		matrixRunMu.Unlock()
	}()
	return run, true
}

func writeMatrixError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", message)
}

func streamMatrixEvents(w http.ResponseWriter, r *http.Request, run *matrixRun) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsubscribe := run.subscribe()
	defer unsubscribe()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.eventType, event.data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func runVisionMatrix(run *matrixRun, presets []*VisionPreset, prompts []*VisionPrompt, imageTypes []string) {
	sendEvent := run.emit
	promptNames := make([]string, len(prompts))
	for i, p := range prompts {
		promptNames[i] = p.Name
	}
	presetNames := make([]string, len(presets))
	for i, p := range presets {
		presetNames[i] = p.Name
	}

	totalCells := len(presets) * len(imageTypes) * len(prompts)
	slog.Info("vision matrix started",
		"presets", len(presets),
		"images", len(imageTypes),
		"prompts", len(prompts),
		"total_cells", totalCells,
		"cell_concurrency", matrixConcurrency,
	)

	// Send the initial metadata so the UI can render the skeleton.
	sendEvent("start", map[string]interface{}{
		"total_cells": totalCells,
		"presets":     presetNames,
		"image_types": imageTypes,
		"prompts":     promptNames,
	})

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
			slog.Error("vision matrix sample image preparation failed", "image", imgType, "error", err)
			continue
		}
		tempImgs[imgType] = tempImg{id: id, path: path}
	}
	defer func() {
		for _, t := range tempImgs {
			os.Remove(t.path)
		}
	}()

	var allResults []MatrixPresetResult
	totalSuccesses := 0

	for pi, preset := range presets {
		cellsPerPreset := len(imageTypes) * len(prompts)
		presetStarted := time.Now()
		slog.Info("vision matrix loading model",
			"preset", preset.Name,
			"model", preset.Model,
			"preset_index", pi+1,
			"preset_total", len(presets),
			"cells", cellsPerPreset,
		)

		sendEvent("preset_start", map[string]interface{}{
			"preset":      preset.Name,
			"model":       preset.Model,
			"index":       pi,
			"total":       len(presets),
			"total_cells": cellsPerPreset,
		})

		// Warmup: wait for the model to load before firing parallel cells.
		modelReady := warmupModel(preset, func(attempt, maxAttempts int, err string) {
			slog.Warn("vision matrix waiting for model",
				"preset", preset.Name,
				"model", preset.Model,
				"attempt", attempt,
				"max_attempts", maxAttempts,
				"error", err,
			)
			sendEvent("model_wait", map[string]interface{}{
				"preset":       preset.Name,
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"error":        err,
			})
		})

		if !modelReady {
			slog.Error("vision matrix model failed to load; skipping preset",
				"preset", preset.Name,
				"model", preset.Model,
				"elapsed_ms", time.Since(presetStarted).Milliseconds(),
				"skipped_cells", cellsPerPreset,
			)
			sendEvent("preset_failed", map[string]interface{}{
				"preset":      preset.Name,
				"message":     "model did not become ready within timeout",
				"duration_ms": time.Since(presetStarted).Milliseconds(),
			})
			// Still add a result so the UI shows 0/N for this preset.
			pr := MatrixPresetResult{
				Preset:   preset.Name,
				Model:    preset.Model,
				Endpoint: preset.Endpoint,
				Cells:    make(map[string]map[string]*MatrixCell),
			}
			for _, imgType := range imageTypes {
				for _, p := range prompts {
					if pr.Cells[imgType] == nil {
						pr.Cells[imgType] = make(map[string]*MatrixCell)
					}
					pr.Cells[imgType][p.Name] = &MatrixCell{Error: "model warmup failed"}
					pr.TotalRuns++
					sendEvent("cell_complete", map[string]interface{}{
						"preset": preset.Name, "image": imgType, "prompt": p.Name,
						"success": false, "duration_ms": int64(0), "error": "model warmup failed",
					})
				}
			}
			allResults = append(allResults, pr)
			continue
		}

		warmupDuration := time.Since(presetStarted).Milliseconds()
		slog.Info("vision matrix model ready; running tests",
			"preset", preset.Name,
			"model", preset.Model,
			"warmup_ms", warmupDuration,
			"cells", cellsPerPreset,
			"cell_concurrency", matrixConcurrency,
		)
		sendEvent("model_ready", map[string]interface{}{
			"preset":      preset.Name,
			"model":       preset.Model,
			"duration_ms": warmupDuration,
		})

		pr := MatrixPresetResult{
			Preset:   preset.Name,
			Model:    preset.Model,
			Endpoint: preset.Endpoint,
			Cells:    make(map[string]map[string]*MatrixCell),
		}

		cellNumber := 0
		for _, imgType := range imageTypes {
			for _, prompt := range prompts {
				t, ok := tempImgs[imgType]
				if !ok {
					continue
				}
				cellNumber++
				overallCell := pi*cellsPerPreset + cellNumber

				slog.Info("vision matrix cell started", "preset", preset.Name, "preset_index", pi+1, "preset_total", len(presets), "cell", cellNumber, "cell_total", cellsPerPreset, "overall_cell", overallCell, "overall_total", totalCells, "image", imgType, "prompt", prompt.Name)
				sendEvent("cell_start", map[string]interface{}{"preset": preset.Name, "image": imgType, "prompt": prompt.Name, "cell": cellNumber, "cell_total": cellsPerPreset, "overall_cell": overallCell, "overall_total": totalCells})

				cell := runMatrixCell(t.id, preset, prompt)

				if pr.Cells[imgType] == nil {
					pr.Cells[imgType] = make(map[string]*MatrixCell)
				}
				pr.Cells[imgType][prompt.Name] = cell
				pr.TotalRuns++
				if cell.Success {
					pr.Successes++
				}

				if cell.Success {
					slog.Info("vision matrix cell passed", "preset", preset.Name, "cell", cellNumber, "cell_total", cellsPerPreset, "overall_cell", overallCell, "image", imgType, "prompt", prompt.Name, "duration_ms", cell.DurationMs)
				} else {
					slog.Error("vision matrix cell failed", "preset", preset.Name, "cell", cellNumber, "cell_total", cellsPerPreset, "overall_cell", overallCell, "image", imgType, "prompt", prompt.Name, "duration_ms", cell.DurationMs, "error", cell.Error)
				}

				sendEvent("cell_complete", map[string]interface{}{"preset": preset.Name, "image": imgType, "prompt": prompt.Name, "cell": cellNumber, "cell_total": cellsPerPreset, "overall_cell": overallCell, "overall_total": totalCells, "success": cell.Success, "duration_ms": cell.DurationMs, "error": cell.Error, "failure": cell.Failure})
			}
		}

		allResults = append(allResults, pr)
		totalSuccesses += pr.Successes
		presetDuration := time.Since(presetStarted).Milliseconds()
		slog.Info("vision matrix preset complete",
			"preset", preset.Name,
			"model", preset.Model,
			"successes", pr.Successes,
			"total", pr.TotalRuns,
			"duration_ms", presetDuration,
		)

		sendEvent("preset_complete", map[string]interface{}{
			"preset":      preset.Name,
			"successes":   pr.Successes,
			"total":       pr.TotalRuns,
			"duration_ms": presetDuration,
		})

		// Ask the runtime to unload before moving to the next preset. A successful
		// response acknowledges the request; only the runtime can confirm memory release.
		slog.Info("vision matrix requesting model unload", "preset", preset.Name, "model", preset.Model)
		sendEvent("model_unloading", map[string]interface{}{
			"preset": preset.Name,
			"model":  preset.Model,
		})
		unloadStarted := time.Now()
		status, err := tryUnloadModel(preset)
		if err != nil {
			slog.Warn("vision matrix model unload request failed",
				"preset", preset.Name,
				"model", preset.Model,
				"status", status,
				"duration_ms", time.Since(unloadStarted).Milliseconds(),
				"error", err,
			)
			sendEvent("model_unload_failed", map[string]interface{}{
				"preset":  preset.Name,
				"status":  status,
				"message": err.Error(),
			})
			continue
		}
		confirmed := waitForModelUnload(preset, modelUnloadTimeout, func(elapsed time.Duration) {
			sendEvent("model_unload_wait", map[string]interface{}{"preset": preset.Name, "elapsed_ms": elapsed.Milliseconds(), "timeout_ms": modelUnloadTimeout.Milliseconds()})
		})
		slog.Info("vision matrix model unload acknowledged",
			"preset", preset.Name,
			"model", preset.Model,
			"status", status,
			"duration_ms", time.Since(unloadStarted).Milliseconds(),
		)
		sendEvent("model_unloaded", map[string]interface{}{
			"preset":            preset.Name,
			"status":            status,
			"duration_ms":       time.Since(unloadStarted).Milliseconds(),
			"total_duration_ms": time.Since(presetStarted).Milliseconds(),
			"confirmed":         confirmed,
		})
	}

	slog.Info("vision matrix complete", "successes", totalSuccesses, "total_cells", totalCells)

	sendEvent("done", MatrixResult{
		Presets:    presetNames,
		ImageTypes: imageTypes,
		Prompts:    promptNames,
		Results:    allResults,
		TotalCells: totalCells,
		Successes:  totalSuccesses,
	})
}

// warmupModel sends a lightweight request to the preset to ensure the model
// is loaded before we fire parallel cells. Returns true if the model responded
// successfully within the timeout.
func warmupModel(preset *VisionPreset, onWait func(attempt, max int, errMsg string)) bool {
	const maxAttempts = 8
	const retryDelay = 5 * time.Second

	reqBody := map[string]interface{}{
		"model":      preset.Model,
		"messages":   []map[string]string{{"role": "user", "content": "Ready?"}},
		"max_tokens": 5,
	}
	bodyJSON, _ := json.Marshal(reqBody)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest("POST", preset.Endpoint, bytes.NewReader(bodyJSON))
		if err != nil {
			onWait(attempt, maxAttempts, err.Error())
			time.Sleep(retryDelay)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if preset.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+preset.APIKey)
		}

		client := &http.Client{Timeout: modelWarmupTimeout}
		resp, err := client.Do(req)
		if err != nil {
			onWait(attempt, maxAttempts, err.Error())
			time.Sleep(retryDelay)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return true
		}
		onWait(attempt, maxAttempts, fmt.Sprintf("HTTP %d", resp.StatusCode))
		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}
	return false
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
		cell.Failure = newMatrixFailureDetails(preset, result)
	} else {
		text := result.Text
		if len(text) > 200 {
			text = text[:200]
		}
		cell.Preview = text
	}
	return cell
}

func newMatrixFailureDetails(preset *VisionPreset, result PresetResult) *MatrixFailureDetails {
	request, _ := http.NewRequest(http.MethodGet, "/api/vision/runtime", nil)
	return &MatrixFailureDetails{
		CapturedAt:      time.Now().UTC().Format(time.RFC3339),
		HTTPStatus:      result.HTTPStatus,
		HTTPStatusText:  result.HTTPStatusText,
		ResponseHeaders: result.ResponseHeaders,
		Runtime:         probeRuntime(request, preset),
	}
}

// tryUnloadModel sends an Ollama-compatible keep_alive=0 request to ask the
// server to evict the model from memory. A successful response only confirms
// that the runtime accepted the request.
func tryUnloadModel(preset *VisionPreset) (int, error) {
	// Lemonade exposes an explicit lifecycle endpoint. Fall back to the
	// Ollama-compatible keep_alive request for other OpenAI-compatible servers.
	if strings.Contains(strings.ToLower(preset.Endpoint), "lemonade") {
		lemonadeBody, _ := json.Marshal(map[string]string{"model_name": preset.Model})
		lemonadeURL := strings.TrimSuffix(preset.Endpoint, "/v1/chat/completions") + "/v1/unload"
		if status, _, err := postUnload(lemonadeURL, lemonadeBody, preset.APIKey); status != http.StatusNotFound {
			return status, err
		}
	}
	body := map[string]interface{}{
		"model":      preset.Model,
		"messages":   []interface{}{},
		"keep_alive": 0,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("marshal unload request: %w", err)
	}
	status, _, err := postUnload(preset.Endpoint, b, preset.APIKey)
	return status, err
}

func postUnload(url string, body []byte, apiKey string) (int, bool, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, false, fmt.Errorf("create unload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: modelUnloadTimeout}).Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("send unload request: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, true, fmt.Errorf("unload request returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, true, nil
}

func waitForModelUnload(preset *VisionPreset, timeout time.Duration, onWait func(time.Duration)) bool {
	deadline := time.Now().Add(timeout)
	for {
		status := probeRuntime(&http.Request{}, preset)
		if status.Provider == "generic" {
			return false
		}
		for _, model := range status.Models {
			if model.Name == preset.Model {
				goto wait
			}
		}
		return true
	wait:
		if time.Now().After(deadline) {
			return false
		}
		onWait(time.Until(deadline))
		time.Sleep(time.Second)
	}
}
