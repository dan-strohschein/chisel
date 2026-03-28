# Benchmark Re-run Prompt

Copy-paste everything below the line into a fresh Claude Code session in the chisel project directory.

---

Re-run the chisel benchmarks BM1 and BM2. The AID skeletons, chisel bugs, and permissions are already fixed from the previous session. You just need to execute the benchmarks.

## Setup

- **SyndrDB** is at `/Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB`
- **SyndrDB `.aidocs/`** already exists with 12 package skeletons and a manifest
- **Chisel binary** is at `/Users/danstrohschein/Documents/CodeProjects/AI/chisel/chisel` (rebuild with `go build -o chisel ./cmd/chisel` if needed)
- **Cartograph binary** is at `/Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph`
- Bash permissions are pre-configured for go, git, chisel, cartograph, and shell utilities
- SyndrDB is in `additionalDirectories` in settings

## Verify tools work first

Before running benchmarks, verify:
```
/Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph callstack IsFieldForeignKey --up --format json --dir /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.aidocs/
```
and:
```
/Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph callstack WALManager.Close --up --format json --dir /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.aidocs/
```
Both should return JSON with paths. If they fail, the AID files may need fixing.

## BM1: Error Propagation

**Task:** Add `error` return to `IsFieldForeignKey()` in SyndrDB and propagate through all callers.

