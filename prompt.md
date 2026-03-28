# Chisel Implementation Prompt

Use this prompt to bootstrap the Chisel implementation from the AID skeleton.

---

## Context

You are implementing **Chisel**, a semantic refactoring CLI tool for codebases that use AID (Architecture Intent Documents). Chisel performs codebase-wide refactoring operations — rename, move, error propagation — using the semantic graph instead of text-level find-and-replace.

The complete design spec lives in `.aidocs/`. Read `.aidocs/manifest.aid` first, then each package's `.aid` file. The AID files define every type, function signature, decision, and antipattern. **Your job is to implement code that matches these contracts exactly.**

There is a Claude skill file at `.claude/skills/cartograph.md` that documents the cartograph CLI tool — read it for the full command reference and JSON output schema.

## Architecture: CLI-to-CLI

Chisel does **not** import cartograph as a Go library. Cartograph's packages are all `internal/` — they can't be imported from outside the module. Instead, Chisel shells out to the `cartograph` binary:

```
chisel rename Foo Bar
  └── cartograph depends Foo --format json --dir .aidocs/
  └── cartograph callstack Foo --both --format json --dir .aidocs/
  └── grep -rn "Foo" ./src/
  └── generates edits → applies or previews diff
```

**Dependencies:**
- `cartograph` binary on PATH (or at a known location — see `.claude/skills/cartograph.md`)
- `github.com/dan-strohschein/aidkit/pkg/parser` — Go library for parsing AID files directly (used for AID file updates in the edit phase)

This means `go.mod` only needs the aidkit dependency. Cartograph is a runtime dependency (binary), not a compile-time one.

## Implementation Order

Build bottom-up following the data flow pipeline. Each phase should compile and have basic tests before moving to the next.

### Phase 1: Project Scaffolding

```
chisel/
├── .aidocs/            # Already exists — the design spec
├── .claude/skills/     # Already exists — cartograph skill
├── CLAUDE.md           # Already exists
├── go.mod              # Create: module github.com/dan-strohschein/chisel
├── cmd/chisel/
│   └── main.go         # Stub — just prints "chisel v0.1"
├── resolve/
│   └── resolve.go
│   └── cartograph.go   # Cartograph CLI wrapper
├── edit/
│   └── edit.go
└── patch/
    └── patch.go
```

1. Create `go.mod` with dependency on aidkit only.
2. Create stub files for each package with types defined but functions returning `nil`/`error`.
3. Run `go build ./...` — it must compile.

### Phase 2: `resolve` Package

Read `.aidocs/resolve.aid` for the complete spec.

**The key insight:** Chisel's resolve phase is a thin orchestrator. Cartograph does the heavy graph work. Chisel's job is to:
1. Ask cartograph the right question for each refactor type
2. Parse the JSON response
3. Map graph nodes to actual source file locations via grep
4. Return a clean `Resolution` struct

**Types to implement:**
- `Intent`, `RefactorKind`, `Location`, `Resolution` — as specified in the AID
- `GraphNode`, `GraphResult`, `GraphPath`, `GraphEdge` — lightweight structs matching cartograph's JSON output schema (defined in resolve.aid)

**Key functions:**

1. **`RunCartograph(command string, args []string, aidDir string) (*GraphResult, error)`** — The bridge to cartograph. Runs `exec.Command("cartograph", command, args..., "--format", "json", "--dir", aidDir)`, captures stdout, unmarshals JSON into `GraphResult`. Returns structured errors from stderr if exit code is non-zero.

2. **`Resolve(intent Intent) (*Resolution, error)`** — Dispatches based on `intent.Kind`:
   - Rename → calls `RunCartograph("depends", ...)` for types, `RunCartograph("callstack", ..., "--both")` for functions
   - Move → calls `RunCartograph("depends", ...)`
   - Propagate → calls `RunCartograph("callstack", ..., "--up")`
   Then calls `FindSourceLocations` for each node in the result.

3. **`FindSourceLocations(node GraphNode, sourceDir, symbolName string) []Location`** — If `node.SourceFile` is set, use it as primary location. Then run `grep -rn <symbolName> <sourceDir>` to find all occurrences. Parse grep output (`file:line:content`) into `Location` structs. Deduplicate.

4. **`ResolveRename`, `ResolveMove`, `ResolvePropagate`** — Each calls the appropriate cartograph commands and collects locations.

