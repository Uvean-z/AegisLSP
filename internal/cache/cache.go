package cache

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Uvean-z/aegislsp/internal/types"
)

func init() {
	gob.Register(types.Location{})
	gob.Register(types.Range{})
	gob.Register(types.Position{})
	gob.Register(json.RawMessage{})
}

// cacheKey uniquely identifies a semantic query: file + content hash + position.
type cacheKey struct {
	FilePath string // absolute path to the source file
	Hash     string // SHA-256 of file content
	Line     int    // 0-based line
	Column   int    // 0-based column
}

// cacheEntry holds cached semantic results for one position.
type cacheEntry struct {
	Definition []types.Location `json:"definition,omitempty"`
	Hover      json.RawMessage  `json:"hover,omitempty"`
}

// persistData is the on-disk serialization format.
type persistData struct {
	Version int                     `json:"version"`
	Entries map[cacheKey]cacheEntry `json:"entries"`
}

const currentVersion = 1

// SemanticCache is a persistent, concurrency-safe cache for LSP semantic data.
// Entries are keyed by (filePath, sha256(fileContent), line, column).
// Data is persisted to a gob-encoded file on disk.
type SemanticCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]cacheEntry
	path    string // on-disk file path (empty = in-memory only)
}

// New creates a SemanticCache. If path is non-empty, the cache is loaded from
// (and periodically persisted to) that file. If the file does not exist, an
// empty cache is created.
func New(path string) (*SemanticCache, error) {
	sc := &SemanticCache{
		entries: make(map[cacheKey]cacheEntry),
		path:    path,
	}

	if path == "" {
		return sc, nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve cache path: %w", err)
	}
	sc.path = absPath

	// Attempt to load existing cache.
	if _, err := os.Stat(absPath); err == nil {
		if err := sc.load(); err != nil {
			// Corrupt cache — start fresh.
			sc.entries = make(map[cacheKey]cacheEntry)
		}
	}

	return sc, nil
}

// HashContent computes the SHA-256 hex digest of data.
func HashContent(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// GetDefinition returns cached Definition results for the given position.
// Returns (locations, true) on hit, (nil, false) on miss.
func (sc *SemanticCache) GetDefinition(filePath, hash string, line, col int) ([]types.Location, bool) {
	key := cacheKey{FilePath: filePath, Hash: hash, Line: line, Column: col}
	sc.mu.RLock()
	entry, ok := sc.entries[key]
	sc.mu.RUnlock()
	if !ok || entry.Definition == nil {
		return nil, false
	}
	// Return a copy to prevent external mutation.
	out := make([]types.Location, len(entry.Definition))
	copy(out, entry.Definition)
	return out, true
}

// GetHover returns cached Hover data for the given position.
// Returns (raw, true) on hit, (nil, false) on miss.
func (sc *SemanticCache) GetHover(filePath, hash string, line, col int) (json.RawMessage, bool) {
	key := cacheKey{FilePath: filePath, Hash: hash, Line: line, Column: col}
	sc.mu.RLock()
	entry, ok := sc.entries[key]
	sc.mu.RUnlock()
	if !ok || entry.Hover == nil {
		return nil, false
	}
	// Return a copy.
	out := make(json.RawMessage, len(entry.Hover))
	copy(out, entry.Hover)
	return out, true
}

// SetDefinition stores Definition results in the cache.
func (sc *SemanticCache) SetDefinition(filePath, hash string, line, col int, locations []types.Location) {
	key := cacheKey{FilePath: filePath, Hash: hash, Line: line, Column: col}
	sc.mu.Lock()
	entry := sc.entries[key]
	// Copy to prevent external mutation from affecting cache.
	entry.Definition = make([]types.Location, len(locations))
	copy(entry.Definition, locations)
	sc.entries[key] = entry
	sc.mu.Unlock()
}

// SetHover stores Hover data in the cache.
func (sc *SemanticCache) SetHover(filePath, hash string, line, col int, raw json.RawMessage) {
	key := cacheKey{FilePath: filePath, Hash: hash, Line: line, Column: col}
	sc.mu.Lock()
	entry := sc.entries[key]
	entry.Hover = make(json.RawMessage, len(raw))
	copy(entry.Hover, raw)
	sc.entries[key] = entry
	sc.mu.Unlock()
}

// InvalidateFile removes all cache entries for the given file path.
func (sc *SemanticCache) InvalidateFile(filePath string) {
	sc.mu.Lock()
	for key := range sc.entries {
		if key.FilePath == filePath {
			delete(sc.entries, key)
		}
	}
	sc.mu.Unlock()
}

// InvalidateByHash removes all cache entries whose hash does not match the
// current content hash for the given file. This is a targeted eviction:
// if the file changed, the old hash entries become stale.
func (sc *SemanticCache) InvalidateByHash(filePath, currentHash string) {
	sc.mu.Lock()
	for key := range sc.entries {
		if key.FilePath == filePath && key.Hash != currentHash {
			delete(sc.entries, key)
		}
	}
	sc.mu.Unlock()
}

// Save persists the cache to disk. It is safe to call concurrently.
func (sc *SemanticCache) Save() error {
	if sc.path == "" {
		return nil
	}

	sc.mu.RLock()
	data := persistData{
		Version: currentVersion,
		Entries: make(map[cacheKey]cacheEntry, len(sc.entries)),
	}
	for k, v := range sc.entries {
		data.Entries[k] = v
	}
	sc.mu.RUnlock()

	// Ensure parent directory exists.
	dir := filepath.Dir(sc.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	// Write to a temp file then rename for atomicity.
	tmp := sc.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("encode cache: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, sc.path)
}

// load reads the cache from disk.
func (sc *SemanticCache) load() error {
	f, err := os.Open(sc.path)
	if err != nil {
		return err
	}
	defer f.Close()

	var data persistData
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return fmt.Errorf("decode cache: %w", err)
	}
	if data.Version != currentVersion {
		return fmt.Errorf("cache version mismatch: got %d, want %d", data.Version, currentVersion)
	}
	sc.entries = data.Entries
	if sc.entries == nil {
		sc.entries = make(map[cacheKey]cacheEntry)
	}
	return nil
}

// Size returns the number of cached entries (for diagnostics).
func (sc *SemanticCache) Size() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.entries)
}

// Clear removes all entries from the cache and deletes the on-disk file.
func (sc *SemanticCache) Clear() {
	sc.mu.Lock()
	sc.entries = make(map[cacheKey]cacheEntry)
	sc.mu.Unlock()
	if sc.path != "" {
		os.Remove(sc.path)
	}
}
