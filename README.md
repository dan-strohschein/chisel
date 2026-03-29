# Chisel

Semantic refactoring tool powered by [AID](https://github.com/dan-strohschein/AID-Docs) + [Cartograph](https://github.com/dan-strohschein/cartograph). Performs precise, codebase-wide renames, moves, and error propagation using a semantic graph instead of text-level find-and-replace.

## Why Chisel?

A plain `sed` rename of `Close()` touches every type's `Close()` in the codebase. Chisel renames only `Head.Close()` — leaving `DB.Close()`, `File.Close()`, and 76 other types untouched.

For distinctive names like `GetBundleByName`, chisel skips the graph entirely and greps the full source tree in under a second — matching vanilla performance with zero overhead.

## Install

### Prerequisites

- Go 1.21+
- [AID](https://github.com/dan-strohschein/AID-Docs) — `.aidocs/` skeleton files for your project
- [Cartograph](https://github.com/dan-strohschein/cartograph) — semantic graph engine (binary on PATH or specified via flag)

### Build from source

```bash
git clone https://github.com/dan-strohschein/chisel.git
cd chisel
go build -o chisel ./cmd/chisel
```

### Generate AID files for your project

Chisel requires `.aidocs/` skeleton files. Generate them with `aid-gen-go`:

```bash
# Install the AID generator
go install github.com/dan-strohschein/aidkit/cmd/aid-gen-go@latest

# Generate skeletons (including test coverage)
aid-gen-go -test -output .aidocs ./...
```

## Usage

### Rename a symbol

```bash
# Dry-run (default) — preview the diff
chisel rename QueryEngine GraphQueryEngine \
  -dir .aidocs -src . -cartograph cartograph

# Apply changes
chisel rename QueryEngine GraphQueryEngine \
  -dir .aidocs -src . -cartograph cartograph -apply
```

### Rename a method (disambiguation)

When renaming a method shared by multiple types, chisel uses the semantic graph to target only the right type:

```bash
chisel rename WALManager.Close Shutdown \
  -dir .aidocs -src . -cartograph cartograph
```

### With LSP for type verification

For generic method names shared by many types (Close, Flush, Get), pass an LSP server for precise type checking at each call site:

```bash
chisel rename Head.Close Shutdown \
  -dir .aidocs -src . -cartograph cartograph \
  -lsp-cmd "gopls serve"
```

Supported LSP servers:

| Language | Command |
|----------|---------|
| Go | `gopls serve` |
| Python | `pyright-langserver --stdio` |
| TypeScript | `typescript-language-server --stdio` |
| Rust | `rust-analyzer` |
| C/C++ | `clangd` |

If the LSP server isn't available, chisel falls back to heuristic disambiguation automatically.

### Move a symbol

```bash
chisel move QueryEngine internal/newpkg \
  -dir .aidocs -src . -cartograph cartograph
```

### Propagate error returns

Add an error return to a function and propagate through all callers:

```bash
chisel propagate IsFieldForeignKey error \
  -dir .aidocs -src . -cartograph cartograph
```

## Flags

| Flag | Description |
|------|-------------|
| `-dir` | Path to `.aidocs/` directory (default: auto-discover) |
| `-src` | Path to source tree (default: parent of `.aidocs/`) |
| `-apply` | Actually modify files (default: dry-run preview) |
| `-cartograph` | Path to cartograph binary (default: find on PATH) |
| `-lsp-cmd` | LSP server command for type verification |
| `-include-comments` | Also rename occurrences in comments |
| `-backup .bak` | Create backup files before modifying |
| `-format json` | Output as JSON instead of unified diff |

## Using with AI Agents

Chisel is designed to be invoked by AI coding agents (Claude Code, Copilot, Cursor, etc.) as a CLI tool. The recommended workflow:

### 1. Install the Claude Code skill (optional)

If using Claude Code, copy the skill file to enable automatic chisel invocation:

```bash
mkdir -p ~/.claude/skills/chisel
cp docs/SKILL.md ~/.claude/skills/chisel/SKILL.md
```

The skill teaches Claude when and how to use chisel for refactoring tasks.

### 2. Agent workflow

```
1. Agent runs:  chisel rename Old New -dir .aidocs -src .    (dry-run)
2. Agent reviews the diff output
3. Agent runs:  chisel rename Old New -dir .aidocs -src . -apply
4. Agent runs:  go build ./...    (or equivalent build command)
5. Agent greps for any remaining references chisel missed
6. Agent fixes stragglers with targeted sed
```

### 3. When to use LSP

Agents should pass `-lsp-cmd` when renaming generic method names:

- **Distinctive names** (GetBundleByName, CompactStaleHead): No LSP needed — chisel's fast path handles these in <1 second
- **Generic names** (Close, Flush, Get, Reset): Use `-lsp-cmd "gopls serve"` to prevent false positives across 10-80+ types sharing the name

## How It Works

Chisel uses a pipeline architecture: **Intent -> Resolve -> Edit -> Patch**

### Resolve phase

Chisel automatically chooses the fastest safe strategy:

1. **Fast path** — If the method name is distinctive (multi-word camelCase like `GetBundleByName`), skip the graph entirely and grep the full source tree. Sub-second.
2. **Interface-aware analysis** — If multiple types share a generic name, check AID files for interface relationships. Types sharing an interface are renamed together; independent types are excluded.
3. **Graph path** — For ambiguous generic names, query Cartograph's semantic graph to find callers/dependents, then scope grep to only those files.
4. **LSP verification** — When `-lsp-cmd` is provided, verify each ambiguous call site's type through the language server.

### Edit phase

Generates precise text edits with:
- Word-boundary matching (won't rename `GetDocumentPage` inside `GetDocumentPageReadOnly`)
- Comment filtering (code-only by default, `--include-comments` to include)
- Method definition detection (distinguishes `func (x *Head) Close()` from `func (x *DB) Close()`)
- Test file sweep (finds references in `*_test.go` files outside AID coverage)

### Patch phase

Applies edits bottom-to-top within each file (preserving line numbers), outputs unified diff or JSON, and updates AID skeleton files after source edits succeed.

## Architecture

```
chisel/
  cmd/chisel/     CLI entry point
  resolve/        Symbol resolution (AID + Cartograph + grep)
  edit/           Edit generation (text transforms, scope matching)
  patch/          File modification and diff output
```

## Dependencies

- [aidkit](https://github.com/dan-strohschein/aidkit) — AID file parser
- [cartograph](https://github.com/dan-strohschein/cartograph) — Semantic graph query engine

## License

MIT
