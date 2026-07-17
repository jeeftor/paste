package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Test helpers ---

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dataDir = dir
	assets = Assets{IndexHTML: []byte("<html><body>Klipbord {{VERSION}}</body></html>")}
	// Create subdirectories
	for _, d := range []string{textDir, fileDir, chunkDir} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}
	// Reset global state
	meta = Metadata{Items: []Item{}}
	maxUploadMB = defaultMaxUploadMB
	maxUploadBytes = int64(maxUploadMB) * 1024 * 1024
	return dir
}

func teardownTestDir(t *testing.T, dir string) {
	t.Helper()
	os.RemoveAll(dir)
}

func makeItem(id, name, itemType string) Item {
	return Item{
		ID:       id,
		Name:     name,
		Type:     itemType,
		MimeType: "text/plain",
		Size:     100,
		Created:  time.Now(),
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		TTL:      "7d",
	}
}

func makeImageItem(id, name string) Item {
	return Item{
		ID:       id,
		Name:     name,
		Type:     "file",
		MimeType: "image/png",
		Size:     500,
		Created:  time.Now(),
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		TTL:      "7d",
	}
}

// --- Tests ---

func TestEnvOr(t *testing.T) {
	os.Unsetenv("TEST_ENV_VAR")
	if v := envOr("TEST_ENV_VAR", "default"); v != "default" {
		t.Errorf("expected 'default', got %q", v)
	}
	os.Setenv("TEST_ENV_VAR", "custom")
	defer os.Unsetenv("TEST_ENV_VAR")
	if v := envOr("TEST_ENV_VAR", "default"); v != "custom" {
		t.Errorf("expected 'custom', got %q", v)
	}
}

