package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dan-strohschein/cartograph/pkg/graph"
	"github.com/dan-strohschein/cartograph/pkg/query"
)

// MockGraphQuerier returns canned responses for testing.
type MockGraphQuerier struct {
	Results map[string]*GraphResult
	Err     error
}

func (m *MockGraphQuerier) Query(command string, args []string, aidDir string) (*GraphResult, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	key := command
	if len(args) > 0 {
		key += ":" + args[0]
	}
	if r, ok := m.Results[key]; ok {
		return r, nil
	}
	return nil, &NotFoundError{Symbol: key}
}

func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

func TestResolveRename(t *testing.T) {
	mock := &MockGraphQuerier{
		Results: map[string]*GraphResult{
			"depends:QueryEngine": {
				Query:     "TypeDependents(QueryEngine)",
				Summary:   "Found 2 dependents",
				NodeCount: 3,
				MaxDepth:  1,
				Paths: []GraphPath{
					{
						Nodes: []GraphNode{
							{Name: "QueryEngine", QualifiedName: "testpkg.QueryEngine", Kind: "Type", Module: "testpkg"},
							{Name: "ProcessRequest", QualifiedName: "testpkg.ProcessRequest", Kind: "Method", Module: "testpkg"},
							{Name: "NewQueryEngine", QualifiedName: "testpkg.NewQueryEngine", Kind: "Function", Module: "testpkg"},
						},
						Depth: 1,
					},
				},
			},
		},
	}

	resolver := &Resolver{Graph: mock}
	td := testdataDir()

	intent := Intent{
		Kind:      Rename,
		Target:    "QueryEngine",
		NewName:   "GraphQueryEngine",
		AidDir:    filepath.Join(td, "aidocs"),
		SourceDir: filepath.Join(td, "src"),
	}

	res, err := resolver.Resolve(intent)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if res.Symbol.Name != "QueryEngine" {
		t.Errorf("expected symbol name QueryEngine, got %s", res.Symbol.Name)
	}

	if len(res.Locations) == 0 {
		t.Error("expected at least one location")
	}

	// Verify locations are sorted
	for i := 1; i < len(res.Locations); i++ {
		prev := res.Locations[i-1]
		curr := res.Locations[i]
		if prev.File > curr.File || (prev.File == curr.File && prev.Line > curr.Line) {
			t.Errorf("locations not sorted: %s:%d > %s:%d", prev.File, prev.Line, curr.File, curr.Line)
		}
	}

	// Verify no duplicates
	seen := make(map[string]bool)
	for _, loc := range res.Locations {
		key := loc.File + ":" + string(rune(loc.Line))
		if seen[key] {
			t.Errorf("duplicate location: %s:%d", loc.File, loc.Line)
		}
		seen[key] = true
	}
}

