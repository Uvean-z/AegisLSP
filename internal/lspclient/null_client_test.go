package lspclient

import (
	"context"
	"testing"

	"github.com/Uvean-z/aegislsp/internal/types"
)

func TestNullLSPClient_IsInitialized(t *testing.T) {
	c := NewNullLSPClient()
	if c.IsInitialized() {
		t.Error("NullLSPClient.IsInitialized() should return false")
	}
}

func TestNullLSPClient_Definition(t *testing.T) {
	c := NewNullLSPClient()
	_, err := c.Definition(context.Background(), "file:///test.go", types.Position{Line: 0, Character: 0})
	if err != ErrNoActiveLSP {
		t.Errorf("expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_Hover(t *testing.T) {
	c := NewNullLSPClient()
	_, err := c.Hover(context.Background(), "file:///test.go", types.Position{Line: 0, Character: 0})
	if err != ErrNoActiveLSP {
		t.Errorf("expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_Initialize(t *testing.T) {
	c := NewNullLSPClient()
	err := c.Initialize(context.Background(), "file:///root", nil)
	if err != ErrNoActiveLSP {
		t.Errorf("expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_Shutdown(t *testing.T) {
	c := NewNullLSPClient()
	err := c.Shutdown(context.Background())
	if err != ErrNoActiveLSP {
		t.Errorf("expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_DocumentMethods(t *testing.T) {
	c := NewNullLSPClient()
	ctx := context.Background()

	if err := c.OpenDocument(ctx, "file:///t.go", "go", "pkg main"); err != ErrNoActiveLSP {
		t.Errorf("OpenDocument: expected ErrNoActiveLSP, got %v", err)
	}
	if err := c.CloseDocument(ctx, "file:///t.go"); err != ErrNoActiveLSP {
		t.Errorf("CloseDocument: expected ErrNoActiveLSP, got %v", err)
	}
	if err := c.ChangeDocument(ctx, "file:///t.go", "new"); err != ErrNoActiveLSP {
		t.Errorf("ChangeDocument: expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_NavigationMethods(t *testing.T) {
	c := NewNullLSPClient()
	ctx := context.Background()
	pos := types.Position{Line: 0, Character: 0}

	if _, err := c.TypeDefinition(ctx, "file:///t.go", pos); err != ErrNoActiveLSP {
		t.Errorf("TypeDefinition: expected ErrNoActiveLSP, got %v", err)
	}
	if _, err := c.Implementation(ctx, "file:///t.go", pos); err != ErrNoActiveLSP {
		t.Errorf("Implementation: expected ErrNoActiveLSP, got %v", err)
	}
	if _, err := c.References(ctx, "file:///t.go", pos); err != ErrNoActiveLSP {
		t.Errorf("References: expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_AnalysisMethods(t *testing.T) {
	c := NewNullLSPClient()
	ctx := context.Background()

	if _, err := c.DocumentSymbols(ctx, "file:///t.go"); err != ErrNoActiveLSP {
		t.Errorf("DocumentSymbols: expected ErrNoActiveLSP, got %v", err)
	}
	if _, err := c.WorkspaceSymbols(ctx, "query"); err != ErrNoActiveLSP {
		t.Errorf("WorkspaceSymbols: expected ErrNoActiveLSP, got %v", err)
	}
	if _, err := c.Diagnostics(ctx, "file:///t.go"); err != ErrNoActiveLSP {
		t.Errorf("Diagnostics: expected ErrNoActiveLSP, got %v", err)
	}
}

func TestNullLSPClient_OnNotification(t *testing.T) {
	c := NewNullLSPClient()
	// Should not panic.
	c.OnNotification("test", nil)
}
