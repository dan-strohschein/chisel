package edit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dan-strohschein/chisel/resolve"
)

// GenerateEdits takes a Resolution and produces all text edits needed.
func GenerateEdits(resolution *resolve.Resolution) (*EditSet, error) {
	var edits []Edit
	var err error

	switch resolution.Intent.Kind {
	case resolve.Rename:
		edits, err = GenerateRenameEdits(resolution, resolution.Intent.NewName)
	case resolve.Move:
		edits, err = GenerateMoveEdits(resolution, resolution.Intent.Destination)
	case resolve.Propagate:
		edits, err = GeneratePropagateEdits(resolution, resolution.Intent.ErrorType)
	default:
		return nil, fmt.Errorf("unknown refactor kind: %d", resolution.Intent.Kind)
	}
	if err != nil {
		return nil, err
	}

	// Generate AID edits
	aidEdits := GenerateAidEdits(resolution, edits)

	// Sort source edits: by file ascending, then line descending (for bottom-to-top application)
	sortEditsDescending(edits)
	sortEditsDescending(aidEdits)

	files := make(map[string]bool)
	for _, e := range edits {
		files[e.File] = true
	}

	return &EditSet{
		Intent:    resolution.Intent,
		Edits:     edits,
		FileCount: len(files),
		EditCount: len(edits),
		AidEdits:  aidEdits,
	}, nil
}

// GenerateRenameEdits generates edits for a rename operation.
func GenerateRenameEdits(resolution *resolve.Resolution, newName string) ([]Edit, error) {
	oldName := resolve.SymbolBaseName(resolution.Intent.Target)
	newBaseName := resolve.SymbolBaseName(newName)
	isMethodRename := strings.Contains(resolution.Intent.Target, ".")
	var typeName string
	var defFile string
	var defLine int
	if isMethodRename {
		// Extract the type name: "WALManager.Close" → "WALManager"
		parts := strings.SplitN(resolution.Intent.Target, ".", 2)
		typeName = parts[0]
		defFile = resolution.Symbol.SourceFile
		defLine = resolution.Symbol.SourceLine
		// Resolve defFile to absolute path for comparison
		if defFile != "" && !filepath.IsAbs(defFile) {
			defFile = resolve.ResolveSourceFile(defFile, resolution.Intent.SourceDir, resolution.Intent.AidDir)
		}
	}
	var edits []Edit

	for _, loc := range resolution.Locations {
		context := loc.Context
		if context == "" {
			line, err := ReadLineFromFile(loc.File, loc.Line)
			if err != nil {
				continue
			}
			context = line
		}

		lang := "go" // Default; could be derived from AID @lang
		if !ScopeMatch(context, oldName, lang, resolution.Intent.IncludeComments) {
			continue
		}

		// For method renames, filter out lines where the method belongs to a
		// different type. Only allow:
		// 1. The target type's method definition (func (x *WALManager) Close)
		// 2. Call sites where the method is called (e.g., obj.Close())
		// Exclude other types' method definitions (func (x *OtherType) Close)
		if isMethodRename {
			if !isMethodRenameCandidate(context, oldName, typeName, loc, defFile, defLine) {
				continue
			}
		}

		edits = append(edits, Edit{
			File:    loc.File,
			Line:    loc.Line,
			OldText: oldName,
			NewText: newBaseName,
			Kind:    classifyEditKind(loc.SymbolKind),
		})
	}

	return edits, nil
}

