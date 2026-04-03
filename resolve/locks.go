package resolve

import (
	"fmt"

	"github.com/dan-strohschein/cartograph/pkg/graph"
)

// CheckLockSafety inspects the graph for lock-related concerns when
// refactoring a symbol. Returns warnings if the target is involved
// in lock acquisition or protects lock-guarded data.
func CheckLockSafety(querier *LibraryGraphQuerier, target string) []string {
	var warnings []string
	g := querier.Graph

	// Find all Lock nodes in the graph
	locks := g.NodesByKind(graph.KindLock)
	if len(locks) == 0 {
		return nil
	}

	// Check if the refactoring target is a function that acquires any lock
	for _, lock := range locks {
		// Check EdgeAcquires pointing to this lock
		for _, e := range g.InEdges(lock.ID) {
			if e.Kind != graph.EdgeAcquires {
				continue
			}
			acquirer, err := g.NodeByID(e.Source)
			if err != nil {
				continue
			}
			if acquirer.Name == target || acquirer.QualifiedName == target {
				warnings = append(warnings,
					fmt.Sprintf("function %q acquires lock %q — ensure lock semantics are preserved after refactoring",
						acquirer.QualifiedName, lock.Name))
			}
		}

		// Check if the target is a field protected by this lock
		// (lock nodes have @protects listing the fields they guard)
		if meta, ok := lock.Metadata["protects"]; ok {
			if containsSymbol(meta, target) {
				warnings = append(warnings,
					fmt.Sprintf("symbol %q is protected by lock %q — renaming/moving may require updating lock documentation",
						target, lock.Name))
			}
		}

		// Check EdgeOrderedBefore for lock ordering constraints
		for _, e := range g.OutEdges(lock.ID) {
			if e.Kind != graph.EdgeOrderedBefore {
				continue
			}
			otherLock, err := g.NodeByID(e.Target)
			if err != nil {
				continue
			}
			// Check if target acquires either lock in the ordering chain
			for _, ie := range g.InEdges(lock.ID) {
				if ie.Kind != graph.EdgeAcquires {
					continue
				}
				acq, err := g.NodeByID(ie.Source)
				if err != nil {
					continue
				}
				if acq.Name == target || acq.QualifiedName == target {
					warnings = append(warnings,
						fmt.Sprintf("function %q acquires lock %q which has ordering constraint (must be acquired before %q) — moving this function may break lock ordering",
							target, lock.Name, otherLock.Name))
				}
			}
		}
	}

	return warnings
}

// containsSymbol checks if a comma-separated metadata string contains the given symbol.
func containsSymbol(meta, symbol string) bool {
	// Simple substring check — metadata is typically comma-separated field names
	return len(meta) > 0 && len(symbol) > 0 && contains(meta, symbol)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
