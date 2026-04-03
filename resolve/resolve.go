package resolve

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dan-strohschein/aidkit/pkg/parser"
	"github.com/dan-strohschein/cartograph/pkg/query"
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
	var resolution *Resolution
	var err error

	switch intent.Kind {
	case Rename:
		resolution, err = r.resolveRename(intent)
	case Move:
		resolution, err = r.resolveMove(intent)
	case Propagate:
		resolution, err = r.resolvePropagate(intent)
	case Extract:
		resolution, err = r.resolveExtract(intent)
	default:
		return nil, fmt.Errorf("unknown refactor kind: %d", intent.Kind)
	}

	if err != nil {
		return nil, err
	}

	// Check lock safety when using library mode (has access to graph)
	if libQuerier, ok := r.Graph.(*LibraryGraphQuerier); ok {
		lockWarnings := CheckLockSafety(libQuerier, intent.Target)
		resolution.Warnings = append(resolution.Warnings, lockWarnings...)
	}

	return resolution, nil
}

func (r *Resolver) resolveRename(intent Intent) (*Resolution, error) {
	baseName := SymbolBaseName(intent.Target)
	isMethod := strings.Contains(intent.Target, ".")

	// Fast path: if a full-tree grep for this basename is safe (won't cause
	// false positives), skip the semantic graph entirely. Safety is determined
	// by analyzing AID files:
	// - If only one type has this method → always safe (unique name)
	// - If multiple types share it AND they're all related (interface
	//   implementations, mocks, adapters) → safe (renaming one requires all)
	// - If unrelated types share the name → NOT safe, use cartograph
	if isMethod && intent.AidDir != "" {
		if isGrepSafeForBaseName(intent.AidDir, intent.Target, baseName) {
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

	res := buildResolution(intent, *primary, locations, warnings)
	res.FastPath = true
	return res, nil
}

// isGrepSafeForBaseName determines whether a full-tree grep for the basename
// is safe (won't cause false positives). Uses a layered analysis:
//
//  1. Count types sharing this method name in AID files
//  2. If unique (1 type) → always safe
//  3. If the name is distinctive (long, multi-word like GetBundleByName) →
//     safe even with multiple types, since distinctive names don't collide
//     across unrelated types
//  4. If the name is generic (Close, Flush, Get) → check interface
//     relationships. Safe only if all types share an interface.
func isGrepSafeForBaseName(aidDir, target, baseName string) bool {
	files, err := filepath.Glob(filepath.Join(aidDir, "*.aid"))
	if err != nil {
		return false
	}

	// Phase 1: collect all types that have this method
	suffix := "." + baseName
	var typesWithMethod []string
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "@fn ") {
				fnName := strings.TrimPrefix(trimmed, "@fn ")
				if strings.HasSuffix(fnName, suffix) {
					typeName := fnName[:len(fnName)-len(suffix)]
					typesWithMethod = append(typesWithMethod, typeName)
				} else if fnName == baseName {
					typesWithMethod = append(typesWithMethod, "")
				}
			}
		}
	}

	if len(typesWithMethod) <= 1 {
		return true // Unique — always safe
	}

	// Phase 2: check name distinctiveness. Multi-word camelCase names like
	// "GetBundleByName" or "CleanTombstones" are distinctive enough that
	// different types won't independently invent the same name for unrelated
	// purposes. Single-word names like "Close", "Flush", "Get" are generic
	// and commonly appear on unrelated types.
	//
	// Heuristic: count uppercase letters (word boundaries in camelCase).
	// "Close" has 1, "CleanTombstones" has 2, "GetBundleByName" has 4.
	// Names with 2+ words are distinctive.
	upperCount := 0
	for _, ch := range baseName {
		if ch >= 'A' && ch <= 'Z' {
			upperCount++
		}
	}
	if upperCount >= 3 {
		return true // Distinctive multi-word name — safe for grep
	}

	// Phase 3: generic single-word name — check interface relationships.
	// Look for @requires blocks containing "fn <baseName>(" in AID files.
	var interfacesWithMethod []string
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		var currentType string
		inRequires := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "@type ") || strings.HasPrefix(trimmed, "@trait ") {
				tag := "@type "
				if strings.HasPrefix(trimmed, "@trait ") {
					tag = "@trait "
				}
				currentType = strings.TrimPrefix(trimmed, tag)
				inRequires = false
			}
			if trimmed == "@requires" {
				inRequires = true
			}
			if strings.HasPrefix(trimmed, "---") {
				inRequires = false
			}
			// Check for method in @requires block: "fn MethodName(" or just "MethodName("
			if inRequires && currentType != "" {
				if strings.Contains(trimmed, "fn "+baseName+"(") || strings.HasPrefix(trimmed, baseName+"(") {
					interfacesWithMethod = append(interfacesWithMethod, currentType)
				}
			}
			// Also check @purpose lines for "implements <Interface>" pattern
			if strings.HasPrefix(trimmed, "@purpose") && strings.Contains(trimmed, "implements") {
				// This is on a method — we'll use it in phase 3
			}
		}
	}

	// Phase 3: for each type with the method, check if it's related to the
	// target type (shares an interface, is a mock/fake, or references the target).
	targetType := ""
	if strings.Contains(target, ".") {
		targetType = strings.SplitN(target, ".", 2)[0]
	}

	// Build a set of interface names for quick lookup
	ifaceSet := make(map[string]bool)
	for _, iface := range interfacesWithMethod {
		ifaceSet[strings.ToLower(iface)] = true
	}

	unrelatedCount := 0
	for _, typeName := range typesWithMethod {
		if typeName == targetType {
			continue // The target itself — always included
		}
		// Check if this type is related to an interface or to the target type
		if isTypeRelatedToInterfaces(aidDir, typeName, ifaceSet, targetType) {
			continue // Related — safe to include
		}
		unrelatedCount++
	}

	return unrelatedCount == 0
}

