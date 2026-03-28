# Chisel

Semantic refactoring tool powered by AID + Cartograph. Performs precise, codebase-wide refactoring operations using the semantic graph instead of text-level find-and-replace.

## AID Documentation

This project uses AID skeleton files in `.aidocs/` as the design spec.

- **Read `.aidocs/manifest.aid` first** to see all packages and their `@key_risks`
- **Implement code to match the AID contracts** — signatures, types, and workflows are the spec
- Check `@antipatterns` before making architectural decisions
- Check `@decision` blocks to understand WHY things are designed a certain way

## Architecture

4 packages, pipeline architecture:

- **resolve** — Reads AID files, builds Cartograph graph, finds every reference to a symbol. Pure read-only.
- **edit** — Generates precise text edits from resolved locations. Pure transform, no I/O.
- **patch** — Applies edits to files or outputs as unified diff. The only package that writes to disk.
- **cli** — CLI interface. `chisel rename|move|propagate`. Dry-run by default.

Pipeline: `Intent → Resolve → Edit → Patch`

## Commands

```
chisel rename <old> <new>                    Rename a symbol across the codebase
chisel move <symbol> <destination-package>   Move a symbol to another package
chisel propagate <function> <error-type>     Add error return and propagate through callers
```

All commands default to dry-run (preview diff). Pass `--apply` to modify files.

## Dependencies

- `github.com/dan-strohschein/aidkit/pkg/parser` — AID file parser
- `github.com/dan-strohschein/cartograph` — Semantic graph and query engine

## Build

```bash
go build -o chisel ./cmd/chisel
```

## Key Design Decisions

- **Dry-run by default** — AI agents invoke CLI tools programmatically. No confirmation prompts. Instead, the default is read-only. The agent reviews the diff, then explicitly passes `--apply`.
- **Language-agnostic** — Chisel doesn't parse source code with ASTs. It uses AID for structure and grep for location discovery. Works for any language AID supports.
- **AID edits are separate** — Source files are edited first. AID files are only updated after source edits succeed. This prevents AID/source desync.
- **Bottom-to-top edit application** — Edits within a file are applied from the last line to the first, so line numbers don't shift during application.
