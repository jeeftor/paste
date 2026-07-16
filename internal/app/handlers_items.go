package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func apiFilesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	items := listItems()
	if value := r.URL.Query().Get("persistent"); value != "" {
		persistent := value == "true"
		filtered := items[:0]
		for _, item := range items {
			if item.Persistent == persistent {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	includeAnalysisContent := r.URL.Query().Get("analysis") != "summary"
	list := make([]map[string]interface{}, len(items))
	for index, item := range items {
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
			"url":        linkURL(item.ID),
		}
		if len(item.Analyses) > 0 {
			if includeAnalysisContent {
				entry["analyses"] = item.Analyses
			} else {
				entry["analyses"] = analysisSummaries(item.Analyses)
			}
		}
		list[index] = entry
	}
	writeJSON(w, map[string]interface{}{"items": list})
}

func analysisSummaries(analyses map[string]*ItemAnalysis) map[string]map[string]interface{} {
	summaries := make(map[string]map[string]interface{}, len(analyses))
	for name, analysis := range analyses {
		if analysis == nil {
			continue
		}
		summaries[name] = map[string]interface{}{
			"status":      analysis.Status,
			"backend":     analysis.Backend,
			"duration_ms": analysis.DurationMs,
		}
	}
	return summaries
}

func apiFileHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/files/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "analysis" {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		item, ok := findItem(id)
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]interface{}{"id": item.ID, "analyses": item.Analyses})
		return
	}
	if len(parts) >= 2 && parts[1] == "url" {
		item, ok := findItem(id)
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"url": linkURL(item.ID)})
		return
	}

	item, ok := findItem(id)
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		deleteItem(id)
		writeJSON(w, map[string]string{"status": "deleted", "id": id})
		return
	case http.MethodPatch:
		var request struct {
			Persistent *bool `json:"persistent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if request.Persistent == nil {
			http.Error(w, `{"error":"persistent field required"}`, http.StatusBadRequest)
			return
		}
		if !updateItem(id, func(item *Item) bool {
			item.Persistent = *request.Persistent
			if item.Persistent {
				item.Expires = time.Time{}
				item.TTL = "never"
			} else {
				item.Expires = time.Now().Add(defaultTTL)
				item.TTL = ttlString(defaultTTL)
			}
			return true
		}) {
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
	case http.MethodGet:
	default:
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
			"id": item.ID, "name": item.Name, "type": "text", "content": string(data),
			"created": item.Created, "expires": item.Expires,
		})
		return
	}

	w.Header().Set("Content-Type", item.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", item.Name))
	http.ServeFile(w, r, filepath.Join(dataDir, fileDir, id))
}

func apiTextHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Content string `json:"content"`
		Name    string `json:"name"`
		TTL     string `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		request.Content = r.FormValue("content")
		request.Name = r.FormValue("name")
		request.TTL = r.FormValue("ttl")
	}
	if request.Content == "" {
		http.Error(w, `{"error":"content required"}`, http.StatusBadRequest)
		return
	}
	ttl, err := parseTTL(request.TTL)
	if err != nil {
		ttl = defaultTTL
	}
	id, err := genID()
	if err != nil {
		http.Error(w, `{"error":"failed to generate item ID"}`, http.StatusInternalServerError)
		return
	}
	name := request.Name
	if name == "" {
		name = fmt.Sprintf("snippet_%s.txt", time.Now().Format("20060102_150405"))
	}
	if err := os.WriteFile(filepath.Join(dataDir, textDir, id), []byte(request.Content), 0644); err != nil {
		http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
		return
	}
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	addItem(Item{ID: id, Name: name, Type: "text", Size: int64(len(request.Content)), Created: time.Now(), Expires: expires, TTL: ttlString(ttl)})
	writeJSON(w, map[string]interface{}{"id": id, "name": name, "url": linkURL(id)})
}

func apiTextItemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
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
	_, _ = w.Write(data)
}
