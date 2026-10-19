// Package corpus generates the random programs and graphs jaala's evaluators are compared on (#87):
// a graph of nodes, weighted edges and sometimes units, and a program over it, both from one seed.
// datalog's tests compare every evaluator and Source over them, and a store module (stores/bolt)
// compares its Source against the seed's MemSource, so the corpus is one generator rather than a
// copy per module.
//
// A seed's program depends on the order the generator draws in, so a replay (JAALA_GEN_SEED) needs
// the same draws: never range over a map while drawing.
package corpus

import "slices"

// The engine's names the generator writes, as datalog spells them; datalog's tests check they
// still agree.
const (
	ClosureName = "graph.closure"
	PathsName   = "graph.paths"
)

// PathModes are graph.paths' modes, as datalog lists them.
var PathModes = []string{"walk", "trail", "acyclic", "simple", "shortest"}

// Bind is the host's values for the goal's set-bound variable, by name without its ?, or nil.
func (p Program) Bind() map[string][]string { return p.goal.bind }

// Shapes are the shapes the generator aimed this program at ("recursion", "negation", ...), which
// the corpus's control counts.
func (p Program) Shapes() []string { return p.shapes }

// HasUnits is whether any weight carries a unit (#192).
func (f Facts) HasUnits() bool { return f.units != nil }

// WithoutUnits is f with every weight's unit left off.
func (f Facts) WithoutUnits() Facts {
	f.units = nil
	return f
}

// InUnit is f with every weight in unit u.
func (f Facts) InUnit(u string) Facts {
	f.units = slices.Repeat([]string{u}, len(f.units))
	return f
}

// Rules is how many rules the program has.
func (p Program) Rules() int { return len(p.rules) }

// GoalLiterals is how many literals the goal's body has.
func (p Program) GoalLiterals() int { return len(p.goal.body) }

// Negates is whether a rule's body negates a literal.
func (p Program) Negates() bool {
	for _, r := range p.rules {
		for _, l := range r.body {
			if l.neg {
				return true
			}
		}
	}
	return false
}

// Nodes is how many nodes the graph has.
func (f Facts) Nodes() int { return f.n }

// Edges is how many edges the graph has.
func (f Facts) Edges() int { return len(f.edges) }