// isTypeRelatedToInterfaces checks if a type implements any of the given
// interfaces by scanning its @purpose and @related annotations in AID files.
func isTypeRelatedToInterfaces(aidDir, typeName string, interfaces map[string]bool, targetType string) bool {
	files, err := filepath.Glob(filepath.Join(aidDir, "*.aid"))
	if err != nil {
		return false
	}

	lowerTarget := strings.ToLower(targetType)

	// Check if the type name itself contains the target type name.
	// E.g., "mockBundleServiceForBTreeRangeScan" contains "bundleservice".
	lowerTypeName := strings.ToLower(typeName)
	if strings.Contains(lowerTypeName, lowerTarget) {
		return true
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		inType := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Track when we're in the right @type block
			if strings.HasPrefix(trimmed, "@type "+typeName) || strings.HasPrefix(trimmed, "@trait "+typeName) {
				inType = true
				continue
			}
			if inType && (strings.HasPrefix(trimmed, "---") || (strings.HasPrefix(trimmed, "@type ") || strings.HasPrefix(trimmed, "@trait "))) {
				inType = false
			}

			if !inType {
				continue
			}

			// Check @related for the target type or any interface
			if strings.HasPrefix(trimmed, "@related") {
				lowerLine := strings.ToLower(trimmed)
				if strings.Contains(lowerLine, lowerTarget) {
					return true
				}
				for iface := range interfaces {
					if strings.Contains(lowerLine, iface) {
						return true
					}
				}
			}

			// Check @purpose for "implements <Interface>" patterns
			if strings.HasPrefix(trimmed, "@purpose") {
				lowerLine := strings.ToLower(trimmed)
				for iface := range interfaces {
					if strings.Contains(lowerLine, iface) {
						return true
					}
				}
				// Also check if purpose mentions the target type
				if strings.Contains(lowerLine, lowerTarget) {
					return true
				}
				// Check for common mock/test patterns
				if strings.Contains(lowerLine, "mock") || strings.Contains(lowerLine, "fake") || strings.Contains(lowerLine, "stub") {
					if strings.Contains(lowerLine, lowerTarget) {
						return true
					}
				}
			}
		}
	}

	return false
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
	// Prefer the error-aware path when the querier supports it.
	// ErrorProducers follows ProducesError + PropagatesError edges for
	// transitive error chain discovery, which is more complete than
	// the callstack-only approach.
	if eq, ok := r.Graph.(ErrorQuerier); ok {
		return r.resolvePropagateViaErrors(intent, eq)
	}
	return r.resolvePropagateViaCallstack(intent)
}