func TestResolveNotFound(t *testing.T) {
	mock := &MockGraphQuerier{
		Results: map[string]*GraphResult{},
	}

	resolver := &Resolver{Graph: mock}
	td := testdataDir()

	intent := Intent{
		Kind:      Rename,
		Target:    "NonexistentSymbol",
		NewName:   "Whatever",
		AidDir:    filepath.Join(td, "aidocs"),
		SourceDir: filepath.Join(td, "src"),
	}

	_, err := resolver.Resolve(intent)
	if err == nil {
		t.Fatal("expected error for nonexistent symbol")
	}
	if _, ok := err.(*NotFoundError); !ok {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestParseGrepOutput(t *testing.T) {
	output := `/path/to/file.go:10:func QueryEngine() {
/path/to/file.go:25:	engine := QueryEngine{}
/path/to/other.go:5:import "QueryEngine"`

	locs := parseGrepOutput(output)
	if len(locs) != 3 {
		t.Fatalf("expected 3 locations, got %d", len(locs))
	}
	if locs[0].Line != 10 {
		t.Errorf("expected line 10, got %d", locs[0].Line)
	}
	if locs[1].Line != 25 {
		t.Errorf("expected line 25, got %d", locs[1].Line)
	}
	if locs[2].File != "/path/to/other.go" {
		t.Errorf("expected /path/to/other.go, got %s", locs[2].File)
	}
}

func TestGraphResultJSON(t *testing.T) {
	jsonStr := `{
		"query": "TypeDependents(Foo)",
		"summary": "Found 1",
		"node_count": 1,
		"max_depth": 0,
		"paths": [{
			"nodes": [{"name": "Foo", "qualified_name": "pkg.Foo", "kind": "Type", "module": "pkg"}],
			"edges": [],
			"depth": 0
		}]
	}`

	var result GraphResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if result.NodeCount != 1 {
		t.Errorf("expected node_count 1, got %d", result.NodeCount)
	}
	if result.Paths[0].Nodes[0].Name != "Foo" {
		t.Errorf("expected node name Foo, got %s", result.Paths[0].Nodes[0].Name)
	}
}

func TestSymbolBaseName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"QueryEngine", "QueryEngine"},
		{"HttpClient.Get", "Get"},
		{"User.email", "email"},
		{"a.b.c", "c"},
	}
	for _, tt := range tests {
		got := SymbolBaseName(tt.input)
		if got != tt.want {
			t.Errorf("SymbolBaseName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsExported(t *testing.T) {
	tests := []struct {
		name string
		lang string
		want bool
	}{
		// Go: uppercase = exported, lowercase = unexported
		{"QueryEngine", "go", true},
		{"queryEngine", "go", false},
		{"A", "go", true},
		{"a", "go", false},
		// Empty name is never exported
		{"", "go", false},

		// Python: underscore prefix = private
		{"get_data", "python", true},
		{"_private", "python", false},
		{"__dunder__", "python", false},
		{"PublicClass", "python", true},

		// Rust: underscore prefix = private (at name level)
		{"process", "rust", true},
		{"_internal", "rust", false},
		{"Config", "rust", true},

		// Java: keyword-based visibility, name-level always returns true
		{"getData", "java", true},
		{"privateHelper", "java", true},

		// Default (unrecognized) falls back to Go convention
		{"Exported", "unknown", true},
		{"unexported", "unknown", false},
		{"Exported", "", true},
		{"unexported", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.lang+"/"+tt.name, func(t *testing.T) {
			got := IsExported(tt.name, tt.lang)
			if got != tt.want {
				t.Errorf("IsExported(%q, %q) = %v, want %v", tt.name, tt.lang, got, tt.want)
			}
		})
	}
}

func TestParseErrorMapEntry(t *testing.T) {
	tests := []struct {
		line       string
		wantFunc   string
		wantStrat  string
		wantWrap   string
		wantConv   string
	}{
		{
			line:      `BundleService.GetBundle: wrap "fetching bundle: %w"`,
			wantFunc:  "BundleService.GetBundle",
			wantStrat: "wrap",
			wantWrap:  "fetching bundle: %w",
		},
		{
			line:      "CacheService.Get: log",
			wantFunc:  "CacheService.Get",
			wantStrat: "log",
		},
		{
			line:      "Validator.Validate: return",
			wantFunc:  "Validator.Validate",
			wantStrat: "return",
		},
		{
			line:      "Parser.Parse: convert ValidationError",
			wantFunc:  "Parser.Parse",
			wantStrat: "convert",
			wantConv:  "ValidationError",
		},
		{
			// Unknown strategy defaults to return
			line:      "Foo.Bar: something_else",
			wantFunc:  "Foo.Bar",
			wantStrat: "return",
		},
		{
			// Empty strategy defaults to return
			line:      "Foo.Bar:",
			wantFunc:  "Foo.Bar",
			wantStrat: "return",
		},
		{
			// No colon — invalid line
			line:     "no colon here",
			wantFunc: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			funcName, handling := parseErrorMapEntry(tt.line)
			if funcName != tt.wantFunc {
				t.Errorf("funcName = %q, want %q", funcName, tt.wantFunc)
			}
			if funcName == "" {
				return
			}
			if handling.Strategy != tt.wantStrat {
				t.Errorf("strategy = %q, want %q", handling.Strategy, tt.wantStrat)
			}
			if handling.WrapMsg != tt.wantWrap {
				t.Errorf("wrapMsg = %q, want %q", handling.WrapMsg, tt.wantWrap)
			}
			if handling.ConvertTo != tt.wantConv {
				t.Errorf("convertTo = %q, want %q", handling.ConvertTo, tt.wantConv)
			}
		})
	}
}

func TestReadErrorMap(t *testing.T) {
	tmpDir := t.TempDir()

	aidContent := `@module testpkg
@lang go

---

@error_map errors
@entries
  BundleService.GetBundle: wrap "fetching bundle: %w"
  CacheService.Get: log
  Validator.Validate: return
  Parser.Parse: convert ValidationError
`
	if err := os.WriteFile(filepath.Join(tmpDir, "testpkg.aid"), []byte(aidContent), 0644); err != nil {
		t.Fatalf("writing temp aid file: %v", err)
	}

	result := readErrorMap(tmpDir)
	if result == nil {
		t.Fatal("readErrorMap returned nil, expected entries")
	}

	// Check wrap strategy
	if h, ok := result["BundleService.GetBundle"]; !ok {
		t.Error("missing entry for BundleService.GetBundle")
	} else {
		if h.Strategy != "wrap" {
			t.Errorf("BundleService.GetBundle strategy = %q, want %q", h.Strategy, "wrap")
		}
		if h.WrapMsg != "fetching bundle: %w" {
			t.Errorf("BundleService.GetBundle wrapMsg = %q, want %q", h.WrapMsg, "fetching bundle: %w")
		}
	}

	// Check log strategy
	if h, ok := result["CacheService.Get"]; !ok {
		t.Error("missing entry for CacheService.Get")
	} else if h.Strategy != "log" {
		t.Errorf("CacheService.Get strategy = %q, want %q", h.Strategy, "log")
	}

	// Check return strategy
	if h, ok := result["Validator.Validate"]; !ok {
		t.Error("missing entry for Validator.Validate")
	} else if h.Strategy != "return" {
		t.Errorf("Validator.Validate strategy = %q, want %q", h.Strategy, "return")
	}

	// Check convert strategy
	if h, ok := result["Parser.Parse"]; !ok {
		t.Error("missing entry for Parser.Parse")
	} else {
		if h.Strategy != "convert" {
			t.Errorf("Parser.Parse strategy = %q, want %q", h.Strategy, "convert")
		}
		if h.ConvertTo != "ValidationError" {
			t.Errorf("Parser.Parse convertTo = %q, want %q", h.ConvertTo, "ValidationError")
		}
	}

	// Verify count
	if len(result) != 4 {
		t.Errorf("expected 4 entries, got %d", len(result))
	}

	// Empty directory returns nil
	emptyDir := t.TempDir()
	if got := readErrorMap(emptyDir); got != nil {
		t.Errorf("readErrorMap on empty dir = %v, want nil", got)
	}

	// Empty string returns nil
	if got := readErrorMap(""); got != nil {
		t.Errorf("readErrorMap(\"\") = %v, want nil", got)
	}
}

func TestCheckLockSafety(t *testing.T) {
	g := graph.NewGraph()

	// Add a Lock node
	lockID := graph.MakeNodeID("pkg", graph.KindLock, "mu")
	g.AddNode(graph.Node{
		ID:            lockID,
		Kind:          graph.KindLock,
		Name:          "mu",
		QualifiedName: "pkg.mu",
		Module:        "pkg",
		Metadata:      map[string]string{},
	})

	// Add a Function node that acquires the lock
	funcID := graph.MakeNodeID("pkg", graph.KindFunction, "ProcessRequest")
	g.AddNode(graph.Node{
		ID:            funcID,
		Kind:          graph.KindFunction,
		Name:          "ProcessRequest",
		QualifiedName: "pkg.ProcessRequest",
		Module:        "pkg",
		Metadata:      map[string]string{},
	})

	// Add an Acquires edge: ProcessRequest -> mu
	g.AddEdge(graph.Edge{
		Source: funcID,
		Target: lockID,
		Kind:   graph.EdgeAcquires,
	})

	engine := query.NewQueryEngine(g, 10)
	querier := &LibraryGraphQuerier{Engine: engine, Graph: g}

	// Function that acquires the lock should produce a warning
	warnings := CheckLockSafety(querier, "ProcessRequest")
	if len(warnings) == 0 {
		t.Fatal("expected warnings for function that acquires a lock, got none")
	}
	found := false
	for _, w := range warnings {
		if contains(w, "ProcessRequest") && contains(w, "mu") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning mentioning ProcessRequest and mu, got: %v", warnings)
	}

	// Function that does NOT acquire the lock should produce no warnings
	warnings = CheckLockSafety(querier, "SomeOtherFunc")
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for unrelated function, got: %v", warnings)
	}

	// Graph with no locks should return nil
	emptyG := graph.NewGraph()
	emptyG.AddNode(graph.Node{
		ID:            graph.MakeNodeID("pkg", graph.KindFunction, "Foo"),
		Kind:          graph.KindFunction,
		Name:          "Foo",
		QualifiedName: "pkg.Foo",
		Module:        "pkg",
		Metadata:      map[string]string{},
	})
	emptyEngine := query.NewQueryEngine(emptyG, 10)
	emptyQuerier := &LibraryGraphQuerier{Engine: emptyEngine, Graph: emptyG}
	if warnings := CheckLockSafety(emptyQuerier, "Foo"); warnings != nil {
		t.Errorf("expected nil warnings for graph with no locks, got: %v", warnings)
	}
}