Create two worktrees from SyndrDB HEAD:
- `bm1-vanilla` branch + worktree
- `bm1-chisel` branch + worktree (copy `.aidocs/` into this worktree since it's untracked)

### Agent A prompt (vanilla — standard tools only, NO chisel/cartograph):

> You are working in the SyndrDB codebase at /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.claude/worktrees/bm1-vanilla.
>
> **Task:** Add an `error` return to the function `IsFieldForeignKey` in `src/internal/domain/bundle/bundle_service_schema.go` and propagate the error through all callers.
>
> 1. Find `IsFieldForeignKey` — it currently returns `(bool, string, string)`. Change it to return `(bool, string, string, error)`.
> 2. Find every call site that calls `IsFieldForeignKey` in the codebase.
> 3. At each call site, update the variable assignment to capture the new `error` return value.
> 4. Add `if err != nil { return ..., err }` error handling after each call site.
> 5. Update any comments that reference the function's return values.
>
> After making all changes, verify with: `go build ./src/internal/domain/bundle/...`
>
> Do NOT use any external tools like chisel or cartograph. Report: files modified, call sites updated, build result.

### Agent B prompt (chisel+cartograph):

> You are working in the SyndrDB codebase at /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.claude/worktrees/bm1-chisel.
>
> **Task:** Add an `error` return to the function `IsFieldForeignKey` in `src/internal/domain/bundle/bundle_service_schema.go` and propagate the error through all callers.
>
> 1. Find `IsFieldForeignKey` — it currently returns `(bool, string, string)`. Change it to return `(bool, string, string, error)`.
> 2. Find every call site that calls `IsFieldForeignKey` in the codebase.
> 3. At each call site, update the variable assignment to capture the new `error` return value.
> 4. Add `if err != nil { return ..., err }` error handling after each call site.
> 5. Update any comments that reference the function's return values.
>
> After making all changes, verify with: `go build ./src/internal/domain/bundle/...`
>
> **You have access to these tools:**
> 1. **Cartograph** at `/Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph`. Use it to trace the call chain:
>    ```
>    /Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph callstack IsFieldForeignKey --up --format json --dir .aidocs/
>    ```
> 2. **Chisel** at `/Users/danstrohschein/Documents/CodeProjects/AI/chisel/chisel`. Try:
>    ```
>    /Users/danstrohschein/Documents/CodeProjects/AI/chisel/chisel propagate IsFieldForeignKey error --dir .aidocs/ --src src/
>    ```
>
> The project has `.aidocs/` files. Use cartograph and/or chisel to find call sites and understand the call chain. Fall back to manual methods if needed. Report: files modified, call sites updated, build result, which tools you used.

## BM2: Ambiguous Rename

**Task:** Rename `WALManager.Close()` to `WALManager.Shutdown()` WITHOUT touching `Close()` on 40+ other types.

Create two more worktrees:
- `bm2-vanilla` branch + worktree
- `bm2-chisel` branch + worktree (copy `.aidocs/`)

### Agent A prompt (vanilla):

> You are working in the SyndrDB codebase at /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.claude/worktrees/bm2-vanilla.
>
> **Task:** Rename the `Close()` method on the `WALManager` type (in the `journal` package) to `Shutdown()`.
>
> **CRITICAL:** ONLY rename `Close()` on `WALManager`. Do NOT rename `Close()` on any other type. There are 40+ other types with `Close()` methods — those must remain unchanged.
>
> What needs to change:
> 1. The method definition: `func (wm *WALManager) Close() error` → `func (wm *WALManager) Shutdown() error`
> 2. Every call site where `Close()` is called on a WALManager variable
> 3. Call sites in tests where the variable is a `*WALManager`
> 4. Comments referencing `WALManager.Close`
> 5. Do NOT change `wm.wal.Close()` inside the method body — that's `WriteAheadLog.Close()`
>
> After making all changes, verify with: `go build ./src/internal/journal/...`
>
> Do NOT use any external tools like chisel or cartograph. Report: files modified, call sites updated, build result, how many Close() calls you examined but left unchanged.

### Agent B prompt (chisel+cartograph):

> You are working in the SyndrDB codebase at /Users/danstrohschein/Documents/CodeProjects/golang/SyndrDB/.claude/worktrees/bm2-chisel.
>
> **Task:** Rename the `Close()` method on the `WALManager` type (in the `journal` package) to `Shutdown()`.
>
> **CRITICAL:** ONLY rename `Close()` on `WALManager`. Do NOT rename `Close()` on any other type. There are 40+ other types with `Close()` methods — those must remain unchanged.
>
> What needs to change:
> 1. The method definition: `func (wm *WALManager) Close() error` → `func (wm *WALManager) Shutdown() error`
> 2. Every call site where `Close()` is called on a WALManager variable
> 3. Call sites in tests where the variable is a `*WALManager`
> 4. Comments referencing `WALManager.Close`
> 5. Do NOT change `wm.wal.Close()` inside the method body — that's `WriteAheadLog.Close()`
>
> After making all changes, verify with: `go build ./src/internal/journal/...`
>
> **You have access to these tools:**
> 1. **Cartograph** at `/Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph`. Find what depends on WALManager:
>    ```
>    /Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph depends WALManager --format json --dir .aidocs/
>    /Users/danstrohschein/Documents/CodeProjects/AI/cartograph/cartograph callstack WALManager.Close --up --format json --dir .aidocs/
>    ```
> 2. **Chisel** at `/Users/danstrohschein/Documents/CodeProjects/AI/chisel/chisel`.
>
> Use cartograph to identify which functions interact with WALManager, then precisely target your renames. Report: files modified, call sites updated, build result, which tools you used, how many Close() calls you examined but left unchanged.

## Execution

For each benchmark:
1. Record `date +%s` before launching the agent
2. Launch the agent (use `isolation: "worktree"` or manual worktrees)
3. Record `date +%s` after agent completes
4. The agent result will include `<usage>` tags with `total_tokens`, `tool_uses`, and `duration_ms`
5. Run verification on both worktrees:
   ```bash
   # For BM1:
   grep -rn "IsFieldForeignKey" --include="*.go" src/internal/domain/bundle/
   go build ./src/internal/domain/bundle/...

   # For BM2:
   grep -rn "WALManager.*Close\|walManager\.Close\|wm\.Close" --include="*.go" src/internal/journal/
   grep -rn "Shutdown" --include="*.go" src/internal/journal/
   go build ./src/internal/journal/...
   ```
6. Compare diffs: `git diff HEAD -- '*.go'`

## Output

Write results to `/Users/danstrohschein/Documents/CodeProjects/AI/chisel/BM1.md` and `BM2.md`, overwriting the previous versions. Include exact numbers from the `<usage>` tags.

## Previous Results (for comparison)

Last run's BM2 was invalid — Agent B couldn't execute Bash commands (permissions were denied). This re-run should show whether cartograph actually helps with disambiguation when the agent can use it.

| Metric | BM1 Previous | BM2 Previous |
|--------|-------------|-------------|
| Vanilla tokens | 17,316 | 34,743 |
| Chisel tokens | 17,974 | 27,188 |
| Vanilla time | 62s | 95s |
| Chisel time | 257s | 110s |
| Agent B used tools? | No (fell back) | No (Bash denied) |