// resolvePropagateViaErrors uses cartograph's ErrorProducers query to find
// all functions in the error chain, including transitive propagation.
func (r *Resolver) resolvePropagateViaErrors(intent Intent, eq ErrorQuerier) (*Resolution, error) {
	nodes, err := r.findSymbolNodes(intent)
	if err != nil {
		return nil, err
	}
	primary := nodes[0]

	// Query error producers for the error type — this follows
	// ProducesError and PropagatesError edges transitively
	errorResult, err := eq.ErrorProducers(intent.ErrorType)
	if err != nil {
		// Fall back to callstack approach if error query fails
		return r.resolvePropagateViaCallstack(intent)
	}

	// Also get callers via callstack for call-site discovery
	callResult, _ := r.Graph.Query("callstack", []string{intent.Target, "--up"}, intent.AidDir)

	// Merge nodes from both error chain and call chain
	allNodes := []GraphNode{primary}
	locations, warnings := r.locateAll(allNodes, intent.SourceDir, intent.AidDir, SymbolBaseName(intent.Target), intent.Target)

	// Collect caller nodes from both queries
	baseName := SymbolBaseName(intent.Target)
	seen := make(map[string]bool)

	// Process error chain nodes
	errorNodes := collectNodes(errorResult)
	for _, node := range errorNodes {
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

	// Also process callstack nodes (may find callers the error graph missed)
	if callResult != nil {
		callerNodes := collectNodes(callResult)
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
	}

	locations = deduplicateLocations(locations)
	sortLocations(locations)

	resolution := buildResolution(intent, primary, locations, warnings)
	resolution.ErrorMap = readErrorMap(intent.AidDir)
	return resolution, nil
}

// resolvePropagateViaCallstack is the original propagate implementation
// using only callstack --up queries. Used as fallback when the querier
// doesn't support ErrorProducers (e.g., CLI mode).
func (r *Resolver) resolvePropagateViaCallstack(intent Intent) (*Resolution, error) {
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

	resolution := buildResolution(intent, primary, locations, warnings)
	resolution.ErrorMap = readErrorMap(intent.AidDir)
	return resolution, nil
}

// resolveExtract identifies a function and its private dependencies for
// extraction to a new package. Requires library mode.
func (r *Resolver) resolveExtract(intent Intent) (*Resolution, error) {
	libQuerier, ok := r.Graph.(*LibraryGraphQuerier)
	if !ok {
		return nil, fmt.Errorf("extract requires library mode (do not use --cartograph flag)")
	}

	nodes, err := r.findSymbolNodes(intent)
	if err != nil {
		return nil, err
	}
	primary := nodes[0]

	// Find all callees (forward call chain)
	callResult, err := libQuerier.Engine.CallStack(intent.Target, query.Forward)
	if err != nil {
		return nil, fmt.Errorf("querying callees: %w", err)
	}

	// Get all symbols in the target's current module
	moduleResult, err := libQuerier.Engine.ListModule(primary.Module)
	if err != nil {
		return nil, fmt.Errorf("listing module %s: %w", primary.Module, err)
	}

	// Build set of module-local symbols
	moduleSymbols := make(map[string]bool)
	if moduleResult != nil {
		for _, nodes := range moduleResult.Matches {
			for _, n := range nodes {
				moduleSymbols[n.QualifiedName] = true
			}
		}
	}

	// Identify private dependencies: callees in the same module that are
	// unexported and only called by functions being extracted.
	var dependencies []GraphNode
	calleeNodes := collectNodes(convertQueryResult(callResult))
	for _, callee := range calleeNodes {
		if callee.QualifiedName == primary.QualifiedName {
			continue
		}
		if callee.Module != primary.Module {
			continue
		}
		// Check if the symbol is exported (public). Exported symbols stay in
		// the original package since other code may depend on them.
		calleeBase := SymbolBaseName(callee.Name)
		if IsExported(calleeBase, intent.Language) {
			continue
		}
		dependencies = append(dependencies, callee)
	}

	// Locate all affected code
	allNodes := []GraphNode{primary}
	allNodes = append(allNodes, dependencies...)
	locations, warnings := r.locateAll(allNodes, intent.SourceDir, intent.AidDir, SymbolBaseName(intent.Target), intent.Target)

	// Also find import locations for the current module (callers need import updates)
	importLocs := findImportLocations(intent.SourceDir, primary.Module)
	locations = append(locations, importLocs...)

	resolution := buildResolution(intent, primary, locations, warnings)
	resolution.Dependencies = dependencies

	if len(dependencies) > 0 {
		depNames := make([]string, len(dependencies))
		for i, d := range dependencies {
			depNames[i] = d.Name
		}
		resolution.Warnings = append(resolution.Warnings,
			fmt.Sprintf("will also extract %d private dependencies: %s",
				len(dependencies), strings.Join(depNames, ", ")))
	}

	return resolution, nil
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

		// If cartograph didn't provide source_file/source_line, supplement
		// from AID files. This happens when AID uses bare filenames that
		// cartograph can't resolve to absolute paths.
		if primary.SourceFile == "" || primary.SourceLine == 0 {
			if aidNode, err := scanAidFilesForSymbol(intent.AidDir, intent.Target); err == nil {
				if primary.SourceFile == "" {
					primary.SourceFile = aidNode.SourceFile
				}
				if primary.SourceLine == 0 {
					primary.SourceLine = aidNode.SourceLine
				}
			}
		}

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

				// If source_file is a bare filename (no directory), try to
				// qualify it using the module name. This prevents ambiguity when
				// multiple packages have files with the same name (e.g.,
				// hashindexV2/hash_index_api.go vs hashindexV3/hash_index_api.go).
				if node.SourceFile != "" && !strings.Contains(node.SourceFile, "/") && module != "" {
					// Search for module/filename under the project root
					projectRoot := filepath.Dir(filepath.Clean(aidDir))
					var match string
					filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return nil
						}
						if info.Name() == node.SourceFile && strings.Contains(filepath.Dir(path), module) {
							match = path
							return filepath.SkipAll
						}
						return nil
					})
					if match != "" {
						node.SourceFile = match
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

	// Collect definition locations from graph node source metadata.
	// Track which files have graph-sourced definitions so grep can
	// focus on finding references rather than re-discovering definitions.
	graphDefinitionFiles := make(map[string]bool)
	for _, node := range nodes {
		locs := FindSourceLocations(node, sourceDir, aidDir, symbolName)
		for _, loc := range locs {
			graphDefinitionFiles[loc.File] = true
		}
		locations = append(locations, locs...)
	}
	hasGraphLocations := len(graphDefinitionFiles) > 0

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

		// Supplemental sweep: grep for type-qualified call patterns across the
		// source tree. Cartograph may miss callers when the call graph has
		// indirect edges (e.g., Close calls storage.FlushWithHeaderUpdate
		// instead of Flush directly). Searching for common variable patterns
		// like "hashIndex.Flush" catches these without matching unrelated types.
		typeName := strings.SplitN(fullTarget, ".", 2)[0]
		typeLocs := grepTypedMethodCalls(sourceDir, typeName, symbolName)
		locations = append(locations, typeLocs...)
	} else if hasGraphLocations {
		// When graph nodes provided definition locations, grep only for
		// references (call sites, type refs) — don't re-discover definitions.
		// This is faster and avoids duplicate definition entries.
		grepLocs := grepSymbol(sourceDir, symbolName)
		locations = append(locations, grepLocs...)
	} else {
		// No graph source locations — grep the whole tree for everything
		grepLocs := grepSymbol(sourceDir, symbolName)
		locations = append(locations, grepLocs...)
	}

	// Sweep test files for references. Cartograph's graph is built from AID
	// files which typically don't cover test packages, so test call sites are
	// invisible to the scoped grep above. A targeted grep of *_test.go files
	// closes this gap without scanning the entire source tree.
	testLocs := grepSymbolInTests(sourceDir, symbolName)
	locations = append(locations, testLocs...)

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