**Cartograph command mapping:**
| Refactor | Symbol Kind | Cartograph Command |
|----------|------------|-------------------|
| Rename type | Type | `depends <Type>` |
| Rename function | Function | `callstack <fn> --both` |
| Rename field | Field | `field <Type.Field>` |
| Move | any | `depends <Symbol>` |
| Propagate | Function | `callstack <fn> --up` |

### Phase 3: `edit` Package

Read `.aidocs/edit.aid` for the complete spec. This phase is **pure Go** — no external tools.

**Types:** `Edit`, `EditKind`, `EditSet` — as specified.

**Functions:**
1. **`GenerateEdits`** — Dispatch to rename/move/propagate generators based on intent kind.
2. **`GenerateRenameEdits`** — For each location, read the source line, replace old name with new name. Use `ScopeMatch` to skip strings/comments.
3. **`GenerateMoveEdits`** — Generate import path updates + qualified reference updates.
4. **`GeneratePropagateEdits`** — Change function signature to add error return. Wrap call sites with error handling.
5. **`ScopeMatch`** — Heuristic: walk the line tracking quote/comment state.
6. **`GenerateAidEdits`** — Update AID files using aidkit/parser to read them, then generate edits for `@fn`/`@type` names, `@sig` fields, cross-references.

**Key rule:** Edits sorted by line descending within each file (bottom-to-top application).

### Phase 4: `patch` Package

Read `.aidocs/patch.aid` for the complete spec.

**Types:** `Patch`, `PatchOptions` — as specified.

**Functions:**
1. **`Apply`** — Group edits by file, apply each, collect diffs. Source first, then AID.
2. **`ApplyToFile`** — Read file, apply edits bottom-to-top, generate diff.
3. **`GenerateDiff`** — Unified diff format. Can use `github.com/pmezard/go-difflib` or implement minimal.
4. **`FormatPatch`** — unified/json/summary output formatting.

**Key:** `DryRun` defaults to `true`.

### Phase 5: `cmd/chisel` CLI

Read `.aidocs/cli.aid` for the complete spec.

- Parse subcommand: `rename`, `move`, `propagate`
- Build `Intent` from CLI args
- Run pipeline: `Resolve → GenerateEdits → Apply`
- Print result via `FormatPatch`
- Global flags: `--dir`, `--src`, `--apply`, `--backup`, `--format`
- Auto-discover `.aidocs/` by walking up from CWD
- Exit codes: 0 = success, 1 = error, 2 = ambiguous symbol

### Phase 6: Tests

Create a `testdata/` directory with a small fixture project containing `.aidocs/` files and source files. Tests shell out to a real (or mocked) cartograph binary.

Priority:
1. **`resolve`** — Test `RunCartograph` with known JSON. Test `FindSourceLocations` with a fixture directory.
2. **`edit`** — Test `ScopeMatch` thoroughly. Test rename edit generation with mock resolution.
3. **`patch`** — Test `ApplyToFile` with known edits. Verify dry-run. Verify backup.
4. **Integration** — End-to-end: fixture project → full pipeline → verify diff output.

For unit tests that shouldn't require cartograph, extract an interface:
```go
type GraphQuerier interface {
    Query(command string, args []string, aidDir string) (*GraphResult, error)
}
```
Production uses `CLIGraphQuerier` (shells out). Tests use `MockGraphQuerier` (returns canned JSON).

## Antipatterns to Avoid

From the AID spec — violating any of these is a bug:

1. **Don't parse source code with ASTs.** Chisel is language-agnostic. AID + cartograph + grep.
2. **Don't import cartograph as a Go library.** Shell out to the binary. Parse JSON output.
3. **Don't modify files during resolution.** Resolve is read-only.
4. **Don't apply edits during generation.** Edit generation is pure.
5. **Don't update AID before source.** If source edits fail, AID stays unchanged.
6. **Don't assume AID is complete.** Grep fallback is essential.
7. **Don't skip resolve for "simple" renames.** The graph finds references grep would miss.
8. **Don't buffer the entire source tree.** Load files on demand.
9. **Don't auto-pick ambiguous symbols.** Fail with all candidates listed.
10. **Don't add confirmation prompts.** AI agents invoke this programmatically. Dry-run is the safety mechanism.
