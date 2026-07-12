package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

//go:embed manifest.json
var manifestJSON []byte

//go:embed sw.js
var swJS []byte

//go:embed icon.svg
var iconSVG []byte

var version = "dev"

const (
	defaultPort      = "8080"
	defaultDataDir   = "/data"
	defaultBaseURL   = "http://localhost:8080"
	defaultTTL       = 7 * 24 * time.Hour
	idLen            = 6
	textDir          = "text"
	fileDir          = "files"
	chunkDir         = "chunks"
	metaFile         = "metadata.json"
	defaultMaxUploadMB = 2048 // 2GB
	chunkSize          = 5 * 1024 * 1024 // 5MB chunks
	chunkStaleTime     = 1 * time.Hour   // incomplete uploads cleaned after 1h
)

var (
	maxUploadMB    int
	maxUploadBytes int64
)

var (
	dataDir  string
	baseURL  string
	meta     Metadata
	metaMu   sync.RWMutex
)

// Vision processing configuration
var (
	visionEnabled  bool
	visionEndpoint string
	visionModel    string
)

type ItemAnalysis struct {
	Status      string    `json:"status"`       // "pending", "complete", "failed"
	Text        string    `json:"text,omitempty"`
	Description string    `json:"description,omitempty"`
	Backend     string    `json:"backend,omitempty"`
	ProcessedAt time.Time `json:"processed_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type Item struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Type       string       `json:"type"` // "text" or "file"
	MimeType   string       `json:"mime_type,omitempty"`
	Size       int64        `json:"size"`
	Created    time.Time    `json:"created"`
	Expires    time.Time    `json:"expires"`
	TTL        string       `json:"ttl"`
	Persistent bool         `json:"persistent"`
	Analysis   *ItemAnalysis `json:"analysis,omitempty"`
	Content    string       `json:"content,omitempty"` // for text items in API responses
}

type Metadata struct {
	Items []Item `json:"items"`
}

func main() {
	dataDir = envOr("DATA_DIR", defaultDataDir)
	baseURL = envOr("BASE_URL", defaultBaseURL)
	port := envOr("PORT", defaultPort)

	// Parse max upload size from env
	maxUploadMB = defaultMaxUploadMB
	if v := envOr("MAX_UPLOAD_MB", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxUploadMB = n
		}
	}
	maxUploadBytes = int64(maxUploadMB) * 1024 * 1024

	// Vision processing config
	visionEndpoint = envOr("VISION_ENDPOINT", "http://localhost:13305/v1/chat/completions")
	visionModel = envOr("VISION_MODEL", "Qwen3-VL-4B-Instruct-GGUF")
	visionEnabled = envOr("VISION_ENABLED", "true") == "true"

	for _, d := range []string{textDir, fileDir, chunkDir} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0755); err != nil {
			log.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	loadMetadata()
	cleanupOrphanedMetadata()

	// Start expiry sweeper
	go sweeper()
	// Start chunk cleanup sweeper
	go chunkSweeper()

	mux := http.NewServeMux()

	// Web UI
	mux.HandleFunc("/", rootHandler)

	// PWA assets
	mux.HandleFunc("/manifest.json", manifestHandler)
	mux.HandleFunc("/sw.js", swHandler)
	mux.HandleFunc("/icon.svg", iconHandler)

	// REST API
	mux.HandleFunc("/api/files", apiFilesHandler)
	mux.HandleFunc("/api/files/", apiFileHandler)
	mux.HandleFunc("/api/analyze/", apiAnalyzeHandler)
	mux.HandleFunc("/api/text", apiTextHandler)
	mux.HandleFunc("/api/text/", apiTextItemHandler)
	mux.HandleFunc("/api/upload", apiUploadHandler)
	mux.HandleFunc("/api/upload/init", apiUploadInitHandler)
	mux.HandleFunc("/api/upload/chunk", apiUploadChunkHandler)
	mux.HandleFunc("/api/upload/status/", apiUploadStatusHandler)
	mux.HandleFunc("/api/upload/complete", apiUploadCompleteHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/version", versionHandler)

	// MCP endpoint
	mux.HandleFunc("/mcp", mcpHandler)
	mux.HandleFunc("/mcp/", mcpHandler)

	// Direct file/text access by ID
	mux.HandleFunc("/f/", directFileHandler)
	mux.HandleFunc("/t/", directTextHandler)

	log.Printf("paste server starting on :%s (data: %s, base: %s, max upload: %dMB)", port, dataDir, baseURL, maxUploadMB)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- Metadata ---

func loadMetadata() {
	metaMu.Lock()
	defer metaMu.Unlock()
	path := filepath.Join(dataDir, metaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: failed to read metadata: %v", err)
		}
		meta = Metadata{Items: []Item{}}
		return
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		log.Printf("Warning: failed to parse metadata: %v", err)
		meta = Metadata{Items: []Item{}}
	}
}

// cleanupOrphanedMetadata removes metadata entries whose files no longer exist on disk
func cleanupOrphanedMetadata() {
	metaMu.Lock()
	defer metaMu.Unlock()
	var cleaned []Item
	removed := 0
	for _, item := range meta.Items {
		var fpath string
		if item.Type == "text" {
			fpath = filepath.Join(dataDir, textDir, item.ID)
		} else {
			fpath = filepath.Join(dataDir, fileDir, item.ID)
		}
		if _, err := os.Stat(fpath); err != nil {
			log.Printf("Cleanup: removing orphaned metadata entry %s (%s) — file missing", item.ID, item.Name)
			removed++
			continue
		}
		cleaned = append(cleaned, item)
	}
	if removed > 0 {
		meta.Items = cleaned
		log.Printf("Cleanup: removed %d orphaned entries", removed)
		// Save cleaned metadata
		path := filepath.Join(dataDir, metaFile)
		data, err := json.MarshalIndent(meta, "", "  ")
		if err == nil {
			os.WriteFile(path, data, 0644)
		}
	}
}

func saveMetadata() {
	metaMu.RLock()
	defer metaMu.RUnlock()
	path := filepath.Join(dataDir, metaFile)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal metadata: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("Failed to write metadata: %v", err)
	}
}

func addItem(item Item) {
	metaMu.Lock()
	meta.Items = append(meta.Items, item)
	metaMu.Unlock()
	saveMetadata()
}

func removeItem(id string) bool {
	metaMu.Lock()
	defer metaMu.Unlock()
	for i, item := range meta.Items {
		if item.ID == id {
			meta.Items = append(meta.Items[:i], meta.Items[i+1:]...)
			return true
		}
	}
	return false
}

// updateItem modifies an item in-place by ID and saves metadata.
// The update function receives a pointer to the item; return false to abort.
func updateItem(id string, update func(*Item) bool) bool {
	metaMu.Lock()
	defer metaMu.Unlock()
	for i := range meta.Items {
		if meta.Items[i].ID == id {
			if !update(&meta.Items[i]) {
				return false
			}
			metaMu.Unlock()
			saveMetadata()
			metaMu.Lock()
			return true
		}
	}
	return false
}

func findItem(id string) (Item, bool) {
	metaMu.RLock()
	defer metaMu.RUnlock()
	for _, item := range meta.Items {
		if item.ID == id {
			return item, true
		}
	}
	return Item{}, false
}

func listItems() []Item {
	metaMu.RLock()
	defer metaMu.RUnlock()
	result := make([]Item, len(meta.Items))
	copy(result, meta.Items)
	return result
}

// --- ID generation ---

const idChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func genID() string {
	b := make([]byte, idLen)
	now := time.Now().UnixNano()
	for i := range b {
		b[i] = idChars[(now>>uint(i*5))%int64(len(idChars))]
	}
	// Mix in some randomness from time
	t := time.Now().UnixMicro()
	for i := range b {
		b[i] = idChars[(t+int64(b[i]))%int64(len(idChars))]
	}
	// Ensure uniqueness
	for {
		id := string(b)
		if _, exists := findItem(id); !exists {
			return id
		}
		t++
		b[0] = idChars[t%int64(len(idChars))]
	}
}

// --- TTL ---

func parseTTL(s string) (time.Duration, error) {
	switch s {
	case "1h":
		return time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	case "never", "":
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid TTL: %s", s)
	}
}

func ttlString(d time.Duration) string {
	switch d {
	case time.Hour:
		return "1h"
	case 24 * time.Hour:
		return "1d"
	case 7 * 24 * time.Hour:
		return "7d"
	case 30 * 24 * time.Hour:
		return "30d"
	case 0:
		return "never"
	default:
		return d.String()
	}
}

// --- Expiry sweeper ---

func sweeper() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		for _, item := range listItems() {
			if item.Persistent || item.Expires.IsZero() {
				continue
			}
			if now.After(item.Expires) {
				deleteItem(item.ID)
				log.Printf("Expired item %s (%s)", item.ID, item.Name)
			}
		}
	}
}

func deleteItem(id string) {
	item, ok := findItem(id)
	if !ok {
		return
	}
	if item.Type == "text" {
		os.Remove(filepath.Join(dataDir, textDir, id))
	} else {
		os.Remove(filepath.Join(dataDir, fileDir, id))
	}
	removeItem(id)
}

// --- Handlers ---

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	html := strings.Replace(string(indexHTML), "{{VERSION}}", version, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func manifestHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Write(manifestJSON)
}

func swHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	// Service worker should not be cached aggressively
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(swJS)
}

func iconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(iconSVG)
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, version)))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func apiFilesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	items := listItems()

	// Filter by persistent flag if query param is present
	if q := r.URL.Query().Get("persistent"); q != "" {
		wantPersistent := q == "true"
		filtered := items[:0]
		for _, item := range items {
			if item.Persistent == wantPersistent {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	// Sort by created desc
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	// Strip content from list response
	list := make([]map[string]interface{}, len(items))
	for i, item := range items {
		entry := map[string]interface{}{
			"id":         item.ID,
			"name":       item.Name,
			"type":       item.Type,
			"mime_type":  item.MimeType,
			"size":       item.Size,
			"created":    item.Created,
			"expires":    item.Expires,
			"ttl":        item.TTL,
			"persistent": item.Persistent,
			"url":        fmt.Sprintf("%s/%s/%s", baseURL, itemTypePath(item.Type), item.ID),
		}
		if item.Analysis != nil {
			entry["analysis"] = item.Analysis
		}
		list[i] = entry
	}
	writeJSON(w, map[string]interface{}{"items": list})
}

func apiFileHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/files/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	id := parts[0]

	if len(parts) >= 2 && parts[1] == "url" {
		item, ok := findItem(id)
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"url": fmt.Sprintf("%s/%s/%s", baseURL, itemTypePath(item.Type), item.ID)})
		return
	}

	item, ok := findItem(id)
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	if r.Method == http.MethodDelete {
		deleteItem(id)
		writeJSON(w, map[string]string{"status": "deleted", "id": id})
		return
	}

	if r.Method == http.MethodPatch {
		var req struct {
			Persistent *bool `json:"persistent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if req.Persistent == nil {
			http.Error(w, `{"error":"persistent field required"}`, http.StatusBadRequest)
			return
		}
		ok := updateItem(id, func(item *Item) bool {
			item.Persistent = *req.Persistent
			if *req.Persistent {
				// Persistent items never expire
				item.Expires = time.Time{}
				item.TTL = "never"
			} else {
				// Unpersisting: restore default TTL so it will eventually expire
				item.Expires = time.Now().Add(defaultTTL)
				item.TTL = ttlString(defaultTTL)
			}
			return true
		})
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		updated, _ := findItem(id)
		writeJSON(w, map[string]interface{}{
			"id":         updated.ID,
			"persistent": updated.Persistent,
			"expires":    updated.Expires,
			"ttl":        updated.TTL,
		})
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if item.Type == "text" {
		data, err := os.ReadFile(filepath.Join(dataDir, textDir, id))
		if err != nil {
			http.Error(w, `{"error":"file not found on disk"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]interface{}{
			"id":      item.ID,
			"name":    item.Name,
			"type":    "text",
			"content": string(data),
			"created": item.Created,
			"expires": item.Expires,
		})
		return
	}

	// File download
	fpath := filepath.Join(dataDir, fileDir, id)
	w.Header().Set("Content-Type", item.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", item.Name))
	http.ServeFile(w, r, fpath)
}

func apiTextHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Content string `json:"content"`
		Name    string `json:"name"`
		TTL     string `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Try form data
		req.Content = r.FormValue("content")
		req.Name = r.FormValue("name")
		req.TTL = r.FormValue("ttl")
	}

	if req.Content == "" {
		http.Error(w, `{"error":"content required"}`, http.StatusBadRequest)
		return
	}

	ttl, err := parseTTL(req.TTL)
	if err != nil {
		ttl = defaultTTL
	}

	id := genID()
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("snippet_%s.txt", time.Now().Format("20060102_150405"))
	}

	if err := os.WriteFile(filepath.Join(dataDir, textDir, id), []byte(req.Content), 0644); err != nil {
		http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
		return
	}

	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	item := Item{
		ID:      id,
		Name:    name,
		Type:    "text",
		Size:    int64(len(req.Content)),
		Created: time.Now(),
		Expires: expires,
		TTL:     ttlString(ttl),
	}
	addItem(item)

	writeJSON(w, map[string]interface{}{
		"id":   id,
		"name": name,
		"url":  fmt.Sprintf("%s/t/%s", baseURL, id),
	})
}

func apiTextItemHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/text/")
	if id == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	if _, ok := findItem(id); !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(filepath.Join(dataDir, textDir, id))
	if err != nil {
		http.Error(w, `{"error":"not found on disk"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

func apiUploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, `{"error":"upload too large or invalid"}`, http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":"file field required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	ttlStr := r.FormValue("ttl")
	ttl, err := parseTTL(ttlStr)
	if err != nil {
		ttl = defaultTTL
	}

	id := genID()
	fpath := filepath.Join(dataDir, fileDir, id)

	dst, err := os.Create(fpath)
	if err != nil {
		http.Error(w, `{"error":"failed to create file"}`, http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		os.Remove(fpath)
		http.Error(w, `{"error":"failed to write file"}`, http.StatusInternalServerError)
		return
	}

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	item := Item{
		ID:       id,
		Name:     header.Filename,
		Type:     "file",
		MimeType: mimeType,
		Size:     written,
		Created:  time.Now(),
		Expires:  expires,
		TTL:      ttlString(ttl),
	}
	addItem(item)

	// Auto-analyze images with vision model
	if visionEnabled && strings.HasPrefix(mimeType, "image/") {
		go analyzeImageAsync(id)
	}

	writeJSON(w, map[string]interface{}{
		"id":   id,
		"name": header.Filename,
		"url":  fmt.Sprintf("%s/f/%s", baseURL, id),
	})
}

// --- Chunked upload ---

type chunkUploadMeta struct {
	Filename    string    `json:"filename"`
	MimeType    string    `json:"mime_type"`
	Size        int64     `json:"size"`
	TTL         string    `json:"ttl"`
	TotalChunks int       `json:"total_chunks"`
	Created     time.Time `json:"created"`
}

func chunkUploadDir(uploadID string) string {
	return filepath.Join(dataDir, chunkDir, uploadID)
}

func chunkMetaPath(uploadID string) string {
	return filepath.Join(chunkUploadDir(uploadID), ".meta")
}

func loadChunkMeta(uploadID string) (chunkUploadMeta, bool) {
	data, err := os.ReadFile(chunkMetaPath(uploadID))
	if err != nil {
		return chunkUploadMeta{}, false
	}
	var m chunkUploadMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return chunkUploadMeta{}, false
	}
	return m, true
}

func saveChunkMeta(uploadID string, m chunkUploadMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(chunkMetaPath(uploadID), data, 0644)
}

func apiUploadInitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		Size     int64  `json:"size"`
		TTL      string `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	if req.Filename == "" {
		http.Error(w, `{"error":"filename required"}`, http.StatusBadRequest)
		return
	}
	if req.Size <= 0 {
		http.Error(w, `{"error":"size required"}`, http.StatusBadRequest)
		return
	}
	if req.Size > maxUploadBytes {
		http.Error(w, fmt.Sprintf(`{"error":"file too large (max %d MB)"}`, maxUploadMB), http.StatusBadRequest)
		return
	}

	totalChunks := int((req.Size + chunkSize - 1) / chunkSize)
	uploadID := genChunkID()

	dir := chunkUploadDir(uploadID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, `{"error":"failed to create upload dir"}`, http.StatusInternalServerError)
		return
	}

	meta := chunkUploadMeta{
		Filename:    req.Filename,
		MimeType:    req.MimeType,
		Size:        req.Size,
		TTL:         req.TTL,
		TotalChunks: totalChunks,
		Created:     time.Now(),
	}
	if err := saveChunkMeta(uploadID, meta); err != nil {
		os.RemoveAll(dir)
		http.Error(w, `{"error":"failed to save upload metadata"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"upload_id":    uploadID,
		"chunk_size":   chunkSize,
		"total_chunks": totalChunks,
	})
}

func apiUploadChunkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Limit chunk size to chunkSize + some overhead for multipart encoding
	r.Body = http.MaxBytesReader(w, r.Body, chunkSize+1024*1024)
	if err := r.ParseMultipartForm(chunkSize + 1024*1024); err != nil {
		http.Error(w, `{"error":"chunk too large or invalid"}`, http.StatusBadRequest)
		return
	}

	uploadID := r.FormValue("upload_id")
	if uploadID == "" {
		http.Error(w, `{"error":"upload_id required"}`, http.StatusBadRequest)
		return
	}

	meta, ok := loadChunkMeta(uploadID)
	if !ok {
		http.Error(w, `{"error":"upload not found or expired"}`, http.StatusNotFound)
		return
	}

	chunkIndexStr := r.FormValue("chunk_index")
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil || chunkIndex < 0 || chunkIndex >= meta.TotalChunks {
		http.Error(w, `{"error":"invalid chunk_index"}`, http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("chunk")
	if err != nil {
		http.Error(w, `{"error":"chunk data required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	chunkPath := filepath.Join(chunkUploadDir(uploadID), strconv.Itoa(chunkIndex))
	dst, err := os.Create(chunkPath)
	if err != nil {
		http.Error(w, `{"error":"failed to write chunk"}`, http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, `{"error":"failed to write chunk"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"chunk_index":  chunkIndex,
		"upload_id":    uploadID,
	})
}

func apiUploadStatusHandler(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimPrefix(r.URL.Path, "/api/upload/status/")
	if uploadID == "" {
		http.Error(w, `{"error":"upload_id required"}`, http.StatusBadRequest)
		return
	}

	meta, ok := loadChunkMeta(uploadID)
	if !ok {
		http.Error(w, `{"error":"upload not found or expired"}`, http.StatusNotFound)
		return
	}

	// List received chunks
	entries, err := os.ReadDir(chunkUploadDir(uploadID))
	if err != nil {
		http.Error(w, `{"error":"failed to read upload dir"}`, http.StatusInternalServerError)
		return
	}

	received := []int{}
	for _, e := range entries {
		if e.Name() == ".meta" {
			continue
		}
		if idx, err := strconv.Atoi(e.Name()); err == nil {
			received = append(received, idx)
		}
	}

	writeJSON(w, map[string]interface{}{
		"upload_id":    uploadID,
		"filename":     meta.Filename,
		"size":         meta.Size,
		"total_chunks": meta.TotalChunks,
		"received":     received,
		"complete":     len(received) == meta.TotalChunks,
	})
}

func apiUploadCompleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	if req.UploadID == "" {
		http.Error(w, `{"error":"upload_id required"}`, http.StatusBadRequest)
		return
	}

	meta, ok := loadChunkMeta(req.UploadID)
	if !ok {
		http.Error(w, `{"error":"upload not found or expired"}`, http.StatusNotFound)
		return
	}

	// Verify all chunks are present
	dir := chunkUploadDir(req.UploadID)
	for i := 0; i < meta.TotalChunks; i++ {
		chunkPath := filepath.Join(dir, strconv.Itoa(i))
		if _, err := os.Stat(chunkPath); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"missing chunk %d"}`, i), http.StatusBadRequest)
			return
		}
	}

	// Reassemble chunks into final file
	id := genID()
	fpath := filepath.Join(dataDir, fileDir, id)
	dst, err := os.Create(fpath)
	if err != nil {
		http.Error(w, `{"error":"failed to create file"}`, http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	var totalWritten int64
	for i := 0; i < meta.TotalChunks; i++ {
		chunkPath := filepath.Join(dir, strconv.Itoa(i))
		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			os.Remove(fpath)
			http.Error(w, `{"error":"failed to read chunk"}`, http.StatusInternalServerError)
			return
		}
		n, err := io.Copy(dst, chunkFile)
		chunkFile.Close()
		if err != nil {
			os.Remove(fpath)
			http.Error(w, `{"error":"failed to write file"}`, http.StatusInternalServerError)
			return
		}
		totalWritten += n
	}

	// Clean up chunks
	os.RemoveAll(dir)

	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	ttl, err := parseTTL(meta.TTL)
	if err != nil {
		ttl = defaultTTL
	}

	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	item := Item{
		ID:       id,
		Name:     meta.Filename,
		Type:     "file",
		MimeType: mimeType,
		Size:     totalWritten,
		Created:  time.Now(),
		Expires:  expires,
		TTL:      ttlString(ttl),
	}
	addItem(item)

	// Auto-analyze images with vision model
	if visionEnabled && strings.HasPrefix(mimeType, "image/") {
		go analyzeImageAsync(id)
	}

	writeJSON(w, map[string]interface{}{
		"id":   id,
		"name": meta.Filename,
		"url":  fmt.Sprintf("%s/f/%s", baseURL, id),
	})
}

func genChunkID() string {
	// Reuse genID but ensure it doesn't collide with existing items or chunk uploads
	for {
		id := genID()
		// Check it doesn't already exist as a chunk upload
		if _, err := os.Stat(chunkUploadDir(id)); os.IsNotExist(err) {
			return id
		}
	}
}

// chunkSweeper cleans up incomplete chunk uploads older than chunkStaleTime
func chunkSweeper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		root := filepath.Join(dataDir, chunkDir)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		now := time.Now()
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			meta, ok := loadChunkMeta(e.Name())
			if !ok {
				// No meta file — stale, remove
				os.RemoveAll(filepath.Join(root, e.Name()))
				continue
			}
			if now.Sub(meta.Created) > chunkStaleTime {
				os.RemoveAll(filepath.Join(root, e.Name()))
				log.Printf("Cleaned up stale chunk upload %s (%s)", e.Name(), meta.Filename)
			}
		}
	}
}

func directFileHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/f/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	item, ok := findItem(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fpath := filepath.Join(dataDir, fileDir, id)
	w.Header().Set("Content-Type", item.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", item.Name))
	http.ServeFile(w, r, fpath)
}

func directTextHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/t/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := findItem(id); !ok {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(dataDir, textDir, id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

func itemTypePath(t string) string {
	if t == "text" {
		return "t"
	}
	return "f"
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
