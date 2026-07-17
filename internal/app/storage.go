package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeeftor/klipbord/internal/id"
)

const (
	defaultTTL = 7 * 24 * time.Hour
	idLen      = 12
	textDir    = "text"
	fileDir    = "files"
	chunkDir   = "chunks"
	metaFile   = "metadata.json"
)

var (
	dataDir string
	baseURL string
	meta    Metadata
	metaMu  sync.RWMutex
)

// ItemAnalysis is the result of an optional vision analysis for an item.
type ItemAnalysis struct {
	Status      string          `json:"status"`
	Text        string          `json:"text,omitempty"`
	Description string          `json:"description,omitempty"`
	Evidence    *VisualEvidence `json:"evidence,omitempty"`
	Backend     string          `json:"backend,omitempty"`
	PromptName  string          `json:"prompt_name,omitempty"`
	Question    string          `json:"question,omitempty"`
	ProcessedAt time.Time       `json:"processed_at,omitempty"`
	DurationMs  int64           `json:"duration_ms,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// VisualEvidence preserves visible facts that a text-only agent can reason over.
type VisualEvidence struct {
	Structure     []string    `json:"structure,omitempty"`
	State         []string    `json:"state,omitempty"`
	Uncertainties []string    `json:"uncertainties,omitempty"`
	UIElements    []UIElement `json:"ui_elements,omitempty"`
}

// UIElement describes a visible interface control without inferring behavior.
type UIElement struct {
	Role     string   `json:"role"`
	Label    string   `json:"label"`
	State    []string `json:"state,omitempty"`
	Location string   `json:"location,omitempty"`
}

// Item describes a text snippet or uploaded file.
type Item struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Type       string                   `json:"type"`
	MimeType   string                   `json:"mime_type,omitempty"`
	Size       int64                    `json:"size"`
	Created    time.Time                `json:"created"`
	Expires    time.Time                `json:"expires"`
	TTL        string                   `json:"ttl"`
	Persistent bool                     `json:"persistent"`
	Analyses   map[string]*ItemAnalysis `json:"analyses,omitempty"`
	Content    string                   `json:"content,omitempty"`
}

type Metadata struct {
	Items []Item `json:"items"`
}

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

// cleanupOrphanedMetadata removes metadata entries whose files no longer exist on disk.
func cleanupOrphanedMetadata() {
	metaMu.Lock()
	defer metaMu.Unlock()
	var cleaned []Item
	removed := 0
	for _, item := range meta.Items {
		path := filepath.Join(dataDir, fileDir, item.ID)
		if item.Type == "text" {
			path = filepath.Join(dataDir, textDir, item.ID)
		}
		if _, err := os.Stat(path); err != nil {
			log.Printf("Cleanup: removing orphaned metadata entry %s (%s) — file missing", item.ID, item.Name)
			removed++
			continue
		}
		cleaned = append(cleaned, item)
	}
	if removed == 0 {
		return
	}

	meta.Items = cleaned
	log.Printf("Cleanup: removed %d orphaned entries", removed)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(dataDir, metaFile), data, 0644)
	}
}

func saveMetadata() {
	metaMu.RLock()
	defer metaMu.RUnlock()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal metadata: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dataDir, metaFile), data, 0644); err != nil {
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
	for index, item := range meta.Items {
		if item.ID == id {
			meta.Items = append(meta.Items[:index], meta.Items[index+1:]...)
			metaMu.Unlock()
			saveMetadata()
			return true
		}
	}
	metaMu.Unlock()
	return false
}

// updateItem modifies an item in place and persists it when update accepts the change.
func updateItem(id string, update func(*Item) bool) bool {
	metaMu.Lock()
	defer metaMu.Unlock()
	for index := range meta.Items {
		if meta.Items[index].ID != id || !update(&meta.Items[index]) {
			continue
		}
		metaMu.Unlock()
		saveMetadata()
		metaMu.Lock()
		return true
	}
	return false
}

func findItem(id string) (Item, bool) {
	metaMu.RLock()
	defer metaMu.RUnlock()
	for _, item := range meta.Items {
		if item.ID == id {
			return cloneItem(item), true
		}
	}
	return Item{}, false
}

func listItems() []Item {
	metaMu.RLock()
	defer metaMu.RUnlock()
	items := make([]Item, len(meta.Items))
	for index, item := range meta.Items {
		items[index] = cloneItem(item)
	}
	return items
}

func cloneItem(item Item) Item {
	copy := item
	if item.Analyses == nil {
		return copy
	}
	copy.Analyses = make(map[string]*ItemAnalysis, len(item.Analyses))
	for name, analysis := range item.Analyses {
		if analysis == nil {
			continue
		}
		analysisCopy := *analysis
		analysisCopy.Evidence = cloneVisualEvidence(analysis.Evidence)
		copy.Analyses[name] = &analysisCopy
	}
	return copy
}

func cloneVisualEvidence(evidence *VisualEvidence) *VisualEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Structure = append([]string(nil), evidence.Structure...)
	copy.State = append([]string(nil), evidence.State...)
	copy.Uncertainties = append([]string(nil), evidence.Uncertainties...)
	copy.UIElements = make([]UIElement, len(evidence.UIElements))
	for index, element := range evidence.UIElements {
		copy.UIElements[index] = element
		copy.UIElements[index].State = append([]string(nil), element.State...)
	}
	return &copy
}

func genID() (string, error) {
	for {
		generated, err := id.New(idLen)
		if err != nil {
			return "", fmt.Errorf("generate item ID: %w", err)
		}
		if _, exists := findItem(generated); !exists {
			return generated, nil
		}
	}
}

func parseTTL(value string) (time.Duration, error) {
	switch value {
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
		return 0, fmt.Errorf("invalid TTL: %s", value)
	}
}

func ttlString(duration time.Duration) string {
	switch duration {
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
		return duration.String()
	}
}

func sweeper() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		for _, item := range listItems() {
			if item.Persistent || item.Expires.IsZero() || !now.After(item.Expires) {
				continue
			}
			deleteItem(item.ID)
			log.Printf("Expired item %s (%s)", item.ID, item.Name)
		}
	}
}

func deleteItem(id string) {
	item, ok := findItem(id)
	if !ok {
		return
	}
	directory := fileDir
	if item.Type == "text" {
		directory = textDir
	}
	_ = os.Remove(filepath.Join(dataDir, directory, id))
	removeItem(id)
}