// grepTypedMethodCalls greps for common variable-name patterns that indicate
// a call on the target type. For type "HashIndexV3" and method "Flush", this
// searches for patterns like "hashIndex.Flush" and "newIndex.Flush" — variable
// names that contain the core type name (minus version suffixes).
// This catches callers that cartograph misses when AID @calls edges use
// internal method names instead of the public API method.
func grepTypedMethodCalls(sourceDir, typeName, methodName string) []Location {
	if sourceDir == "" || typeName == "" || methodName == "" {
		return nil
	}

	// Build a case-insensitive grep pattern for common Go variable naming:
	// Type "HashIndexV3" → core "hashindex" → pattern "[hH]ashindex.*\.Flush"
	// This matches: hashIndex.Flush, newHashIndex.Flush, hi.Flush won't match (too short)
	lower := strings.ToLower(typeName)
	core := strings.TrimRight(strings.TrimRight(lower, "0123456789"), "v")
	if len(core) < 4 {
		core = lower
	}

	// grep for the pattern: any word containing the core type name, followed by .Method
	// Case-insensitive (-i) because Go variables use camelCase (hashIndex) while
	// the core type name is lowercase (hashindex). The edit phase's ScopeMatch
	// handles exact method name matching.
	pattern := core + "[a-zA-Z0-9_]*\\." + methodName
	cmd := exec.Command("grep", "-rn", "-i", "-E", "--include=*.go", pattern, sourceDir)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseGrepOutput(string(output))
}

