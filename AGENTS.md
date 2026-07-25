# AGENTS.md

## Cursor Cloud specific instructions

This repo is a single Go module (`design-compare`), an MCP server that compares
Figma designs against web implementations. There is no database, web port, or
frontend — the "application" is a stdio JSON-RPC MCP server.

### Toolchain
- Requires **Go 1.26+** (`go.mod` pins `go 1.26`). The VM snapshot has Go 1.26.x
  installed and `go` on `PATH` resolves to it. If `go version` ever reports an
  older toolchain, the pinned version won't build.

### Standard commands (from `README.md`)
- Build: `go build -o design-compare .`
- Test: `go test -v ./...` (fast, in-memory image/tree checks; no browser needed)
- Vet: `go vet ./...`

### Running the server (non-obvious)
- It communicates over **stdio using newline-delimited MCP JSON-RPC**, not HTTP.
  Running `./design-compare` alone just blocks waiting for stdin — that is normal.
- To exercise it manually, pipe an `initialize` request, an
  `notifications/initialized` message, then `tools/list` / `tools/call` into the
  binary's stdin (see the handshake used during setup). The only tool is
  `compare_design` with modes `layout_tree`, `perceptual`, and `strict`.
- Logs (e.g. "VRT Unified Compare MCP Server starting...") are written to
  **stderr**, so they do not corrupt the stdout JSON-RPC stream.
