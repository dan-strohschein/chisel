package resolve

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dan-strohschein/cartograph/pkg/graph"
	"github.com/dan-strohschein/cartograph/pkg/loader"
	"github.com/dan-strohschein/cartograph/pkg/query"
)

// GraphQuerier is the interface for querying the semantic graph.
// Production uses CLIGraphQuerier (shells out to cartograph).
// Tests can use a mock implementation.
type GraphQuerier interface {
	Query(command string, args []string, aidDir string) (*GraphResult, error)
}

// CLIGraphQuerier shells out to the cartograph binary.
type CLIGraphQuerier struct {
	BinaryPath string // Path to cartograph binary. If empty, looks on PATH.
}

// Query runs a cartograph subcommand and parses the JSON result.
func (c *CLIGraphQuerier) Query(command string, args []string, aidDir string) (*GraphResult, error) {
	binary := c.BinaryPath
	if binary == "" {
		binary = "cartograph"
	}

	cmdArgs := []string{command}
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, "--format", "json")
	if aidDir != "" {
		cmdArgs = append(cmdArgs, "--dir", aidDir)
	}

	cmd := exec.Command(binary, cmdArgs...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			return nil, fmt.Errorf("cartograph %s failed: %s", command, stderr)
		}
		return nil, fmt.Errorf("cartograph %s: %w", command, err)
	}

	var result GraphResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("parsing cartograph output: %w", err)
	}
	return &result, nil
}

// LibraryGraphQuerier uses cartograph as an in-process Go library
// instead of shelling out to the binary. This is faster and enables
// richer queries (effects, errors, search) not available via the
// original CLI-based interface.
type LibraryGraphQuerier struct {
	Engine *query.QueryEngine
	Graph  *graph.Graph
}

// NewLibraryGraphQuerier loads AID files from the given directory and
// builds an in-memory graph with a query engine. Uses gob caching for
// faster subsequent loads.
func NewLibraryGraphQuerier(aidDir string) (*LibraryGraphQuerier, error) {
	g, err := loader.LoadFromDirectoryCached(aidDir)
	if err != nil {
		return nil, fmt.Errorf("loading graph from %s: %w", aidDir, err)
	}
	engine := query.NewQueryEngine(g, 20)
	return &LibraryGraphQuerier{Engine: engine, Graph: g}, nil
}

// Query implements GraphQuerier by dispatching to the appropriate
// query engine method based on the command string.
func (l *LibraryGraphQuerier) Query(command string, args []string, aidDir string) (*GraphResult, error) {
	switch command {
	case "depends":
		if len(args) < 1 {
			return nil, fmt.Errorf("depends requires a type name argument")
		}
		qr, err := l.Engine.TypeDependents(args[0])
		if err != nil {
			return nil, err
		}
		return convertQueryResult(qr), nil

	case "callstack":
		if len(args) < 1 {
			return nil, fmt.Errorf("callstack requires a function name argument")
		}
		dir := query.Both
		for _, a := range args[1:] {
			switch a {
			case "--up":
				dir = query.Reverse
			case "--down":
				dir = query.Forward
			case "--both":
				dir = query.Both
			}
		}
		qr, err := l.Engine.CallStack(args[0], dir)
		if err != nil {
			return nil, err
		}
		return convertQueryResult(qr), nil

	case "field":
		if len(args) < 1 {
			return nil, fmt.Errorf("field requires a Type.Field argument")
		}
		parts := strings.SplitN(args[0], ".", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("field argument must be Type.Field, got: %s", args[0])
		}
		qr, err := l.Engine.FieldTouchers(parts[0], parts[1])
		if err != nil {
			return nil, err
		}
		return convertQueryResult(qr), nil

	case "errors":
		if len(args) < 1 {
			return nil, fmt.Errorf("errors requires an error type argument")
		}
		qr, err := l.Engine.ErrorProducers(args[0])
		if err != nil {
			return nil, err
		}
		return convertQueryResult(qr), nil

	default:
		return nil, fmt.Errorf("unsupported library query command: %s", command)
	}
}

// ErrorQuerier is an optional interface for queriers that support
// transitive error chain resolution via cartograph's ErrorProducers.
type ErrorQuerier interface {
	ErrorProducers(errorType string) (*GraphResult, error)
}

// ErrorProducers implements ErrorQuerier using the in-process query engine.
func (l *LibraryGraphQuerier) ErrorProducers(errorType string) (*GraphResult, error) {
	qr, err := l.Engine.ErrorProducers(errorType)
	if err != nil {
		return nil, err
	}
	return convertQueryResult(qr), nil
}

// convertQueryResult converts a cartograph QueryResult to the
// resolve package's GraphResult type.
func convertQueryResult(qr *query.QueryResult) *GraphResult {
	result := &GraphResult{
		Query:     qr.Query,
		Summary:   qr.Summary,
		NodeCount: qr.NodeCount,
		MaxDepth:  qr.MaxDepth,
	}
	for _, path := range qr.Paths {
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
		result.Paths = append(result.Paths, gp)
	}
	return result
}

// convertNode converts a cartograph graph.Node to a resolve.GraphNode.
func convertNode(n graph.Node) GraphNode {
	return GraphNode{
		Name:          n.Name,
		QualifiedName: n.QualifiedName,
		Kind:          string(n.Kind),
		Module:        n.Module,
		SourceFile:    n.SourceFile,
		SourceLine:    n.SourceLine,
	}
}
