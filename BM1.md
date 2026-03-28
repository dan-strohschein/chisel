# BM1: Error Propagation Benchmark (Run 5 — Clean Run 2026-03-28)

## Task
Add `error` return to `IsFieldForeignKey()` in SyndrDB and propagate through all callers.

**Before:** `func IsFieldForeignKey(bundle *models.Bundle, fieldName string) (bool, string, string)`
**After:** `func IsFieldForeignKey(bundle *models.Bundle, fieldName string) (bool, string, string, error)`

Call chain: `CreateHashIndex` and `createHashIndexInternal` both call `IsFieldForeignKey` — both need `if err != nil` added.

## Results

| Metric | Agent A (Vanilla) | Agent B (Chisel+Cartograph) | Delta |
|--------|-------------------|----------------------------|-------|
| **Tokens consumed** | 17,402 | **13,866** | **-20%** |
| **Tool calls** | 20 | **8** | **-60%** |
| **Duration (ms)** | 73,812 | **40,575** | **-45%** |
| **Files modified** | 2 | 2 | — |
| **Call sites updated** | 2 | 2 | — |
| **Build passes** | Yes | Yes | — |
| **Accuracy** | 2/2 (100%) | 2/2 (100%) | — |
| **Used chisel/cartograph** | N/A | **Yes — both ran, chisel --apply succeeded** | — |

## Chisel Propagate Finally Works

This is the **first benchmark run where `chisel propagate --apply` was used successfully**.

Agent B's workflow:
1. Ran `cartograph callstack IsFieldForeignKey --up` — found 1 caller (CreateHashIndex)
2. Ran `chisel propagate IsFieldForeignKey error` (dry-run) — reviewed diff showing all 6 edits
3. Ran `chisel propagate --apply` — applied all edits in one command
4. Ran `go build` and `go vet` — both pass
5. Done in **8 tool calls total**

Agent A's workflow:
1. Grep for function definition
2. Read the file
3. Edit the signature
4. Edit 3 return statements
5. Grep for call sites
6. Read caller file
7. Edit call site 1
8. Edit call site 2
9. Run build
10. ... (20 tool calls total)

## What Chisel Propagate Did Automatically

All 6 edits in one `--apply`:
- Signature: `(bool, string, string)` → `(bool, string, string, error)`
- 3 return statements: added `, nil`
- 2 call sites: added `, err :=` and `if err != nil { return err }`

## Historical Comparison (All Runs)

| Metric | R1 V | R2 V | R3 V | R4 V | **R5 V** | R1 C | R2 C | R3 C | R4 C | **R5 C** |
|--------|------|------|------|------|----------|------|------|------|------|----------|
| Tokens | 17K | 16K | 17K | 16K | **17K** | 18K | 16K | 18K | 20K | **14K** |
| Tools | 16 | 12 | 18 | 14 | **20** | 18 | 13 | 21 | 25 | **8** |
| Time | 62s | 51s | 65s | 58s | **74s** | 257s | 49s | 65s | 93s | **41s** |
| Propagate? | — | — | — | — | — | No | No | Buggy | Denied | **Yes!** |

## Analysis

**Chisel wins decisively.** The 60% reduction in tool calls is the clearest signal — chisel replaced ~12 individual grep/read/edit operations with a single `--apply` command. This translated directly to 45% less wall-clock time and 20% fewer tokens.

**What changed since previous runs:**
- Run 1-2: Agents couldn't edit (permission issues)
- Run 3: Chisel propagate had bugs (malformed signatures, caller corruption)
- Run 4: Bash permissions denied for subagent
- **Run 5: All fixed** — blanket Bash permission + propagate bugs fixed

## Verdict

**Chisel wins.** Even for this simple 2-caller task, the automated propagation saves meaningful work. The advantage would be larger with deeper call chains.
