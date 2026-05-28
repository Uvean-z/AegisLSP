package cache

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/Uvean-z/aegislsp/internal/lspclient"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// CachedLSPClient wraps an LSPClient with a SemanticCache layer.
// Definition and Hover calls check the cache first (keyed by file path +
// SHA-256 of file content + position). On cache miss, the underlying client
// is called and the result is stored in the cache.
// All other LSPClient methods delegate directly.
type CachedLSPClient struct {
	inner lspclient.LSPClient
	cache *SemanticCache
}

// NewCachedLSPClient wraps inner with a persistent semantic cache at cachePath.
// If cachePath is empty, an in-memory-only cache is used.
func NewCachedLSPClient(inner lspclient.LSPClient, cachePath string) (*CachedLSPClient, error) {
	sc, err := New(cachePath)
	if err != nil {
		return nil, err
	}
	return &CachedLSPClient{inner: inner, cache: sc}, nil
}

// Cache returns the underlying SemanticCache (for testing/diagnostics).
func (c *CachedLSPClient) Cache() *SemanticCache {
	return c.cache
}

// Definition checks the cache before calling the underlying client.
// The file at uri is read and hashed; if the hash matches a cached entry for
// the same (file, position), the cached result is returned without touching gopls.
func (c *CachedLSPClient) Definition(ctx context.Context, uri string, position types.Position) ([]types.Location, error) {
	filePath := uriToPath(uri)
	hash, err := fileHash(filePath)
	if err != nil {
		// Can't read file — fall through to live LSP call.
		return c.inner.Definition(ctx, uri, position)
	}

	if locs, ok := c.cache.GetDefinition(filePath, hash, position.Line, position.Character); ok {
		return locs, nil
	}

	locs, err := c.inner.Definition(ctx, uri, position)
	if err != nil {
		return nil, err
	}
	c.cache.SetDefinition(filePath, hash, position.Line, position.Character, locs)
	return locs, nil
}

// Hover checks the cache before calling the underlying client.
func (c *CachedLSPClient) Hover(ctx context.Context, uri string, position types.Position) (json.RawMessage, error) {
	filePath := uriToPath(uri)
	hash, err := fileHash(filePath)
	if err != nil {
		return c.inner.Hover(ctx, uri, position)
	}

	if raw, ok := c.cache.GetHover(filePath, hash, position.Line, position.Character); ok {
		return raw, nil
	}

	raw, err := c.inner.Hover(ctx, uri, position)
	if err != nil {
		return nil, err
	}
	c.cache.SetHover(filePath, hash, position.Line, position.Character, raw)
	return raw, nil
}

// Save persists the cache to disk. Call this before process exit.
func (c *CachedLSPClient) Save() error {
	return c.cache.Save()
}

// --- Delegated methods (no caching) ---

// Initialize delegates to the underlying LSPClient.
func (c *CachedLSPClient) Initialize(ctx context.Context, rootURI string, folders []types.WorkspaceFolder) error {
	return c.inner.Initialize(ctx, rootURI, folders)
}

// Shutdown delegates to the underlying LSPClient.
func (c *CachedLSPClient) Shutdown(ctx context.Context) error {
	return c.inner.Shutdown(ctx)
}

// OpenDocument delegates to the underlying LSPClient.
func (c *CachedLSPClient) OpenDocument(ctx context.Context, uri, langID, content string) error {
	return c.inner.OpenDocument(ctx, uri, langID, content)
}

// CloseDocument delegates to the underlying LSPClient.
func (c *CachedLSPClient) CloseDocument(ctx context.Context, uri string) error {
	return c.inner.CloseDocument(ctx, uri)
}

// ChangeDocument delegates to the underlying LSPClient.
func (c *CachedLSPClient) ChangeDocument(ctx context.Context, uri, content string) error {
	return c.inner.ChangeDocument(ctx, uri, content)
}

// TypeDefinition delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) TypeDefinition(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return c.inner.TypeDefinition(ctx, uri, pos)
}

// Implementation delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) Implementation(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return c.inner.Implementation(ctx, uri, pos)
}

// References delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) References(ctx context.Context, uri string, pos types.Position) ([]types.Location, error) {
	return c.inner.References(ctx, uri, pos)
}

// DocumentSymbols delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) DocumentSymbols(ctx context.Context, uri string) (json.RawMessage, error) {
	return c.inner.DocumentSymbols(ctx, uri)
}

// WorkspaceSymbols delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) WorkspaceSymbols(ctx context.Context, query string) (json.RawMessage, error) {
	return c.inner.WorkspaceSymbols(ctx, query)
}

// Diagnostics delegates to the underlying LSPClient without caching.
func (c *CachedLSPClient) Diagnostics(ctx context.Context, uri string) ([]types.Diagnostic, error) {
	return c.inner.Diagnostics(ctx, uri)
}

// OnNotification delegates to the underlying LSPClient.
func (c *CachedLSPClient) OnNotification(method string, handler lspclient.NotificationHandler) {
	c.inner.OnNotification(method, handler)
}

// IsInitialized delegates to the underlying LSPClient.
func (c *CachedLSPClient) IsInitialized() bool {
	return c.inner.IsInitialized()
}

// --- Helpers ---

// uriToPath converts file:// URIs into local filesystem paths.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}

	u, err := url.Parse(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}

	path, err := url.PathUnescape(u.Path)
	if err != nil {
		path = u.Path
	}

	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		return path[1:]
	}
	return path
}

// fileHash reads the file at path and returns its SHA-256 hex digest.
func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return HashContent(data), nil
}