func TestImpact(t *testing.T) {
	g := graph.NewGraph()

	// Add a Type node
	typeID := graph.MakeNodeID("pkg", graph.KindType, "QueryEngine")
	g.AddNode(graph.Node{
		ID:            typeID,
		Kind:          graph.KindType,
		Name:          "QueryEngine",
		QualifiedName: "pkg.QueryEngine",
		Module:        "pkg",
		SourceFile:    "pkg/engine.go",
		SourceLine:    10,
		Metadata:      map[string]string{},
	})

	// Add a Function node that depends on the type
	funcID := graph.MakeNodeID("pkg", graph.KindFunction, "ProcessRequest")
	g.AddNode(graph.Node{
		ID:            funcID,
		Kind:          graph.KindFunction,
		Name:          "ProcessRequest",
		QualifiedName: "pkg.ProcessRequest",
		Module:        "pkg",
		SourceFile:    "pkg/handler.go",
		SourceLine:    25,
		Metadata:      map[string]string{},
	})

	// Add DependsOn edge: ProcessRequest -> QueryEngine
	g.AddEdge(graph.Edge{
		Source: funcID,
		Target: typeID,
		Kind:   graph.EdgeDependsOn,
	})

	engine := query.NewQueryEngine(g, 10)
	querier := &LibraryGraphQuerier{Engine: engine, Graph: g}

	report, err := Impact(querier, "QueryEngine")
	if err != nil {
		t.Fatalf("Impact failed: %v", err)
	}

	if report.Symbol != "QueryEngine" {
		t.Errorf("report.Symbol = %q, want %q", report.Symbol, "QueryEngine")
	}
	if report.Kind != "Type" {
		t.Errorf("report.Kind = %q, want %q", report.Kind, "Type")
	}

	// Should have at least one dependent
	if len(report.Dependents) == 0 {
		t.Error("expected at least one dependent, got none")
	}

	// Check that dependents include the expected nodes
	foundFunc := false
	for _, dep := range report.Dependents {
		if dep.Name == "ProcessRequest" {
			foundFunc = true
		}
	}
	if !foundFunc {
		t.Errorf("expected ProcessRequest in dependents, got: %v", report.Dependents)
	}

	// Check affected files
	if len(report.AffectedFiles) == 0 {
		t.Error("expected at least one affected file")
	}

	// Summary should be non-empty
	if report.Summary == "" {
		t.Error("expected non-empty summary")
	}

	// Non-existent symbol should return error
	_, err = Impact(querier, "NonexistentSymbol")
	if err == nil {
		t.Error("expected error for non-existent symbol")
	}
}