// isMethodRenameCandidate checks whether a source line is a valid target for
// a method rename. It distinguishes the target type's method definition from
// other types that happen to have the same method name, and filters out calls
// to different types' methods that share the same name.
func isMethodRenameCandidate(line, methodName, typeName string, loc resolve.Location, defFile string, defLine int) bool {
	trimmed := strings.TrimSpace(line)

	// Check if this is a Go method definition: "func (x *Type) Method("
	if strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, ") "+methodName) {
		// It's a method definition — only match if it's on the target type
		if strings.Contains(trimmed, "*"+typeName+")") || strings.Contains(trimmed, " "+typeName+")") {
			return true
		}
		return false // Different type's method definition
	}

	// For lines in the same file as the definition: only allow lines that
	// are clearly call sites on the right type. Lines inside the method body
	// are calls to OTHER objects' methods (not self-calls in Go).
	if loc.File == defFile && loc.Line != defLine {
		// This line is in the definition file but not the definition itself.
		// It's likely a call inside the method body to a different object
		// (e.g., wm.wal.Close() inside WALManager.Close()).
		// Only allow if the line contains a variable name matching the type.
		lowerType := strings.ToLower(typeName)
		lowerLine := strings.ToLower(trimmed)
		// Check for patterns like walManager.Close, wm.Close (common Go receiver names)
		if strings.Contains(lowerLine, lowerType+"."+strings.ToLower(methodName)) {
			return true
		}
		return false
	}

	// For lines in caller files, check if the method call is plausibly on a
	// variable of the target type. This prevents renaming brinIdx.Flush()
	// when we only want hashIndex.Flush().
	//
	// Heuristic: look for the method call pattern ".<method>(" on the line,
	// and check if the variable name before the dot contains the type name
	// (case-insensitive). E.g., for typeName="HashIndexV3":
	//   hashIndex.Flush() → matches (hashIndex contains "hashindex")
	//   idx.Flush() → doesn't match (too ambiguous — skip to be safe)
	//   brinIdx.Flush() → doesn't match (brinidx doesn't contain "hashindex")
	lowerType := strings.ToLower(typeName)
	lowerLine := strings.ToLower(trimmed)

	// Find ".Method(" pattern
	callPattern := "." + strings.ToLower(methodName)
	callIdx := strings.Index(lowerLine, callPattern)
	if callIdx <= 0 {
		return false
	}

	// Extract the variable name before the dot
	prefix := lowerLine[:callIdx]
	// Walk backwards to find the start of the variable name
	varStart := callIdx - 1
	for varStart >= 0 {
		ch := prefix[varStart]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			varStart--
		} else {
			break
		}
	}
	varStart++
	varName := prefix[varStart:callIdx]

	// Check if variable name relates to the type name.
	// For type "HashIndexV3" and variable "hashIndex":
	//   lowerType = "hashindexv3", varName = "hashindex"
	// We check both directions: type contains var OR var contains type prefix
	if strings.Contains(lowerType, varName) || strings.Contains(varName, lowerType) {
		return true
	}

	// Also check without version suffixes: "hashindexv3" → "hashindex"
	// Strip trailing digits from the type name
	trimmedType := strings.TrimRight(lowerType, "0123456789")
	if len(trimmedType) > 2 && (strings.Contains(trimmedType, varName) || strings.Contains(varName, trimmedType)) {
		return true
	}

	return false
}

// GenerateMoveEdits generates edits for a move operation.
func GenerateMoveEdits(resolution *resolve.Resolution, destination string) ([]Edit, error) {
	oldModule := resolution.Symbol.Module
	symbolName := resolve.SymbolBaseName(resolution.Intent.Target)
	var edits []Edit

	for _, loc := range resolution.Locations {
		context := loc.Context
		if context == "" {
			line, err := ReadLineFromFile(loc.File, loc.Line)
			if err != nil {
				continue
			}
			context = line
		}

		switch loc.SymbolKind {
		case "import":
			// Update import path from old module to destination
			edits = append(edits, Edit{
				File:    loc.File,
				Line:    loc.Line,
				OldText: oldModule,
				NewText: destination,
				Kind:    ImportUpdate,
			})
		default:
			// Update qualified references (e.g., oldpkg.Foo → newpkg.Foo)
			oldPkg := lastPathComponent(oldModule)
			newPkg := lastPathComponent(destination)
			oldQualified := oldPkg + "." + symbolName
			newQualified := newPkg + "." + symbolName
			if strings.Contains(context, oldQualified) {
				edits = append(edits, Edit{
					File:    loc.File,
					Line:    loc.Line,
					OldText: oldQualified,
					NewText: newQualified,
					Kind:    TypeReference,
				})
			}
		}
	}

	return edits, nil
}

