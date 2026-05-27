package lspclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Uvean-z/aegislsp/internal/types"
)

// ErrNoActiveLSP is returned by NullLSPClient when no live LSP server is available.
var ErrNoActiveLSP = fmt.Errorf("no active LSP server")

// NullLSPClient is a stub LSPClient that returns ErrNoActiveLSP for all
// operations. It is used as the inner client for CachedLSPClient in MCP mode,
// where no live LSP process is running — cache hits still return data, while
// cache misses gracefully degrade to zero-enrichment.
type NullLSPClient struct{}

// NewNullLSPClient returns a new NullLSPClient.
func NewNullLSPClient() LSPClient {
	return &NullLSPClient{}
}

// Initialize returns ErrNoActiveLSP. NullLSPClient has no backing server.
func (c *NullLSPClient) Initialize(_ context.Context, _ string, _ []types.WorkspaceFolder) error {
	return ErrNoActiveLSP
}

// Shutdown returns ErrNoActiveLSP.
func (c *NullLSPClient) Shutdown(_ context.Context) error {
	return ErrNoActiveLSP
}

// OpenDocument returns ErrNoActiveLSP.
func (c *NullLSPClient) OpenDocument(_ context.Context, _, _, _ string) error {
	return ErrNoActiveLSP
}

// CloseDocument returns ErrNoActiveLSP.
func (c *NullLSPClient) CloseDocument(_ context.Context, _ string) error {
	return ErrNoActiveLSP
}

// ChangeDocument returns ErrNoActiveLSP.
func (c *NullLSPClient) ChangeDocument(_ context.Context, _, _ string) error {
	return ErrNoActiveLSP
}

// Definition returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) Definition(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
	return nil, ErrNoActiveLSP
}

// TypeDefinition returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) TypeDefinition(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
	return nil, ErrNoActiveLSP
}

// Implementation returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) Implementation(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
	return nil, ErrNoActiveLSP
}

// References returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) References(_ context.Context, _ string, _ types.Position) ([]types.Location, error) {
	return nil, ErrNoActiveLSP
}

// Hover returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) Hover(_ context.Context, _ string, _ types.Position) (json.RawMessage, error) {
	return nil, ErrNoActiveLSP
}

// DocumentSymbols returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) DocumentSymbols(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, ErrNoActiveLSP
}

// WorkspaceSymbols returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) WorkspaceSymbols(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, ErrNoActiveLSP
}

// Diagnostics returns nil and ErrNoActiveLSP.
func (c *NullLSPClient) Diagnostics(_ context.Context, _ string) ([]types.Diagnostic, error) {
	return nil, ErrNoActiveLSP
}

// OnNotification is a no-op; there is no server to receive notifications from.
func (c *NullLSPClient) OnNotification(_ string, _ NotificationHandler) {
	// No-op — no server to receive notifications from.
}

// IsInitialized always returns false; NullLSPClient has no backing server.
func (c *NullLSPClient) IsInitialized() bool {
	return false
}