func TestParseTTL(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		hasErr   bool
	}{
		{"1h", time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"never", 0, false},
		{"", 0, false},
		{"invalid", 0, true},
		{"5h", 0, true},
	}

	for _, tt := range tests {
		got, err := parseTTL(tt.input)
		if tt.hasErr {
			if err == nil {
				t.Errorf("parseTTL(%q) expected error, got nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTTL(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("parseTTL(%q) = %v, expected %v", tt.input, got, tt.expected)
		}
	}
}

func TestTTLString(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{time.Hour, "1h"},
		{24 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{30 * 24 * time.Hour, "30d"},
		{0, "never"},
	}
	for _, tt := range tests {
		got := ttlString(tt.d)
		if got != tt.expected {
			t.Errorf("ttlString(%v) = %q, expected %q", tt.d, got, tt.expected)
		}
	}
}

func TestGenID(t *testing.T) {
	setupTestDir(t)
	id, err := genID()
	if err != nil {
		t.Fatalf("genID() returned an error: %v", err)
	}
	if len(id) != idLen {
		t.Errorf("genID() length = %d, expected %d", len(id), idLen)
	}
	// Generate multiple IDs and check uniqueness
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := genID()
		if err != nil {
			t.Fatalf("genID() returned an error: %v", err)
		}
		if ids[id] {
			t.Errorf("genID() generated duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

func TestAddFindItem(t *testing.T) {
	setupTestDir(t)
	item := makeItem("test01", "test.txt", "text")
	addItem(item)

	found, ok := findItem("test01")
	if !ok {
		t.Fatal("findItem() did not find item")
	}
	if found.Name != "test.txt" {
		t.Errorf("found item name = %q, expected 'test.txt'", found.Name)
	}

	// Test not found
	_, ok = findItem("nonexistent")
	if ok {
		t.Error("findItem() found non-existent item")
	}
}

func TestUpdateItem(t *testing.T) {
	setupTestDir(t)
	item := makeItem("upd01", "test.txt", "text")
	addItem(item)

	ok := updateItem("upd01", func(it *Item) bool {
		it.Name = "updated.txt"
		return true
	})
	if !ok {
		t.Fatal("updateItem() returned false")
	}

	found, _ := findItem("upd01")
	if found.Name != "updated.txt" {
		t.Errorf("updated name = %q, expected 'updated.txt'", found.Name)
	}

	// Test abort (return false)
	ok = updateItem("upd01", func(it *Item) bool {
		return false
	})
	if ok {
		t.Error("updateItem() returned true when update returned false")
	}

	// Test not found
	ok = updateItem("nonexistent", func(it *Item) bool {
		return true
	})
	if ok {
		t.Error("updateItem() returned true for non-existent item")
	}
}

func TestDeleteItem(t *testing.T) {
	setupTestDir(t)
	// Create a text item with a file on disk
	item := makeItem("del01", "delete.txt", "text")
	addItem(item)
	// Create the text file on disk
	textPath := filepath.Join(dataDir, textDir, "del01")
	os.WriteFile(textPath, []byte("hello"), 0644)

	// Verify it exists
	_, ok := findItem("del01")
	if !ok {
		t.Fatal("item not found before delete")
	}

	deleteItem("del01")

	// Verify it's gone from metadata
	_, ok = findItem("del01")
	if ok {
		t.Error("item still in metadata after delete")
	}

	// Verify file is removed from disk
	if _, err := os.Stat(textPath); !os.IsNotExist(err) {
		t.Error("file still exists on disk after delete")
	}

	meta = Metadata{}
	loadMetadata()
	if _, ok := findItem("del01"); ok {
		t.Error("deleted item returned after metadata reload")
	}

	// Deleting non-existent should not panic
	deleteItem("nonexistent")
}

func TestRemoveItem(t *testing.T) {
	setupTestDir(t)
	item1 := makeItem("rem01", "a.txt", "text")
	item2 := makeItem("rem02", "b.txt", "text")
	addItem(item1)
	addItem(item2)

	ok := removeItem("rem01")
	if !ok {
		t.Error("removeItem() returned false for existing item")
	}

	_, found := findItem("rem01")
	if found {
		t.Error("item still found after removeItem")
	}

	// Item 2 should still exist
	_, found = findItem("rem02")
	if !found {
		t.Error("item2 removed incorrectly")
	}

	ok = removeItem("nonexistent")
	if ok {
		t.Error("removeItem() returned true for non-existent item")
	}
}

func TestListItems(t *testing.T) {
	setupTestDir(t)
	addItem(makeItem("lst01", "a.txt", "text"))
	addItem(makeItem("lst02", "b.txt", "text"))

	items := listItems()
	if len(items) != 2 {
		t.Errorf("listItems() returned %d items, expected 2", len(items))
	}

	// Verify it's a copy (modifying shouldn't affect original)
	items[0].Name = "modified"
	original, _ := findItem("lst01")
	if original.Name == "modified" {
		t.Error("listItems() did not return a copy")
	}
}

func TestListItemsCopiesAnalyses(t *testing.T) {
	setupTestDir(t)
	item := makeImageItem("analysis01", "image.png")
	item.Analyses = map[string]*ItemAnalysis{
		"default": {Status: "complete", Text: "original", Evidence: &VisualEvidence{
			Structure:  []string{"header above content"},
			UIElements: []UIElement{{Role: "tab", Label: "Clipboard", State: []string{"selected"}}},
		}},
	}
	addItem(item)

	items := listItems()
	items[0].Analyses["default"].Text = "modified"
	items[0].Analyses["default"].Evidence.Structure[0] = "modified"
	items[0].Analyses["default"].Evidence.UIElements[0].State[0] = "disabled"
	items[0].Analyses["extra"] = &ItemAnalysis{Status: "pending"}

	original, ok := findItem("analysis01")
	if !ok {
		t.Fatal("findItem() did not find stored item")
	}
	if got, want := original.Analyses["default"].Text, "original"; got != want {
		t.Errorf("analysis text = %q, expected %q", got, want)
	}
	if _, exists := original.Analyses["extra"]; exists {
		t.Error("listItems() returned a shared analyses map")
	}
	if got, want := original.Analyses["default"].Evidence.Structure[0], "header above content"; got != want {
		t.Errorf("analysis evidence structure = %q, expected %q", got, want)
	}
	if got, want := original.Analyses["default"].Evidence.UIElements[0].State[0], "selected"; got != want {
		t.Errorf("analysis evidence UI state = %q, expected %q", got, want)
	}
}

func TestMetadataPersistence(t *testing.T) {
	dir := setupTestDir(t)
	addItem(makeItem("pers01", "persist.txt", "text"))

	// Reload metadata from disk
	meta = Metadata{} // Clear in-memory
	loadMetadata()

	found, ok := findItem("pers01")
	if !ok {
		t.Fatal("item not found after reload")
	}
	if found.Name != "persist.txt" {
		t.Errorf("loaded name = %q, expected 'persist.txt'", found.Name)
	}

	teardownTestDir(t, dir)
}

func TestLinkURL(t *testing.T) {
	originalBaseURL := baseURL
	baseURL = "https://klipbord.example.com"
	t.Cleanup(func() { baseURL = originalBaseURL })

	if got, want := linkURL("abc123"), "https://klipbord.example.com/link/abc123"; got != want {
		t.Errorf("linkURL() = %q, expected %q", got, want)
	}
}

func TestCleanupOrphanedMetadata(t *testing.T) {
	setupTestDir(t)
	// Create items with files on disk
	item1 := makeItem("orp01", "exists.txt", "text")
	addItem(item1)
	os.WriteFile(filepath.Join(dataDir, textDir, "orp01"), []byte("data"), 0644)

	// Create item without file on disk (orphaned)
	item2 := makeItem("orp02", "orphaned.txt", "text")
	addItem(item2)
	// Don't create the file — it's orphaned

	cleanupOrphanedMetadata()

	// Item 1 should still exist
	_, ok := findItem("orp01")
	if !ok {
		t.Error("existing item was removed by cleanup")
	}

	// Item 2 should be removed
	_, ok = findItem("orp02")
	if ok {
		t.Error("orphaned item was not removed by cleanup")
	}
}
