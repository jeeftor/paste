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
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

const (
	defaultPort      = "8080"
	defaultDataDir   = "/data"
	defaultBaseURL   = "http://localhost:8080"
	defaultTTL       = 7 * 24 * time.Hour
	idLen            = 6
	textDir          = "text"
	fileDir          = "files"
	metaFile         = "metadata.json"
	maxUploadMB      = 100
	maxUploadBytes   = maxUploadMB * 1024 * 1024
)

var (
	dataDir  string
	baseURL  string
	meta     Metadata
	metaMu   sync.RWMutex
)

type Item struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"` // "text" or "file"
	MimeType  string    `json:"mime_type,omitempty"`
	Size      int64     `json:"size"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	TTL       string    `json:"ttl"`
	Content   string    `json:"content,omitempty"` // for text items in API responses
}

type Metadata struct {
	Items []Item `json:"items"`
}

func main() {
	dataDir = envOr("DATA_DIR", defaultDataDir)
	baseURL = envOr("BASE_URL", defaultBaseURL)
	port := envOr("PORT", defaultPort)

	for _, d := range []string{textDir, fileDir} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0755); err != nil {
			log.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	loadMetadata()

	// Start expiry sweeper
	go sweeper()

	mux := http.NewServeMux()

	// Web UI
	mux.HandleFunc("/", rootHandler)

	// REST API
	mux.HandleFunc("/api/files", apiFilesHandler)
	mux.HandleFunc("/api/files/", apiFileHandler)
	mux.HandleFunc("/api/text", apiTextHandler)
	mux.HandleFunc("/api/text/", apiTextItemHandler)
	mux.HandleFunc("/api/upload", apiUploadHandler)
	mux.HandleFunc("/health", healthHandler)

	// MCP endpoint
	mux.HandleFunc("/mcp", mcpHandler)
	mux.HandleFunc("/mcp/", mcpHandler)

	// Direct file/text access by ID
	mux.HandleFunc("/f/", directFileHandler)
	mux.HandleFunc("/t/", directTextHandler)

	log.Printf("paste server starting on :%s (data: %s, base: %s)", port, dataDir, baseURL)
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
			if item.Expires.IsZero() {
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
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
	// Sort by created desc
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	// Strip content from list response
	list := make([]map[string]interface{}, len(items))
	for i, item := range items {
		list[i] = map[string]interface{}{
			"id":         item.ID,
			"name":       item.Name,
			"type":       item.Type,
			"mime_type":  item.MimeType,
			"size":       item.Size,
			"created":    item.Created,
			"expires":    item.Expires,
			"ttl":        item.TTL,
			"url":        fmt.Sprintf("%s/%s/%s", baseURL, itemTypePath(item.Type), item.ID),
		}
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

	writeJSON(w, map[string]interface{}{
		"id":   id,
		"name": header.Filename,
		"url":  fmt.Sprintf("%s/f/%s", baseURL, id),
	})
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
