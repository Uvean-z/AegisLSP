package types

import (
	"encoding/json"
	"time"
)

// Position represents a 0-based position in a text document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range represents a range between two positions in a text document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location represents a location inside a resource (file URI + range).
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// DiagnosticSeverity enumerates LSP diagnostic severities.
type DiagnosticSeverity int

// Diagnostic severity levels as defined by the LSP specification.
const (
	// SeverityError indicates an error-level diagnostic.
	SeverityError DiagnosticSeverity = 1
	// SeverityWarning indicates a warning-level diagnostic.
	SeverityWarning DiagnosticSeverity = 2
	// SeverityInformation indicates an informational diagnostic.
	SeverityInformation DiagnosticSeverity = 3
	// SeverityHint indicates a hint-level diagnostic.
	SeverityHint DiagnosticSeverity = 4
)

// Diagnostic represents a diagnostic reported by an LSP server.
// Code is json.RawMessage because the LSP spec allows int or string.
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Code     json.RawMessage    `json:"code,omitempty"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
}

// SymbolKind enumerates LSP symbol kinds.
type SymbolKind int

// Symbol kind values as defined by the LSP specification.
const (
	SymbolKindFile          SymbolKind = 1  // A file.
	SymbolKindModule        SymbolKind = 2  // A module.
	SymbolKindNamespace     SymbolKind = 3  // A namespace.
	SymbolKindPackage       SymbolKind = 4  // A package.
	SymbolKindClass         SymbolKind = 5  // A class.
	SymbolKindMethod        SymbolKind = 6  // A method.
	SymbolKindProperty      SymbolKind = 7  // A property.
	SymbolKindField         SymbolKind = 8  // A field.
	SymbolKindConstructor   SymbolKind = 9  // A constructor.
	SymbolKindEnum          SymbolKind = 10 // An enumeration.
	SymbolKindInterface     SymbolKind = 11 // An interface.
	SymbolKindFunction      SymbolKind = 12 // A function.
	SymbolKindVariable      SymbolKind = 13 // A variable.
	SymbolKindConstant      SymbolKind = 14 // A constant.
	SymbolKindString        SymbolKind = 15 // A string.
	SymbolKindNumber        SymbolKind = 16 // A number.
	SymbolKindBoolean       SymbolKind = 17 // A boolean.
	SymbolKindArray         SymbolKind = 18 // An array.
	SymbolKindObject        SymbolKind = 19 // An object.
	SymbolKindKey           SymbolKind = 20 // A key / property key.
	SymbolKindNull          SymbolKind = 21 // A null value.
	SymbolKindEnumMember    SymbolKind = 22 // An enum member.
	SymbolKindStruct        SymbolKind = 23 // A struct.
	SymbolKindEvent         SymbolKind = 24 // An event.
	SymbolKindOperator      SymbolKind = 25 // An operator.
	SymbolKindTypeParameter SymbolKind = 26 // A type parameter.
)

// SymbolInformation represents metadata about a symbol in source code.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// WorkspaceFolder represents a workspace folder in the LSP protocol.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// StreamSource identifies which output stream an entry came from.
type StreamSource int

// Stream source identifiers for parsed terminal output.
const (
	// SourceStdout indicates the entry came from the process's stdout.
	SourceStdout StreamSource = iota
	// SourceStderr indicates the entry came from the process's stderr.
	SourceStderr
)

// Entry represents a single parsed line from a terminal stream.
type Entry struct {
	Source    StreamSource `json:"source"`
	LineNum   int          `json:"lineNum"`
	Text      string       `json:"text"`
	Timestamp time.Time    `json:"timestamp"`
}

// ErrorEntry represents an error parsed from terminal output.
// File, Line, Column, and Code are extracted by pattern matching
// against known compiler/linter error formats.
type ErrorEntry struct {
	Entry
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
	Language string `json:"language,omitempty"`
}

// EnrichedError represents a terminal error augmented with LSP context.
// This is the final output of the fusion module.
type EnrichedError struct {
	ErrorEntry
	Diagnostic *Diagnostic         `json:"diagnostic,omitempty"`
	Symbols    []SymbolInformation `json:"symbols,omitempty"`
	Definition *Location           `json:"definition,omitempty"`
	References []Location          `json:"references,omitempty"`

	// Count records how many similar errors were folded into this entry.
	// 1 means no folding occurred; >1 means (Count-1) duplicates were suppressed.
	Count int `json:"count"`
}
