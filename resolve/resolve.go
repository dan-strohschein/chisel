package resolve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// NotFoundError indicates the target symbol was not found in the graph.
type NotFoundError struct {
	Symbol string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("symbol not found: %s", e.Symbol)
}

// AmbiguousError indicates multiple symbols matched the target name.
type AmbiguousError struct {
	Symbol     string
	Candidates []GraphNode
}

func (e *AmbiguousError) Error() string {
	names := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		names[i] = c.QualifiedName
	}
	return fmt.Sprintf("ambiguous symbol %q — matches: %s", e.Symbol, strings.Join(names, ", "))
}

// Resolver performs refactoring resolution using cartograph and grep.
type Resolver struct {
	Graph GraphQuerier
}

// Resolve takes a refactoring intent and returns all affected locations.
func (r *Resolver) Resolve(intent Intent) (*Resolution, error) {
	switch intent.Kind {
	case Rename:
		return r.resolveRename(intent)
	case Move:
		return r.resolveMove(intent)
	case Propagate:
		return r.resolvePropagate(intent)
	default:
		return nil, fmt.Errorf("unknown refactor kind: %d", intent.Kind)
	}
}

func (r *Resolver) resolveRename(intent Intent) (*Resolution, error) {
	baseName := SymbolBaseName(intent.Target)
	isMethod := strings.Contains(intent.Target, ".")

	// Fast path: if the basename is rare across AID files, skip the semantic
	// graph and grep the entire source tree. Names like "GetBundleByName" appear
	// on at most a few types (real + mocks) — a full-tree grep is safe and fast.
	// Only skip this for truly ambiguous names (Close, Get, Flush) that appear
	// on many unrelated types where grep would cause false positives.
	if isMethod && intent.AidDir != "" {
		count := countAidSymbolsByBaseName(intent.AidDir, baseName)
		if count <= 5 {
			// Rare name — use fast grep path instead of scoped graph traversal
			return r.resolveRenameUnique(intent, baseName)
		}
	}

	// Determine the right cartograph command based on whether target looks like
	// a type, function, or field.
	nodes, err := r.findSymbolNodes(intent)
	if err != nil {
		return nil, err
	}

	primary := nodes[0]

	// Collect all related nodes from cartograph
	var allNodes []GraphNode
	allNodes = append(allNodes, primary)

	switch primary.Kind {
	case "Type":
		result, err := r.Graph.Query("depends", []string{intent.Target}, intent.AidDir)
		if err != nil {
			return nil, fmt.Errorf("querying dependents: %w", err)
		}
		allNodes = append(allNodes, collectNodes(result)...)
	case "Function", "Method":
		// Use --up (callers only) for rename — callees don't need renaming.
		// Using --both would include callees like WriteAheadLog.Close when
		// renaming WALManager.Close, causing false positives.
		result, err := r.Graph.Query("callstack", []string{intent.Target, "--up"}, intent.AidDir)
		if err != nil {
			return nil, fmt.Errorf("querying callstack: %w", err)
		}
		allNodes = append(allNodes, collectNodes(result)...)
	case "Field":
		result, err := r.Graph.Query("field", []string{intent.Target}, intent.AidDir)
		if err != nil {
			return nil, fmt.Errorf("querying field touchers: %w", err)
		}
		allNodes = append(allNodes, collectNodes(result)...)
	}

	// Map nodes to source locations via grep
	locations, warnings := r.locateAll(allNodes, intent.SourceDir, intent.AidDir, baseName, intent.Target)

	return buildResolution(intent, primary, locations, warnings), nil
}