// GeneratePropagateEdits generates edits to add error propagation.
// Only the target function's signature is modified. Callers get error-handling
// wrappers at call sites but their signatures are left unchanged (they already
// return error if they're in the call chain).
func GeneratePropagateEdits(resolution *resolve.Resolution, errorType string) ([]Edit, error) {
	var edits []Edit

	// Identify the target function's definition file and line
	defFile := resolution.Symbol.SourceFile
	defLine := resolution.Symbol.SourceLine
	if defFile != "" && !filepath.IsAbs(defFile) {
		defFile = resolve.ResolveSourceFile(defFile, resolution.Intent.SourceDir, resolution.Intent.AidDir)
	}

	// First pass: check if the target function's signature needs modification.
	// If it already returns the error type, there's nothing to propagate.
	sigModified := false
	for _, loc := range resolution.Locations {
		if loc.SymbolKind != "definition" || loc.File != defFile || loc.Line != defLine {
			continue
		}
		context := loc.Context
		if context == "" {
			line, err := ReadLineFromFile(loc.File, loc.Line)
			if err != nil {
				continue
			}
			context = line
		}
		edit, err := generateSignatureEdit(loc, context, errorType)
		if err == nil {
			edits = append(edits, edit)
			sigModified = true
			// Also generate edits for return statements in the function body
			returnEdits := generateReturnEdits(loc, errorType)
			edits = append(edits, returnEdits...)
		}
		break
	}

	// If the signature wasn't modified (already returns error), skip call site edits.
	// The callers already handle the error return.
	if !sigModified {
		return edits, nil
	}

	// Second pass: wrap call sites with error handling
	for _, loc := range resolution.Locations {
		if loc.SymbolKind != "call" && loc.SymbolKind != "reference" {
			continue
		}
		context := loc.Context
		if context == "" {
			line, err := ReadLineFromFile(loc.File, loc.Line)
			if err != nil {
				continue
			}
			context = line
		}
		callEdits := generateCallSiteEdits(loc, context, resolution.Intent.Target, errorType)
		edits = append(edits, callEdits...)
	}

	return edits, nil
}

// generateReturnEdits reads the function body starting at the definition line
// and generates edits to append ", nil" to each return statement.
func generateReturnEdits(defLoc resolve.Location, errorType string) []Edit {
	content, err := os.ReadFile(defLoc.File)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	if defLoc.Line < 1 || defLoc.Line > len(lines) {
		return nil
	}

	var edits []Edit

	// Walk from the definition line through the function body
	depth := 0
	started := false
	for i := defLoc.Line - 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Track brace depth to know when the function ends
		for _, ch := range line {
			if ch == '{' {
				depth++
				started = true
			} else if ch == '}' {
				depth--
			}
		}

		// If we've entered the function body and depth returns to 0, we're done
		if started && depth == 0 {
			break
		}

		// Look for return statements inside the function body
		if started && strings.HasPrefix(trimmed, "return ") {
			// Append ", nil" to the return values
			// Handle: return true, x, y → return true, x, y, nil
			edits = append(edits, Edit{
				File:    defLoc.File,
				Line:    i + 1,
				OldText: trimmed,
				NewText: trimmed + ", nil",
				Kind:    CallSiteUpdate,
			})
		}
	}

	return edits
}

