package resolve

// RefactorKind identifies the type of refactoring operation.
type RefactorKind int

const (
	Rename    RefactorKind = iota // Rename a symbol across the codebase
	Move                          // Move a symbol to a different package
	Propagate                     // Add error return and propagate through callers
)

// Intent is a parsed refactoring request.
type Intent struct {
	Kind            RefactorKind
	Target          string // The symbol to refactor (e.g., "QueryEngine", "HttpClient.Get")
	NewName         string // For rename: the new name
	Destination     string // For move: the target package
	ErrorType       string // For propagate: the error type to add
	AidDir          string // Path to .aidocs/ directory
	SourceDir       string // Path to source tree
	IncludeComments bool   // If true, also rename occurrences in comments
}

// Location is a specific occurrence of a symbol in a source file.
type Location struct {
	File      string // Absolute path to the source file
	Line      int    // Line number (1-based)
	Column    int    // Column number (1-based, 0 if unknown)
	EndColumn int    // End column (0 if unknown)
	SymbolKind string // "definition", "call", "type_ref", "field_ref", "import"
	Context   string // The full line of source code containing this occurrence
}

// Resolution is the result of resolving an intent.
type Resolution struct {
	Intent          Intent
	Symbol          GraphNode
	Locations       []Location
	AffectedFiles   []string
	AffectedModules []string
	Warnings        []string
	FastPath        bool // True when resolved via grep (rare name), no type disambiguation needed
}

// GraphNode is a code entity from cartograph's JSON output.
type GraphNode struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Module        string `json:"module"`
	SourceFile    string `json:"source_file,omitempty"`
	SourceLine    int    `json:"source_line,omitempty"`
}

// GraphEdge is an edge from cartograph's JSON output.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
}

// GraphPath is a single traversal path from cartograph output.
type GraphPath struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	Depth int         `json:"depth"`
}

// GraphResult is the parsed JSON output from a cartograph CLI query.
type GraphResult struct {
	Query     string      `json:"query"`
	Summary   string      `json:"summary"`
	NodeCount int         `json:"node_count"`
	MaxDepth  int         `json:"max_depth"`
	Paths     []GraphPath `json:"paths"`
}

// EffectEntry is a single entry in a cartograph effects report.
type EffectEntry struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	SourceFile    string `json:"source_file,omitempty"`
	SourceLine    int    `json:"source_line,omitempty"`
}

// EffectResult is the parsed JSON output from cartograph effects command.
type EffectResult struct {
	Function     string                    `json:"function"`
	TotalCallees int                       `json:"total_callees"`
	MaxDepth     int                       `json:"max_depth"`
	Effects      map[string][]EffectEntry  `json:"effects"`
}