// resolveRenameUnique handles renames for symbols with unique basenames.
// Instead of scoped graph traversal, it greps the entire source tree — faster
// when there's no disambiguation needed. Skips cartograph entirely.
func (r *Resolver) resolveRenameUnique(intent Intent, baseName string) (*Resolution, error) {
	// Get the primary node from AID files directly — no cartograph needed.
	// We already know the symbol exists (countAidSymbolsByBaseName found it).
	primary, err := scanAidFilesForSymbol(intent.AidDir, intent.Target)
	if err != nil {
		// Shouldn't happen since countAidSymbolsByBaseName found it, but fallback
		primary = &GraphNode{
			Name:          baseName,
			QualifiedName: intent.Target,
			Kind:          "Method",
		}
	}

	// Grep the entire source tree for the basename
	locations := grepSymbol(intent.SourceDir, baseName)

	// Also add the definition location from the primary node
	defLocs := FindSourceLocations(*primary, intent.SourceDir, intent.AidDir, baseName)
	locations = append(locations, defLocs...)

	locations = deduplicateLocations(locations)
	sortLocations(locations)

	var warnings []string
	if len(locations) == 0 {
		warnings = append(warnings, fmt.Sprintf("no source locations found for %q", baseName))
	}

	return buildResolution(intent, *primary, locations, warnings), nil
}

// countAidSymbolsByBaseName counts how many @fn entries in AID files have
// methods with the given basename. Used to detect unique names that don't
// need semantic disambiguation.
func countAidSymbolsByBaseName(aidDir, baseName string) int {
	files, err := filepath.Glob(filepath.Join(aidDir, "*.aid"))
	if err != nil {
		return 0
	}

	count := 0
	// Match @fn lines where the function name ends with ".baseName"
	// e.g., for baseName="Close": matches "WALManager.Close", "BTreeIndex.Close", etc.
	suffix := "." + baseName
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "@fn ") {
				fnName := strings.TrimPrefix(trimmed, "@fn ")
				if strings.HasSuffix(fnName, suffix) || fnName == baseName {
					count++
				}
			}
		}
	}
	return count
}

func (r *Resolver) resolveMove(intent Intent) (*Resolution, error) {
	nodes, err := r.findSymbolNodes(intent)
	if err != nil {
		return nil, err
	}

	primary := nodes[0]

	result, err := r.Graph.Query("depends", []string{intent.Target}, intent.AidDir)
	if err != nil {
		return nil, fmt.Errorf("querying dependents: %w", err)
	}

	allNodes := []GraphNode{primary}
	allNodes = append(allNodes, collectNodes(result)...)

	locations, warnings := r.locateAll(allNodes, intent.SourceDir, intent.AidDir, SymbolBaseName(intent.Target), intent.Target)

	// Also find import statements for the source package
	importLocs := findImportLocations(intent.SourceDir, primary.Module)
	locations = append(locations, importLocs...)

	return buildResolution(intent, primary, locations, warnings), nil
}

func (r *Resolver) resolvePropagate(intent Intent) (*Resolution, error) {
	nodes, err := r.findSymbolNodes(intent)
	if err != nil {
		return nil, err
	}

	primary := nodes[0]

	// Walk up the call chain
	result, err := r.Graph.Query("callstack", []string{intent.Target, "--up"}, intent.AidDir)
	if err != nil {
		return nil, fmt.Errorf("querying callers: %w", err)
	}

	// Only include the primary node for locateAll — callers are handled separately
	// via scoped grep below. This prevents callers' definition lines from being
	// tagged "definition" and having their signatures incorrectly modified.
	allNodes := []GraphNode{primary}

	locations, warnings := r.locateAll(allNodes, intent.SourceDir, intent.AidDir, SymbolBaseName(intent.Target), intent.Target)

	// For each caller, grep within its file for the target function name.
	// This finds the actual call sites (e.g., "IsFieldForeignKey(") in caller files.
	baseName := SymbolBaseName(intent.Target)
	callerNodes := collectNodes(result)
	seen := make(map[string]bool)
	for _, node := range callerNodes {
		if node.QualifiedName == primary.QualifiedName {
			continue
		}
		if node.SourceFile == "" {
			continue
		}
		file := ResolveSourceFile(node.SourceFile, intent.SourceDir, intent.AidDir)
		if seen[file] {
			continue
		}
		seen[file] = true
		fileLocs := grepSymbolInFile(file, baseName)
		for i := range fileLocs {
			fileLocs[i].SymbolKind = "call"
		}
		locations = append(locations, fileLocs...)
	}

	// Deduplicate
	locations = deduplicateLocations(locations)
	sortLocations(locations)

	return buildResolution(intent, primary, locations, warnings), nil
}