// generateSignatureEdit modifies a Go function signature to add an error return.
func generateSignatureEdit(loc resolve.Location, line, errorType string) (Edit, error) {
	// Pattern: func Foo() Result → func Foo() (Result, error)
	// Pattern: func Foo() → func Foo() error
	// Pattern: func Foo() (A, B) → func Foo() (A, B, error)

	trimmed := strings.TrimSpace(line)

	// Find the return type by locating the parameter list's closing paren first.
	// We need to match parens to find the right ')' — the last ')' could be the
	// return tuple's closing paren, not the parameter list's.
	returnStart := findReturnTypeStart(trimmed)
	if returnStart < 0 {
		return Edit{}, fmt.Errorf("cannot parse function signature: %s", trimmed)
	}

	afterParams := trimmed[returnStart:]
	// Remove the opening brace if present
	afterParams = strings.TrimSuffix(strings.TrimSpace(afterParams), "{")
	afterParams = strings.TrimSpace(afterParams)

	// If the return type already includes the error type, skip modification
	if strings.Contains(afterParams, errorType) {
		return Edit{}, fmt.Errorf("function already returns %s: %s", errorType, trimmed)
	}

	var oldSig, newSig string
	if afterParams == "" {
		// No return type: func Foo() → func Foo() error
		oldSig = trimmed
		insertion := " " + errorType
		braceIdx := strings.LastIndex(trimmed, "{")
		if braceIdx >= 0 {
			newSig = trimmed[:braceIdx] + insertion + " " + trimmed[braceIdx:]
		} else {
			newSig = trimmed + insertion
		}
	} else if strings.HasPrefix(afterParams, "(") {
		// Tuple return: func Foo() (A, B) → func Foo() (A, B, error)
		closeIdx := strings.LastIndex(afterParams, ")")
		if closeIdx >= 0 {
			inside := afterParams[1:closeIdx]
			oldReturn := afterParams[:closeIdx+1]
			newReturn := "(" + inside + ", " + errorType + ")"
			oldSig = trimmed
			newSig = strings.Replace(trimmed, oldReturn, newReturn, 1)
		}
	} else {
		// Single return: func Foo() Result → func Foo() (Result, error)
		returnType := afterParams
		braceIdx := strings.LastIndex(trimmed, "{")
		if braceIdx >= 0 {
			oldSig = trimmed
			newSig = strings.Replace(trimmed, returnType, "("+returnType+", "+errorType+")", 1)
		} else {
			oldSig = trimmed
			newSig = strings.Replace(trimmed, returnType, "("+returnType+", "+errorType+")", 1)
		}
	}

	if oldSig == "" || newSig == "" || oldSig == newSig {
		return Edit{}, fmt.Errorf("could not generate signature edit for: %s", line)
	}

	return Edit{
		File:    loc.File,
		Line:    loc.Line,
		OldText: oldSig,
		NewText: newSig,
		Kind:    SignatureChange,
	}, nil
}

// findReturnTypeStart finds the index in a Go function signature where the
// return type begins. It walks past "func", the receiver (if any), the name,
// and the parameter list by matching parentheses, then returns the index of
// the first character after the parameter list's closing ')'.
// Returns -1 if the signature cannot be parsed.
func findReturnTypeStart(sig string) int {
	// Find "func " prefix
	funcIdx := strings.Index(sig, "func ")
	if funcIdx < 0 {
		return -1
	}

	// Walk past parens. We need to find the parameter list, which is the
	// second set of parens in a method (first is receiver) or first in a function.
	// Strategy: find each '(' and match it with its ')'.
	parenSets := 0
	i := funcIdx + 5 // skip "func "
	for i < len(sig) {
		if sig[i] == '(' {
			// Found opening paren — find matching close
			depth := 1
			i++
			for i < len(sig) && depth > 0 {
				if sig[i] == '(' {
					depth++
				} else if sig[i] == ')' {
					depth--
				}
				i++
			}
			parenSets++
			// After a method receiver (first paren set), skip the method name
			// After the parameter list (second for methods, first for functions),
			// we're at the return type.
			if parenSets == 1 {
				// Could be receiver or params — check if next non-space is a name
				rest := strings.TrimSpace(sig[i:])
				if len(rest) > 0 && rest[0] != '(' && rest[0] != '{' && rest[0] != ')' {
					// This was the receiver — skip the name and look for params
					// Skip to next '('
					continue
				}
				// This was the parameter list (function, not method)
				return i
			}
			if parenSets == 2 {
				// Second paren set — this was definitely the parameter list
				return i
			}
		} else {
			i++
		}
	}
	return -1
}