// grepSymbolInTests runs grep on test files to find test references.
// AID skeletons typically don't cover test packages, so cartograph's graph
// misses test call sites. This targeted sweep catches them without the cost
// of a full source tree grep. Matches both *_test.go (standard) and *_tests.go
// (non-standard but used in some projects).
func grepSymbolInTests(sourceDir, symbolName string) []Location {
	if sourceDir == "" || symbolName == "" {
		return nil
	}
	cmd := exec.Command("grep", "-rn", "--include=*_test.go", "--include=*_tests.go", symbolName, sourceDir)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseGrepOutput(string(output))
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

// parseErrorMapFromAIDText extracts @error_map / @entries blocks from raw AID
// text. This works with aidkit versions that do not yet register error_map as
// a structured annotation (e.g. v0.1.0), while still matching AID line rules
// via parser.ClassifyLine.
func parseErrorMapFromAIDText(content string) map[string]ErrorHandling {
	out := make(map[string]ErrorHandling)
	scanner := bufio.NewScanner(strings.NewReader(content))
	waitingEntries := false
	inEntries := false

	for scanner.Scan() {
		line := scanner.Text()
		lt, fieldName, _ := parser.ClassifyLine(line)

		switch lt {
		case parser.LineSeparator:
			waitingEntries = false
			inEntries = false

		case parser.LineField:
			switch {
			case fieldName == "error_map":
				waitingEntries = true
				inEntries = false
			case fieldName == "entries" && waitingEntries:
				inEntries = true
				waitingEntries = false
			default:
				waitingEntries = false
				inEntries = false
			}

		case parser.LineContinuation:
			if !inEntries {
				continue
			}
			funcName, handling := parseErrorMapEntry(strings.TrimSpace(line))
			if funcName != "" {
				out[funcName] = handling
			}

		case parser.LineBlank, parser.LineComment:
			// skip
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeErrorMapAnnotations copies @error_map entries from parsed annotations
// into dest (used when aidkit recognizes error_map as an annotation).
func mergeErrorMapAnnotations(dest map[string]ErrorHandling, af *parser.AidFile) {
	if af == nil {
		return
	}
	for _, ann := range af.Annotations {
		if ann.Kind != "error_map" {
			continue
		}
		entriesField, ok := ann.Fields["entries"]
		if !ok {
			continue
		}
		for _, line := range entriesField.Lines {
			funcName, handling := parseErrorMapEntry(strings.TrimSpace(line))
			if funcName != "" {
				dest[funcName] = handling
			}
		}
	}
}

// readErrorMap scans AID files in aidDir for @error_map annotations and
// returns a map of function name → ErrorHandling strategy. The @error_map
// annotation fields contain entries like:
//
//	@entries
//	  BundleService.GetBundle: wrap "fetching bundle: %w"
//	  CacheService.Get: log
//	  Validator.Validate: return
//	  Parser.Parse: convert ValidationError
func readErrorMap(aidDir string) map[string]ErrorHandling {
	if aidDir == "" {
		return nil
	}

	errorMap := make(map[string]ErrorHandling)

	entries, err := os.ReadDir(aidDir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".aid") {
			continue
		}
		path := filepath.Join(aidDir, e.Name())

		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if textMap := parseErrorMapFromAIDText(string(raw)); textMap != nil {
			for k, v := range textMap {
				errorMap[k] = v
			}
		}

		af, _, err := parser.ParseFile(path)
		if err != nil {
			continue
		}
		mergeErrorMapAnnotations(errorMap, af)
	}

	if len(errorMap) == 0 {
		return nil
	}
	return errorMap
}

// parseErrorMapEntry parses a single @error_map entry line like:
//
//	BundleService.GetBundle: wrap "fetching bundle: %w"
//	CacheService.Get: log
//	Validator.Validate: return
//	Parser.Parse: convert ValidationError
func parseErrorMapEntry(line string) (string, ErrorHandling) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) < 2 {
		return "", ErrorHandling{}
	}
	funcName := strings.TrimSpace(parts[0])
	rest := strings.TrimSpace(parts[1])

	if rest == "" {
		return funcName, ErrorHandling{Strategy: "return"}
	}

	fields := strings.Fields(rest)
	strategy := fields[0]

	switch strategy {
	case "wrap":
		msg := ""
		// Extract quoted message
		if idx := strings.Index(rest, "\""); idx >= 0 {
			endIdx := strings.LastIndex(rest, "\"")
			if endIdx > idx {
				msg = rest[idx+1 : endIdx]
			}
		}
		return funcName, ErrorHandling{Strategy: "wrap", WrapMsg: msg}
	case "log":
		return funcName, ErrorHandling{Strategy: "log"}
	case "convert":
		target := ""
		if len(fields) > 1 {
			target = fields[1]
		}
		return funcName, ErrorHandling{Strategy: "convert", ConvertTo: target}
	default:
		return funcName, ErrorHandling{Strategy: "return"}
	}
}