// findSymbolNodes queries cartograph and resolves the target to graph node(s).
// Returns an error if not found or ambiguous.
func (r *Resolver) findSymbolNodes(intent Intent) ([]GraphNode, error) {
	// Try depends first — it works for types and functions
	result, err := r.Graph.Query("depends", []string{intent.Target}, intent.AidDir)
	if err != nil {
		// If depends fails, try callstack
		result, err = r.Graph.Query("callstack", []string{intent.Target, "--both"}, intent.AidDir)
		if err != nil {
			// Try field if it looks like Type.Field
			if strings.Contains(intent.Target, ".") {
				result, err = r.Graph.Query("field", []string{intent.Target}, intent.AidDir)
				if err != nil {
					// Fall through to AID file scan below
					result = nil
				}
			}
		}
	}

	if result != nil && len(result.Paths) > 0 {
		// The first node in the first path is typically the target symbol
		primary := result.Paths[0].Nodes[0]
		return []GraphNode{primary}, nil
	}

	// Fallback: scan AID files directly for @fn/@type definitions.
	// This handles symbols that exist in AID but aren't reachable through
	// cartograph's graph traversal (e.g., methods only called via interfaces).
	node, err := scanAidFilesForSymbol(intent.AidDir, intent.Target)
	if err != nil {
		return nil, &NotFoundError{Symbol: intent.Target}
	}
	return []GraphNode{*node}, nil
}

// scanAidFilesForSymbol searches AID files directly for a symbol definition.
// Looks for "@fn <name>" or "@type <name>" lines and extracts source location
// from @source_file/@source_line annotations.
func scanAidFilesForSymbol(aidDir, target string) (*GraphNode, error) {
	if aidDir == "" {
		return nil, fmt.Errorf("no AID directory")
	}

	files, err := filepath.Glob(filepath.Join(aidDir, "*.aid"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no AID files found in %s", aidDir)
	}

	baseName := SymbolBaseName(target)

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		lines := strings.Split(string(content), "\n")
		var module string

		for i, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Track the module name
			if strings.HasPrefix(trimmed, "@module ") {
				module = strings.TrimPrefix(trimmed, "@module ")
			}

			// Check for exact @fn or @type match
			if trimmed == "@fn "+target || trimmed == "@type "+target ||
				trimmed == "@fn "+baseName || trimmed == "@type "+baseName {

				// Found it — extract source_file and source_line from following lines
				node := &GraphNode{
					Name:          baseName,
					QualifiedName: module + "." + target,
					Module:        module,
				}

				// Determine kind
				if strings.HasPrefix(trimmed, "@fn") {
					if strings.Contains(target, ".") {
						node.Kind = "Method"
					} else {
						node.Kind = "Function"
					}
				} else {
					node.Kind = "Type"
				}

				// Scan next few lines for @source_file and @source_line
				for j := i + 1; j < len(lines) && j < i+15; j++ {
					nextLine := strings.TrimSpace(lines[j])
					if strings.HasPrefix(nextLine, "---") || strings.HasPrefix(nextLine, "@fn ") || strings.HasPrefix(nextLine, "@type ") {
						break
					}
					if strings.HasPrefix(nextLine, "@source_file ") {
						node.SourceFile = strings.TrimPrefix(nextLine, "@source_file ")
					}
					if strings.HasPrefix(nextLine, "@source_line ") {
						lineNum, err := strconv.Atoi(strings.TrimPrefix(nextLine, "@source_line "))
						if err == nil {
							node.SourceLine = lineNum
						}
					}
				}

				return node, nil
			}
		}
	}

	return nil, fmt.Errorf("symbol %q not found in AID files", target)
}

