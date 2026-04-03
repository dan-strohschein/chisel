package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dan-strohschein/cartograph/pkg/query"
)

// ImpactReport is the result of a comprehensive impact analysis
// combining type dependents, side effects, and error chains.
type ImpactReport struct {
	Symbol        string                    `json:"symbol"`
	Kind          string                    `json:"kind"`
	Dependents    []GraphNode               `json:"dependents"`
	Effects       map[string][]GraphNode    `json:"effects,omitempty"`
	ErrorChains   []GraphPath               `json:"error_chains,omitempty"`
	AffectedFiles []string                  `json:"affected_files"`
	Summary       string                    `json:"summary"`
}

// Impact performs a comprehensive impact analysis on a symbol by combining
// dependents, side effects, and error chain queries.
func Impact(querier *LibraryGraphQuerier, symbol string) (*ImpactReport, error) {
	report := &ImpactReport{Symbol: symbol}

	// Try to resolve the symbol — first as a type, then as a function
	var dependents *query.QueryResult
	var symbolKind string

	depResult, err := querier.Engine.TypeDependents(symbol)
	if err == nil {
		dependents = depResult
		symbolKind = "Type"
	} else {
		// Try as a function (callers)
		callResult, err2 := querier.Engine.CallStack(symbol, query.Reverse)
		if err2 != nil {
			return nil, fmt.Errorf("symbol not found: %s", symbol)
		}
		dependents = callResult
		symbolKind = "Function"
	}
	report.Kind = symbolKind

	// Collect dependent nodes
	for _, path := range dependents.Paths {
		for _, node := range path.Nodes {
			report.Dependents = append(report.Dependents, convertNode(node))
		}
	}
	report.Dependents = deduplicateGraphNodes(report.Dependents)

	// Query side effects (best-effort — only works for functions)
	effectReport, err := querier.Engine.SideEffects(symbol)
	if err == nil && len(effectReport.Effects) > 0 {
		report.Effects = make(map[string][]GraphNode)
		for category, nodes := range effectReport.Effects {
			var converted []GraphNode
			for _, n := range nodes {
				converted = append(converted, convertNode(n))
			}
			report.Effects[category] = converted
		}
	}

	// Query error chains (best-effort)
	errorResult, err := querier.Engine.ErrorProducers(symbol)
	if err == nil {
		for _, path := range errorResult.Paths {
			gp := GraphPath{Depth: path.Depth}
			for _, n := range path.Nodes {
				gp.Nodes = append(gp.Nodes, convertNode(n))
			}
			for _, e := range path.Edges {
				gp.Edges = append(gp.Edges, GraphEdge{
					Source: string(e.Source),
					Target: string(e.Target),
					Kind:   string(e.Kind),
					Label:  e.Label,
				})
			}
			report.ErrorChains = append(report.ErrorChains, gp)
		}
	}

	// Collect affected files from all nodes
	fileSet := make(map[string]bool)
	for _, n := range report.Dependents {
		if n.SourceFile != "" {
			fileSet[n.SourceFile] = true
		}
	}
	for _, nodes := range report.Effects {
		for _, n := range nodes {
			if n.SourceFile != "" {
				fileSet[n.SourceFile] = true
			}
		}
	}
	for f := range fileSet {
		report.AffectedFiles = append(report.AffectedFiles, f)
	}
	sort.Strings(report.AffectedFiles)

	// Build summary
	effectCount := len(report.Effects)
	report.Summary = fmt.Sprintf("%s (%s): %d dependents, %d effect categories, %d error chains, %d files",
		symbol, symbolKind, len(report.Dependents), effectCount, len(report.ErrorChains), len(report.AffectedFiles))

	return report, nil
}

// FormatImpactReport formats an ImpactReport as human-readable text.
func FormatImpactReport(r *ImpactReport) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Impact: %s (%s)\n", r.Symbol, r.Kind)
	fmt.Fprintf(&sb, "  Dependents: %d\n", len(r.Dependents))
	if len(r.Effects) > 0 {
		categories := make([]string, 0, len(r.Effects))
		for c := range r.Effects {
			categories = append(categories, c)
		}
		sort.Strings(categories)
		fmt.Fprintf(&sb, "  Effects: [%s]\n", strings.Join(categories, ", "))
	}
	if len(r.ErrorChains) > 0 {
		fmt.Fprintf(&sb, "  Error chains: %d\n", len(r.ErrorChains))
	}
	fmt.Fprintf(&sb, "  Files: %d\n", len(r.AffectedFiles))

	if len(r.Dependents) > 0 {
		sb.WriteString("\nDependents:\n")
		for _, n := range r.Dependents {
			loc := ""
			if n.SourceFile != "" {
				loc = fmt.Sprintf(" — %s:%d", n.SourceFile, n.SourceLine)
			}
			fmt.Fprintf(&sb, "  %s (%s)%s\n", n.QualifiedName, n.Module, loc)
		}
	}

	if len(r.Effects) > 0 {
		sb.WriteString("\nEffects:\n")
		for category, nodes := range r.Effects {
			fmt.Fprintf(&sb, "  [%s]\n", category)
			seen := make(map[string]bool)
			for _, n := range nodes {
				if seen[n.QualifiedName] {
					continue
				}
				seen[n.QualifiedName] = true
				loc := ""
				if n.SourceFile != "" {
					loc = fmt.Sprintf(" — %s:%d", n.SourceFile, n.SourceLine)
				}
				fmt.Fprintf(&sb, "    %s%s\n", n.QualifiedName, loc)
			}
		}
	}

	if len(r.AffectedFiles) > 0 {
		sb.WriteString("\nAffected files:\n")
		for _, f := range r.AffectedFiles {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}

	return sb.String()
}

// deduplicateGraphNodes removes duplicate GraphNodes by QualifiedName.
func deduplicateGraphNodes(nodes []GraphNode) []GraphNode {
	seen := make(map[string]bool)
	var result []GraphNode
	for _, n := range nodes {
		key := n.QualifiedName
		if key == "" {
			key = n.Name
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, n)
		}
	}
	return result
}

