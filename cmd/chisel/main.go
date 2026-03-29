package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dan-strohschein/chisel/edit"
	"github.com/dan-strohschein/chisel/patch"
	"github.com/dan-strohschein/chisel/resolve"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcmd := os.Args[1]
	if subcmd == "version" || subcmd == "--version" {
		fmt.Printf("chisel v%s\n", version)
		return
	}

	// Global flags
	fs := flag.NewFlagSet("chisel", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to .aidocs/ directory (default: auto-discover)")
	src := fs.String("src", "", "Path to source tree (default: parent of .aidocs/)")
	apply := fs.Bool("apply", false, "Actually modify files (default: dry-run)")
	backup := fs.String("backup", "", "Create backup files with this suffix before modifying (e.g., .bak)")
	format := fs.String("format", "unified", "Output format: unified, json, summary")
	cartographBin := fs.String("cartograph", "", "Path to cartograph binary (default: find on PATH)")
	includeComments := fs.Bool("include-comments", false, "Also rename occurrences in comments")
	lspCmd := fs.String("lsp-cmd", "", "LSP server command for type verification (e.g., 'gopls serve', 'pyright-langserver --stdio')")

	// Parse flags from args after the subcommand and its positional args
	var positional []string
	args := os.Args[2:]
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(args[i]) > 1 && args[i][0] == '-' {
			flagArgs = append(flagArgs, args[i])
			// Check if this is a value flag
			name := args[i]
			if name[0] == '-' {
				name = name[1:]
			}
			if name[0] == '-' {
				name = name[1:]
			}
			switch name {
			case "dir", "src", "backup", "format", "cartograph", "lsp-cmd":
				if i+1 < len(args) {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			case "apply", "include-comments":
				// Boolean flags — no value to consume
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	fs.Parse(flagArgs)

	// Resolve directories
	aidDir := resolveAidDir(*dir)
	if aidDir == "" {
		fmt.Fprintf(os.Stderr, "Warning: no .aidocs/ directory found. Cartograph queries will be unavailable; falling back to grep-only resolution.\n")
	}
	sourceDir := *src
	if sourceDir == "" {
		if aidDir != "" {
			sourceDir = filepath.Dir(aidDir)
		} else {
			wd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: could not determine working directory: %v\n", err)
				os.Exit(1)
			}
			sourceDir = wd
		}
	}

	// Build intent
	intent, err := buildIntent(subcmd, positional, aidDir, sourceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printUsage()
		os.Exit(1)
	}
	intent.IncludeComments = *includeComments

	// Set up resolver
	querier := &resolve.CLIGraphQuerier{BinaryPath: *cartographBin}
	resolver := &resolve.Resolver{Graph: querier}

	// Phase 1: Resolve
	resolution, err := resolver.Resolve(intent)
	if err != nil {
		if _, ok := err.(*resolve.AmbiguousError); ok {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error resolving: %v\n", err)
		os.Exit(1)
	}

	// Set up type resolver (LSP or null)
	var typeResolver resolve.TypeResolver = &resolve.NullResolver{}
	if *lspCmd != "" {
		parts := strings.Fields(*lspCmd)
		lspResolver, err := resolve.NewLSPResolver(parts[0], parts[1:], sourceDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: LSP server failed to start: %v (falling back to heuristics)\n", err)
		} else {
			typeResolver = lspResolver
			defer lspResolver.Close()
		}
	}

	// Phase 2: Generate edits
	editSet, err := edit.GenerateEdits(resolution, typeResolver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating edits: %v\n", err)
		os.Exit(1)
	}

	// Phase 3: Apply / preview
	options := patch.PatchOptions{
		DryRun:       !*apply,
		UpdateAid:    true,
		BackupSuffix: *backup,
		OutputFormat: *format,
	}

	result, err := patch.Apply(editSet, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error applying: %v\n", err)
		os.Exit(1)
	}

	// Output
	fmt.Println(patch.FormatPatch(result, *format))

	// Print warnings
	for _, w := range resolution.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
}

func buildIntent(subcmd string, positional []string, aidDir, sourceDir string) (resolve.Intent, error) {
	base := resolve.Intent{
		AidDir:    aidDir,
		SourceDir: sourceDir,
	}

	switch subcmd {
	case "rename":
		if len(positional) < 2 {
			return base, fmt.Errorf("rename requires: chisel rename <old> <new>")
		}
		base.Kind = resolve.Rename
		base.Target = positional[0]
		base.NewName = positional[1]
		return base, nil

	case "move":
		if len(positional) < 2 {
			return base, fmt.Errorf("move requires: chisel move <symbol> <destination>")
		}
		base.Kind = resolve.Move
		base.Target = positional[0]
		base.Destination = positional[1]
		return base, nil

	case "propagate":
		if len(positional) < 2 {
			return base, fmt.Errorf("propagate requires: chisel propagate <function> <error-type>")
		}
		base.Kind = resolve.Propagate
		base.Target = positional[0]
		base.ErrorType = positional[1]
		return base, nil

	default:
		return base, fmt.Errorf("unknown command: %s", subcmd)
	}
}

func resolveAidDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := wd; ; d = filepath.Dir(d) {
		candidate := filepath.Join(d, ".aidocs")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
	}
	return ""
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `chisel — semantic refactoring powered by AID + Cartograph

Usage:
  chisel rename <old> <new>                    Rename a symbol across the codebase
  chisel move <symbol> <destination-package>   Move a symbol to another package
  chisel propagate <function> <error-type>     Add error return and propagate through callers

Flags:
  --dir <path>          Path to .aidocs/ directory (default: auto-discover)
  --src <path>          Path to source tree (default: parent of .aidocs/)
  --apply               Actually modify files (default: dry-run preview)
  --backup <suffix>     Create backup files before modifying (e.g., --backup .bak)
  --format <fmt>        Output format: unified (default), json, summary
  --cartograph <path>   Path to cartograph binary (default: find on PATH)
  --include-comments    Also rename occurrences in comments (default: code only)
  --lsp-cmd <cmd>       LSP server for type verification (e.g., "gopls serve")

All commands default to dry-run. Pass --apply to modify files.
`)
}
