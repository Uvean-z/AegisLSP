package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Uvean-z/aegislsp/internal/types"
)

func TestNew_InMemory(t *testing.T) {
	sc, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sc.Size() != 0 {
		t.Errorf("Size = %d, want 0", sc.Size())
	}
}

func TestHashContent_Deterministic(t *testing.T) {
	data := []byte("package main\nfunc main() {}\n")
	h1 := HashContent(data)
	h2 := HashContent(data)
	if h1 != h2 {
		t.Errorf("hashes differ: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}

func TestHashContent_DifferentContent(t *testing.T) {
	h1 := HashContent([]byte("hello"))
	h2 := HashContent([]byte("world"))
	if h1 == h2 {
		t.Error("different content produced same hash")
	}
}

func TestSetGet_Definition_Hit(t *testing.T) {
	sc, _ := New("")
	locs := []types.Location{
		{URI: "file:///main.go", Range: types.Range{Start: types.Position{Line: 5, Character: 0}}},
	}
	sc.SetDefinition("/main.go", "abc123", 10, 5, locs)

	got, ok := sc.GetDefinition("/main.go", "abc123", 10, 5)
	if !ok {
		t.Fatal("cache miss, expected hit")
	}
	if len(got) != 1 || got[0].URI != "file:///main.go" {
		t.Errorf("got %+v", got)
	}
}

func TestSetGet_Definition_Miss_WrongHash(t *testing.T) {
	sc, _ := New("")
	locs := []types.Location{{URI: "file:///main.go"}}
	sc.SetDefinition("/main.go", "abc123", 10, 5, locs)

	_, ok := sc.GetDefinition("/main.go", "DIFFERENT_HASH", 10, 5)
	if ok {
		t.Error("expected cache miss with different hash")
	}
}

func TestSetGet_Definition_Miss_WrongPosition(t *testing.T) {
	sc, _ := New("")
	locs := []types.Location{{URI: "file:///main.go"}}
	sc.SetDefinition("/main.go", "abc123", 10, 5, locs)

	_, ok := sc.GetDefinition("/main.go", "abc123", 11, 5)
	if ok {
		t.Error("expected cache miss with different line")
	}

	_, ok = sc.GetDefinition("/main.go", "abc123", 10, 6)
	if ok {
		t.Error("expected cache miss with different column")
	}
}

func TestSetGet_Hover_Hit(t *testing.T) {
	sc, _ := New("")
	raw := json.RawMessage(`{"contents":{"kind":"markdown","value":"func main()"}}`)
	sc.SetHover("/main.go", "abc123", 10, 5, raw)

	got, ok := sc.GetHover("/main.go", "abc123", 10, 5)
	if !ok {
		t.Fatal("cache miss, expected hit")
	}
	if string(got) != string(raw) {
		t.Errorf("got %s, want %s", got, raw)
	}
}

func TestSetGet_Hover_Miss(t *testing.T) {
	sc, _ := New("")
	_, ok := sc.GetHover("/main.go", "abc123", 10, 5)
	if ok {
		t.Error("expected cache miss on empty cache")
	}
}

func TestInvalidateFile(t *testing.T) {
	sc, _ := New("")
	sc.SetDefinition("/a.go", "h1", 1, 1, []types.Location{{URI: "file:///a.go"}})
	sc.SetDefinition("/b.go", "h2", 2, 2, []types.Location{{URI: "file:///b.go"}})

	sc.InvalidateFile("/a.go")

	_, ok := sc.GetDefinition("/a.go", "h1", 1, 1)
	if ok {
		t.Error("expected /a.go entry to be invalidated")
	}
	_, ok = sc.GetDefinition("/b.go", "h2", 2, 2)
	if !ok {
		t.Error("expected /b.go entry to survive invalidation of /a.go")
	}
}

func TestInvalidateByHash(t *testing.T) {
	sc, _ := New("")
	sc.SetDefinition("/a.go", "old_hash", 1, 1, []types.Location{{URI: "file:///a.go"}})
	sc.SetDefinition("/a.go", "new_hash", 2, 2, []types.Location{{URI: "file:///a.go"}})

	sc.InvalidateByHash("/a.go", "new_hash")

	_, ok := sc.GetDefinition("/a.go", "old_hash", 1, 1)
	if ok {
		t.Error("expected old_hash entry to be evicted")
	}
	_, ok = sc.GetDefinition("/a.go", "new_hash", 2, 2)
	if !ok {
		t.Error("expected new_hash entry to survive")
	}
}

func TestCopySemantics_Definition(t *testing.T) {
	sc, _ := New("")
	locs := []types.Location{{URI: "file:///main.go"}}
	sc.SetDefinition("/main.go", "h", 1, 1, locs)

	// Mutate the returned slice — should not affect cache.
	got, _ := sc.GetDefinition("/main.go", "h", 1, 1)
	got[0].URI = "file:///mutated.go"

	// Re-fetch — should still be original.
	got2, _ := sc.GetDefinition("/main.go", "h", 1, 1)
	if got2[0].URI != "file:///main.go" {
		t.Errorf("cache was mutated externally: %s", got2[0].URI)
	}
}

func TestCopySemantics_Hover(t *testing.T) {
	sc, _ := New("")
	raw := json.RawMessage(`{"value":"original"}`)
	sc.SetHover("/main.go", "h", 1, 1, raw)

	got, _ := sc.GetHover("/main.go", "h", 1, 1)
	got[0] = 'X' // mutate

	got2, _ := sc.GetHover("/main.go", "h", 1, 1)
	if string(got2) != `{"value":"original"}` {
		t.Errorf("cache was mutated externally: %s", got2)
	}
}

func TestPersist_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")

	// Create and populate.
	sc1, _ := New(path)
	locs := []types.Location{
		{URI: "file:///main.go", Range: types.Range{Start: types.Position{Line: 5, Character: 10}}},
	}
	sc1.SetDefinition("/main.go", "abc123", 10, 5, locs)
	raw := json.RawMessage(`{"contents":{"kind":"markdown","value":"func main()"}}`)
	sc1.SetHover("/main.go", "abc123", 10, 5, raw)

	if err := sc1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load from disk.
	sc2, _ := New(path)
	if sc2.Size() != 1 {
		t.Fatalf("loaded Size = %d, want 1", sc2.Size())
	}

	got, ok := sc2.GetDefinition("/main.go", "abc123", 10, 5)
	if !ok {
		t.Fatal("definition cache miss after load")
	}
	if got[0].URI != "file:///main.go" || got[0].Range.Start.Line != 5 {
		t.Errorf("loaded definition: %+v", got)
	}

	gotHover, ok := sc2.GetHover("/main.go", "abc123", 10, 5)
	if !ok {
		t.Fatal("hover cache miss after load")
	}
	if string(gotHover) != string(raw) {
		t.Errorf("loaded hover: %s", gotHover)
	}
}

func TestPersist_CorruptFile_RecoversGracefully(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")

	// Write corrupt data.
	os.WriteFile(path, []byte("not a valid gob file"), 0o644)

	sc, err := New(path)
	if err != nil {
		t.Fatalf("New should not return error for corrupt cache: %v", err)
	}
	// Should start with an empty cache.
	if sc.Size() != 0 {
		t.Errorf("Size = %d, want 0 (corrupt cache should start fresh)", sc.Size())
	}
}

func TestPersist_NonexistentFile_CreatesNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "cache.gob")

	sc, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sc.Size() != 0 {
		t.Errorf("Size = %d, want 0", sc.Size())
	}
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")

	sc, _ := New(path)
	sc.SetDefinition("/main.go", "h", 1, 1, []types.Location{{URI: "file:///main.go"}})
	sc.Save()

	sc.Clear()
	if sc.Size() != 0 {
		t.Errorf("Size after Clear = %d, want 0", sc.Size())
	}
	// File should be removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected cache file to be removed after Clear")
	}
}