// generateCallSiteEdits wraps a call site with error handling.
func generateCallSiteEdits(loc resolve.Location, line, funcName, errorType string) []Edit {
	trimmed := strings.TrimSpace(line)
	baseName := resolve.SymbolBaseName(funcName)

	// Pattern: result := Foo() → result, err := Foo()\nif err != nil { return ..., err }
	// Pattern: Foo() → if err := Foo(); err != nil { return ..., err }

	indent := strings.TrimRight(line, strings.TrimLeft(line, " \t"))

	if strings.Contains(trimmed, ":=") {
		// Assignment form: result := Foo()
		parts := strings.SplitN(trimmed, ":=", 2)
		lhs := strings.TrimSpace(parts[0])
		newLHS := lhs + ", err"
		oldLine := trimmed
		newLine := newLHS + " := " + strings.TrimSpace(parts[1])
		errCheck := indent + "if err != nil {\n" + indent + "\treturn err\n" + indent + "}"

		return []Edit{
			{
				File:    loc.File,
				Line:    loc.Line,
				OldText: oldLine,
				NewText: newLine + "\n" + errCheck,
				Kind:    CallSiteUpdate,
			},
		}
	}

	if strings.Contains(trimmed, "= ") && strings.Contains(trimmed, baseName) {
		// Assignment form: result = Foo()
		parts := strings.SplitN(trimmed, "=", 2)
		lhs := strings.TrimSpace(parts[0])
		newLHS := lhs + ", err"
		oldLine := trimmed
		newLine := newLHS + " = " + strings.TrimSpace(parts[1])
		errCheck := indent + "if err != nil {\n" + indent + "\treturn err\n" + indent + "}"

		return []Edit{
			{
				File:    loc.File,
				Line:    loc.Line,
				OldText: oldLine,
				NewText: newLine + "\n" + errCheck,
				Kind:    CallSiteUpdate,
			},
		}
	}

	// Bare call: Foo()
	if strings.Contains(trimmed, baseName+"(") {
		oldLine := trimmed
		newLine := "if err := " + trimmed + " err != nil {\n" + indent + "\treturn err\n" + indent + "}"
		return []Edit{
			{
				File:    loc.File,
				Line:    loc.Line,
				OldText: oldLine,
				NewText: newLine,
				Kind:    CallSiteUpdate,
			},
		}
	}

	return nil
}

// GenerateAidEdits generates edits to update AID files.
func GenerateAidEdits(resolution *resolve.Resolution, sourceEdits []Edit) []Edit {
	var edits []Edit

	if resolution.Intent.Kind == resolve.Rename {
		oldTarget := resolution.Intent.Target                  // e.g., "WALManager.Close"
		newTarget := resolution.Intent.NewName                 // e.g., "WALManager.Shutdown"
		oldBaseName := resolve.SymbolBaseName(oldTarget)       // e.g., "Close"
		newBaseName := resolve.SymbolBaseName(newTarget)       // e.g., "Shutdown"

		// Find AID files that reference this symbol
		for _, module := range resolution.AffectedModules {
			aidFile := findAidFile(resolution.Intent.AidDir, module)
			if aidFile == "" {
				continue
			}

			content, err := os.ReadFile(aidFile)
			if err != nil {
				continue
			}

			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)

				// Update @fn/@type name declarations
				// AID uses qualified names: "@fn WALManager.Close"
				// Use exact match (not prefix) to avoid matching
				// "@fn BundleService.GetDocumentPageReadOnly" when renaming
				// "BundleService.GetDocumentPage"
				if trimmed == "@fn "+oldTarget || trimmed == "@type "+oldTarget {
					edits = append(edits, Edit{
						File:    aidFile,
						Line:    i + 1,
						OldText: oldTarget,
						NewText: newTarget,
						Kind:    AidUpdate,
					})
				} else if trimmed == "@fn "+oldBaseName || trimmed == "@type "+oldBaseName {
					// Unqualified form: "@fn Close"
					edits = append(edits, Edit{
						File:    aidFile,
						Line:    i + 1,
						OldText: oldBaseName,
						NewText: newBaseName,
						Kind:    AidUpdate,
					})
				}

				// Update @calls/@related/@sig references
				if strings.HasPrefix(trimmed, "@sig") ||
					strings.HasPrefix(trimmed, "@related") ||
					strings.HasPrefix(trimmed, "@calls") {
					// Only replace the full qualified name to avoid changing
					// other types' methods (e.g., WriteAheadLog.Close when
					// renaming WALManager.Close)
					if strings.Contains(line, oldTarget) {
						edits = append(edits, Edit{
							File:    aidFile,
							Line:    i + 1,
							OldText: oldTarget,
							NewText: newTarget,
							Kind:    AidUpdate,
						})
					} else if !strings.Contains(oldTarget, ".") && strings.Contains(line, oldBaseName) {
						// Only use basename fallback for non-method renames
						// (methods must match the full qualified name)
						edits = append(edits, Edit{
							File:    aidFile,
							Line:    i + 1,
							OldText: oldBaseName,
							NewText: newBaseName,
							Kind:    AidUpdate,
						})
					}
				}
			}
		}
	}

	return edits
}

