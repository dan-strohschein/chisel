package resolve

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
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
