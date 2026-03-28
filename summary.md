# Chisel Build Summary

## Build Metrics

| Metric | Value |
|--------|-------|
| **Total tool uses** | ~75 |
| **Estimated tokens consumed** | ~80,000 input + ~30,000 output |
| **Wall-clock time (start to finish)** | ~25 minutes |
| **Files created** | 16 |
| **Files modified** | 4 |
| **Lines of Go code written** | ~1,150 |
| **Lines of AID spec** | ~280 (across 5 .aid files) |
| **Lines of test code** | ~350 |

## AID and Cartograph Usage

| Activity | Count | Notes |
|----------|-------|-------|
| **AID file reads** | 7 | manifest.aid (2x), resolve.aid (2x), edit.aid, patch.aid, cli.aid — read once during design, re-read during implementation |
| **AID file edits** | 4 | Updated manifest.aid (2x) and resolve.aid (2x) to reflect CLI-over-library architecture |
| **AID files created** | 2 | testdata fixture .aid files for tests |
| **Cartograph source reads** | 4 | main.go, json.go, errors.go, go.mod — to understand the CLI interface and JSON output schema |
| **Cartograph binary invocations** | 1 | Smoke test with `./chisel rename` against test fixtures |
| **Claude skill file for cartograph** | 1 | Created `.claude/skills/cartograph.md` documenting all commands and JSON schema |

## Architecture Decision Made During Build

The original plan imported cartograph as a Go library. During the conversation, we discovered:

1. All cartograph packages are under `internal/` (can't import from external modules)
2. Cartograph is designed as a CLI tool with `--format json` output
3. The right approach: **shell out to the binary, parse JSON**

This changed:
- `resolve.aid` — rewrote to use `RunCartograph()` wrapper instead of direct graph imports
- `go.mod` — only depends on aidkit (for AID file editing), not cartograph
- Added `GraphQuerier` interface for testability (mock vs real binary)
- Created cartograph Claude skill file so Claude can use it as a tool during development

## What Was Built

### Packages (4)
- **resolve/** — Cartograph CLI wrapper + grep-based source location discovery
- **edit/** — Pure edit generation (rename, move, propagate) with scope-aware filtering
- **patch/** — File modification with dry-run default, backup support, unified diff output
- **cmd/chisel/** — CLI with 3 subcommands, auto-discovery, safe defaults

### Key Files
```
cmd/chisel/main.go      — CLI entry point, flag parsing, pipeline orchestration
resolve/types.go         — Intent, Location, Resolution, GraphNode/Result/Path/Edge
resolve/cartograph.go    — GraphQuerier interface, CLIGraphQuerier implementation
resolve/resolve.go       — Resolve, ResolveRename/Move/Propagate, FindSourceLocations, grep
edit/types.go            — Edit, EditKind, EditSet
edit/edit.go             — GenerateEdits, ScopeMatch, rename/move/propagate generators
patch/patch.go           — Apply, ApplyToFile, GenerateDiff, FormatPatch
```

### Tests
- **resolve_test.go** — MockGraphQuerier, rename resolution, not-found error, grep parsing, JSON unmarshaling
- **edit_test.go** — ScopeMatch (7 cases), rename edit generation, full pipeline, ReadLineFromFile
- **patch_test.go** — Dry-run verification, backup creation, diff generation, format outputs, full EditSet apply

All tests pass: `go test ./...` — 3/3 packages OK.