// ScopeMatch checks if a symbol occurrence on a line is in a context that should be renamed.
// If includeComments is true, occurrences in comments are also matched (stale comments = bugs).
// Occurrences inside string literals are always excluded.
func ScopeMatch(line, symbol, lang string, includeComments bool) bool {
	idx := strings.Index(line, symbol)
	if idx < 0 {
		return false
	}

	// Word boundary check: the character after the symbol must not be
	// alphanumeric or underscore. This prevents "GetDocumentPage" from
	// matching inside "GetDocumentPageReadOnly".
	afterIdx := idx + len(symbol)
	if afterIdx < len(line) {
		ch := line[afterIdx]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			return false
		}
	}

	// Check if inside a single-line comment
	if !includeComments {
		commentMarkers := []string{"//"}
		if lang == "python" || lang == "ruby" || lang == "bash" {
			commentMarkers = []string{"#"}
		}
		for _, marker := range commentMarkers {
			ci := strings.Index(line, marker)
			if ci >= 0 && ci < idx {
				return false
			}
		}
	}

	// Check if inside a string literal (simple heuristic)
	prefix := line[:idx]
	doubleQuotes := strings.Count(prefix, `"`) - strings.Count(prefix, `\"`)
	if doubleQuotes%2 != 0 {
		return false
	}
	singleQuotes := strings.Count(prefix, "'") - strings.Count(prefix, `\'`)
	if singleQuotes%2 != 0 {
		return false
	}
	backticks := strings.Count(prefix, "`")
	if backticks%2 != 0 {
		return false
	}

	return true
}

// ReadLineFromFile reads a specific line from a source file.
func ReadLineFromFile(file string, lineNum int) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(content), "\n")
	if lineNum < 1 || lineNum > len(lines) {
		return "", fmt.Errorf("line %d out of range (file has %d lines)", lineNum, len(lines))
	}
	return lines[lineNum-1], nil
}

func sortEditsDescending(edits []Edit) {
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].File != edits[j].File {
			return edits[i].File < edits[j].File
		}
		return edits[i].Line > edits[j].Line // Descending within file
	})
}

func classifyEditKind(symbolKind string) EditKind {
	switch symbolKind {
	case "import":
		return ImportUpdate
	case "definition":
		return SymbolRename
	case "call":
		return CallSiteUpdate
	case "type_ref":
		return TypeReference
	default:
		return SymbolRename
	}
}

func lastPathComponent(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func findAidFile(aidDir, module string) string {
	// AID files are named after the last path component of the module
	base := lastPathComponent(module)
	candidates := []string{
		aidDir + "/" + base + ".aid",
		aidDir + "/" + strings.ReplaceAll(module, "/", "_") + ".aid",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