func TestConcurrency_ReadWriteSafety(t *testing.T) {
	sc, _ := New("")
	var wg sync.WaitGroup

	// 100 writers and 100 readers hitting the same cache concurrently.
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			locs := []types.Location{{URI: "file:///main.go"}}
			sc.SetDefinition("/main.go", "hash", idx, 0, locs)
		}(i)
		go func(idx int) {
			defer wg.Done()
			sc.GetDefinition("/main.go", "hash", idx, 0)
		}(i)
	}
	wg.Wait()

	// No panic, no race condition — test passes if -race is enabled.
}

func TestConcurrency_MultipleFiles(t *testing.T) {
	sc, _ := New("")
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			path := "/file_" + string(rune('A'+idx%26)) + ".go"
			sc.SetDefinition(path, "h", idx, 0, []types.Location{{URI: "file://" + path}})
		}(i)
		go func(idx int) {
			defer wg.Done()
			path := "/file_" + string(rune('A'+idx%26)) + ".go"
			sc.GetDefinition(path, "h", idx, 0)
		}(i)
	}
	wg.Wait()
}

func TestFileHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("package main\n"), 0o644)

	h, err := fileHash(path)
	if err != nil {
		t.Fatalf("fileHash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}

	// Same file, same hash.
	h2, _ := fileHash(path)
	if h != h2 {
		t.Error("same file produced different hashes")
	}
}

func TestFileHash_NonexistentFile(t *testing.T) {
	_, err := fileHash("/nonexistent/path/file.go")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestUriToPath(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"file:///D:/project/main.go", "D:/project/main.go"},
		{"file:///home/user/main.go", "home/user/main.go"},
		{"/home/user/main.go", "/home/user/main.go"},
	}
	for _, tt := range tests {
		got := uriToPath(tt.uri)
		if got != tt.want {
			t.Errorf("uriToPath(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}