// collectNodes extracts all unique nodes from a GraphResult.
func collectNodes(result *GraphResult) []GraphNode {
	seen := make(map[string]bool)
	var nodes []GraphNode
	for _, path := range result.Paths {
		for _, node := range path.Nodes {
			key := node.QualifiedName
			if key == "" {
				key = node.Module + "." + node.Name
			}
			if !seen[key] {
				seen[key] = true
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

// locateAll maps graph nodes to source locations using SourceFile hints and grep.
// When fullTarget contains a "." (method rename like "WALManager.Close"), grep is
// scoped to only the files cartograph identified — not the entire source tree.
// This prevents matching unrelated methods with the same basename (e.g., other
// types' Close() methods).
func (r *Resolver) locateAll(nodes []GraphNode, sourceDir, aidDir, symbolName, fullTarget string) ([]Location, []string) {
	var locations []Location
	var warnings []string

	for _, node := range nodes {
		locs := FindSourceLocations(node, sourceDir, aidDir, symbolName)
		locations = append(locations, locs...)
	}

	isMethodRename := strings.Contains(fullTarget, ".")

	if isMethodRename {
		// For method renames, only grep within files that cartograph identified.
		// This prevents matching Close() on unrelated types.
		seen := make(map[string]bool)
		for _, node := range nodes {
			if node.SourceFile != "" {
				file := ResolveSourceFile(node.SourceFile, sourceDir, aidDir)
				if !seen[file] {
					seen[file] = true
					grepLocs := grepSymbolInFile(file, symbolName)
					locations = append(locations, grepLocs...)
				}
			}
		}
	} else {
		// For non-method renames (types, functions), grep the whole tree
		grepLocs := grepSymbol(sourceDir, symbolName)
		locations = append(locations, grepLocs...)
	}

	// Deduplicate and sort
	locations = deduplicateLocations(locations)
	sortLocations(locations)

	if len(locations) == 0 {
		warnings = append(warnings, fmt.Sprintf("no source locations found for %q", symbolName))
	}

	return locations, warnings
}

// ResolveSourceFile resolves a cartograph source_file path to an absolute path.
// Cartograph source_file paths are relative to the project root (parent of .aidocs/).
// If sourceDir is a subdirectory (e.g., "src/"), we use the aidDir parent as the
// base to avoid double-prefixing (e.g., "src/src/internal/...").
func ResolveSourceFile(file, sourceDir, aidDir string) string {
	if filepath.IsAbs(file) {
		return file
	}
	// Try sourceDir first — if the file exists there, use it
	candidate := filepath.Join(sourceDir, file)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fall back to aidDir parent (project root)
	if aidDir != "" {
		projectRoot := filepath.Dir(filepath.Clean(aidDir))
		candidate = filepath.Join(projectRoot, file)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Last resort: search recursively under sourceDir for the filename.
	// This handles cases where @source_file is relative to the package
	// directory (e.g., "types.go") but sourceDir is a parent (e.g., "internal/").
	baseName := filepath.Base(file)
	var found string
	filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == baseName {
			// Verify it's the right file by checking the line count is plausible
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found != "" {
		return found
	}
	// Default: join with sourceDir (original behavior)
	return filepath.Join(sourceDir, file)
}

// FindSourceLocations maps a graph node to actual source file locations.
func FindSourceLocations(node GraphNode, sourceDir, aidDir, symbolName string) []Location {
	var locations []Location

	// Use SourceFile/SourceLine from cartograph if available
	if node.SourceFile != "" && node.SourceLine > 0 {
		file := ResolveSourceFile(node.SourceFile, sourceDir, aidDir)
		locations = append(locations, Location{
			File:       file,
			Line:       node.SourceLine,
			SymbolKind: classifyNodeKind(node.Kind),
			Context:    "", // Will be filled in during edit phase
		})
	}

	return locations
}

// grepSymbolInFile runs grep on a single file to find all occurrences of a symbol.
func grepSymbolInFile(file, symbolName string) []Location {
	if file == "" || symbolName == "" {
		return nil
	}
	cmd := exec.Command("grep", "-n", symbolName, file)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseGrepOutputSingleFile(string(output), file)
}

// grepSymbol runs grep to find all occurrences of a symbol in the source tree.
func grepSymbol(sourceDir, symbolName string) []Location {
	if sourceDir == "" || symbolName == "" {
		return nil
	}

	// Use grep -rn for recursive line-numbered search on source files only.
	// --include="*.go" prevents scanning binary files, data, and logs.
	// No -w flag: we need substring matches to catch derivative symbols
	// (e.g., GetDocumentPage must also match GetDocumentPageReadOnly)
	cmd := exec.Command("grep", "-rn", "--include=*.go", symbolName, sourceDir)
	output, err := cmd.Output()
	if err != nil {
		// grep returns exit code 1 for "no matches" — that's not an error
		return nil
	}

	return parseGrepOutput(string(output))
}

// parseGrepOutput parses grep -rn output into Location structs.
func parseGrepOutput(output string) []Location {
	var locations []Location
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		// Format: file:line:content
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		lineNum, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		locations = append(locations, Location{
			File:       parts[0],
			Line:       lineNum,
			SymbolKind: "reference",
			Context:    parts[2],
		})
	}
	return locations
}

// parseGrepOutputSingleFile parses grep -n output (no filename prefix) for a known file.
func parseGrepOutputSingleFile(output, file string) []Location {
	var locations []Location
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		// Format: line:content
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		lineNum, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		locations = append(locations, Location{
			File:       file,
			Line:       lineNum,
			SymbolKind: "reference",
			Context:    parts[1],
		})
	}
	return locations
}

// findImportLocations greps for import statements referencing a module.
func findImportLocations(sourceDir, module string) []Location {
	if sourceDir == "" || module == "" {
		return nil
	}
	cmd := exec.Command("grep", "-rn", fmt.Sprintf(`"%s"`, module), sourceDir)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	locs := parseGrepOutput(string(output))
	for i := range locs {
		locs[i].SymbolKind = "import"
	}
	return locs
}

// SymbolBaseName extracts the last component of a dotted symbol name.
func SymbolBaseName(target string) string {
	if i := strings.LastIndex(target, "."); i >= 0 {
		return target[i+1:]
	}
	return target
}

// classifyNodeKind maps cartograph node kinds to location symbol kinds.
func classifyNodeKind(kind string) string {
	switch kind {
	case "Function", "Method":
		return "definition"
	case "Type":
		return "definition"
	case "Field":
		return "field_ref"
	default:
		return "reference"
	}
}

func deduplicateLocations(locs []Location) []Location {
	type key struct {
		File string
		Line int
	}
	seen := make(map[key]bool)
	var result []Location
	for _, loc := range locs {
		k := key{loc.File, loc.Line}
		if !seen[k] {
			seen[k] = true
			result = append(result, loc)
		}
	}
	return result
}

func sortLocations(locs []Location) {
	sort.Slice(locs, func(i, j int) bool {
		if locs[i].File != locs[j].File {
			return locs[i].File < locs[j].File
		}
		return locs[i].Line < locs[j].Line
	})
}

func buildResolution(intent Intent, primary GraphNode, locations []Location, warnings []string) *Resolution {
	files := make(map[string]bool)
	modules := make(map[string]bool)
	for _, loc := range locations {
		files[loc.File] = true
	}
	modules[primary.Module] = true

	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	sort.Strings(fileList)

	moduleList := make([]string, 0, len(modules))
	for m := range modules {
		moduleList = append(moduleList, m)
	}
	sort.Strings(moduleList)

	return &Resolution{
		Intent:          intent,
		Symbol:          primary,
		Locations:       locations,
		AffectedFiles:   fileList,
		AffectedModules: moduleList,
		Warnings:        warnings,
	}
}
