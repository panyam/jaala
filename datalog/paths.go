package datalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/panyam/jaala/ns"
)

// graph.paths("calls", ?x, ?y, "trail", 1, 6, ?p) binds ?p to each path of 1 to 6 steps from ?x to ?y
// over the relation calls, base or derived, as a value (#280): an ns.Path's text, which stdlib's path.*
// functions read. Over a relation of two arguments a path is its nodes, ["a","b","c"]; over one of
// three, from, edge and to, it names its edges too. The mode says how a path may repeat:
//
//   - walk: anything may repeat, so max must bound it;
//   - trail: no step twice (no fact of the relation used twice, or with edges no edge, either way), Cypher's;
//   - acyclic: no node twice;
//   - simple: no node twice, but a path may end where it started, closing a cycle (GQL's SIMPLE);
//   - shortest: one path of the fewest steps per pair, the one whose nodes come first, step by step.
//
// Max 0 is no bound, and min 0 gives each start the relation holds its path of no steps. Each answer cites its path's facts
// in order, and its witness has a child per step, as a closure's does. The walk runs depth first from
// whichever end is bound, reading each node's steps once per call, and counts a unit of work per step it
// tries, so the budget stops a walk whose paths multiply.

// pathsName is how a query calls the paths walk.
const pathsName = "graph.paths"

// pathsPrefix starts a paths call's relation name.
const pathsPrefix = "\x00q:"

const pathsFix = `write graph.paths("relation", ?from, ?to, "trail", min, max, ?path): mode walk, trail, acyclic, simple or shortest, max 0 for no bound (walk needs one)`

// pathsBuiltin is what a paths call is to the checks and the planner, as closureBuiltin is a closure's.
var pathsBuiltin = ns.Builtin{
	Arity: 7,
	Modes: [][]bool{
		{true, true, false, true, true, true, false},
		{true, false, true, true, true, true, false},
		{true, false, false, true, true, true, false},
	},
	Labels: []string{"relation", "from", "to", "mode", "min", "max", "path"},
	Types:  []ns.ArgType{{Type: ns.TypeString}, {}, {}, {Type: ns.TypeString}, {Type: ns.TypeNumber}, {Type: ns.TypeNumber}, {Type: ns.TypeString}},
	Doc:    "?path is each path from ?from to ?to in min to max steps of the relation (max 0 for no bound), mode walk, trail, acyclic, simple or shortest",
	Gen:    engineRun,
}

var pathsWalk = &engineWalk{name: pathsName, prefix: pathsPrefix, builtin: pathsBuiltin, fix: pathsFix, lo: 4, arities: []int{2, 3}}

// pathModes are the ways a path may repeat.
var pathModes = []string{"walk", "trail", "acyclic", "simple", "shortest"}

// pathMode reads a paths call's mode.
func pathMode(v ns.Value) (string, error) {
	if v.Num == nil && !v.Absent && slices.Contains(pathModes, v.S) {
		return v.S, nil
	}
	return "", checkErr(KindType, fmt.Sprintf("query: %s's mode must be one of %s, not %s", pathsName, strings.Join(pathModes, ", "), v.S)).call(pathsName, pathsBuiltin.Arity).argument(3, v.S).fix(pathsFix)
}

// errWalkUnbounded refuses a walk with no max, whose paths around a cycle never end.
func errWalkUnbounded() error {
	return checkErr(KindType, fmt.Sprintf("query: %s in mode walk needs a max: around a cycle its paths never end", pathsName)).call(pathsName, pathsBuiltin.Arity).argument(5, "0").fix("give a max, or walk as a trail or acyclic")
}

// extendPaths yields each binding of a paths call under bnd: walking out from a bound ?from, back from
// a bound ?to, and from every start when neither is.
func (b *Base) extendPaths(atom *Atom, bnd *binding, yield func(*binding) error) error {
	if b.run != nil && b.run.explain != nil {
		b.run.explain.access("paths")
	}
	if b.across != nil {
		return checkErr(KindDefinition, fmt.Sprintf("query: %s isn't evaluated across versions: ask each version with At", pathsName)).call(pathsName, len(atom.Args))
	}
	step, _ := stepOf(atom.Relation)
	var vals [7]ns.Value
	var bound [7]bool
	for i, t := range atom.Args {
		vals[i], bound[i] = resolve(t, bnd)
	}
	if !bound[3] || !bound[4] || !bound[5] {
		return checkErr(KindUnbound, fmt.Sprintf("query: %s needs its mode, min and max bound (a variable must appear in a relation before %s reads it)", pathsName, pathsName)).call(pathsName, len(atom.Args))
	}
	// A constant mode or bound was refused when the program was checked, so a bad one came from data,
	// which reaches a call or not by the plan, and walks nothing rather than failing one evaluator.
	mode, err := pathMode(vals[3])
	if err != nil {
		return nil
	}
	lo, hi, err := pathsWalk.bounds(&vals[4], &vals[5])
	if err != nil || mode == "walk" && hi == 0 {
		return nil
	}
	arity := b.stepArity(step)
	if !bound[1] && !bound[2] {
		starts, err := b.closureStarts(step, lo == 0)
		if err != nil {
			return err
		}
		for _, s := range starts {
			m := bnd.mark()
			bnd.set(closureFrom, s)
			from := *atom
			from.Args = slices.Clone(atom.Args)
			from.Args[1] = Term{Var: closureFrom}
			err := b.extendPaths(&from, bnd, func(bnd *binding) error {
				if !unify(atom.Args[1:2], ns.Tuple{Vals: []ns.Value{s}}, bnd) {
					return nil
				}
				return yield(bnd)
			})
			bnd.reset(m)
			if err != nil {
				return err
			}
		}
		return nil
	}
	m := bnd.mark()
	emit := func(start ns.Value, steps []closureStep) error {
		if err := b.countWork(); err != nil {
			return err
		}
		p := pathOf(start, arity, steps)
		row := vals
		row[1], row[2] = p.Nodes[0], p.Nodes[len(p.Nodes)-1]
		row[6] = ns.S(p.Text())
		t := ns.Tuple{Vals: row[:]}
		for _, s := range steps {
			t.Cites = append(t.Cites, s.cites...)
		}
		if !unify(atom.Args, t, bnd) {
			bnd.reset(m)
			return nil
		}
		if b.witnessing() {
			bnd.last = closureWitness(pathsName, row[:], steps)
		}
		err := yield(bnd)
		bnd.reset(m)
		return err
	}
	if bound[1] {
		var target *ns.Value
		if bound[2] {
			target = &vals[2]
		}
		w := pathWalker{b: b, step: step, fwd: true, lo: lo, hi: hi}
		if mode == "shortest" {
			return w.shortest(vals[1], target, func(steps []closureStep) error { return emit(vals[1], steps) })
		}
		return w.enumerate(vals[1], mode, target, func(steps []closureStep) error { return emit(vals[1], steps) })
	}
	back := pathWalker{b: b, step: step, fwd: false, lo: lo, hi: hi}
	if mode != "shortest" {
		// back from ?to: each path found in reverse, written from its start
		return back.enumerate(vals[2], mode, nil, func(steps []closureStep) error {
			fwd := slices.Clone(steps)
			slices.Reverse(fwd)
			start := vals[2]
			if len(fwd) > 0 {
				start = fwd[0].vals[0]
			}
			return emit(start, fwd)
		})
	}
	// The shortest path from each start that reaches ?to is chosen walking out from it, so it is the same
	// path a call with ?from bound gives, whichever end the plan binds.
	out := pathWalker{b: b, step: step, fwd: true, lo: lo, hi: hi}
	return b.closure(step, vals[2], false, lo, hi, nil, func(start ns.Value, _ int, _ func() ([]closureStep, error)) error {
		return out.shortest(start, &vals[2], func(steps []closureStep) error { return emit(start, steps) })
	})
}

// pathOf is the path a walk from start took through steps, in from-to order: its nodes, and its edges
// when the step relation names them.
func pathOf(start ns.Value, arity int, steps []closureStep) ns.Path {
	p := ns.Path{Nodes: []ns.Value{start}, HasEdges: arity == 3}
	if p.HasEdges {
		p.Edges = []ns.Value{}
	}
	for _, s := range steps {
		p.Nodes = append(p.Nodes, s.vals[len(s.vals)-1])
		if p.HasEdges {
			p.Edges = append(p.Edges, s.vals[1])
		}
	}
	return p
}

// A pathWalker walks one step relation out (fwd) or back, within lo to hi steps, reading each node's
// steps once.
type pathWalker struct {
	b      *Base
	step   string
	fwd    bool
	lo, hi int
	steps  map[string][]closureStep
}

// next is where a step leads: its to walking out, its from walking back.
func (w *pathWalker) next(s closureStep) ns.Value {
	if w.fwd {
		return s.vals[len(s.vals)-1]
	}
	return s.vals[0]
}

// stepKey is a step's fact without its citations: its from, edge and to.
func stepKey(s closureStep) string {
	keys := make([]string, len(s.vals))
	for i, v := range s.vals {
		keys[i] = valueKey(v)
	}
	return strings.Join(keys, "\x00")
}

// trailKey is what a trail may take once: a fact's edge when the relation names its edges, so an edge
// walked either way (an undirected relationship's two facts share one) counts once, and otherwise the
// fact.
func trailKey(s closureStep) string {
	if len(s.vals) == 3 {
		return valueKey(s.vals[1])
	}
	return stepKey(s)
}

// stepsFrom is every step from node, once per fact (a fact two members serve is walked once, with the
// citations that come first), ordered by where it leads and then by its fact, so the paths a walk finds,
// and the shortest it chooses, don't depend on the order the facts came in.
func (w *pathWalker) stepsFrom(node ns.Value) ([]closureStep, error) {
	k := valueKey(node)
	if s, ok := w.steps[k]; ok {
		return s, nil
	}
	var out []closureStep
	err := w.b.closureNeighbours(w.step, node, w.fwd, func(_ ns.Value, s closureStep) error {
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, c closureStep) int {
		if d := strings.Compare(valueKey(w.next(a)), valueKey(w.next(c))); d != 0 {
			return d
		}
		if d := strings.Compare(stepKey(a), stepKey(c)); d != 0 {
			return d
		}
		return slices.Compare(a.cites, c.cites)
	})
	out = slices.CompactFunc(out, func(a, c closureStep) bool { return stepKey(a) == stepKey(c) })
	if w.steps == nil {
		w.steps = map[string][]closureStep{}
	}
	w.steps[k] = out
	return out, nil
}

// enumerate visits every path from start of lo to hi steps that the mode allows, ending at target when
// it is set, depth first. The steps it hands visit are in walk order and reused after visit returns.
func (w *pathWalker) enumerate(start ns.Value, mode string, target *ns.Value, visit func([]closureStep) error) error {
	var path []closureStep
	onPath := map[string]int{valueKey(start): 1}
	used := map[string]bool{}
	zero := false // a path of no steps, only from a node the relation holds (onStep)
	if w.lo == 0 {
		var err error
		if zero, err = w.b.onStep(w.step, start); err != nil {
			return err
		}
	}
	var walk func(node ns.Value, depth int) error
	walk = func(node ns.Value, depth int) error {
		at := target == nil || valueEq(node, *target)
		if depth >= w.lo && (depth > 0 || zero) && at {
			if err := visit(path); err != nil {
				return err
			}
		}
		closed := mode == "simple" && depth > 0 && valueEq(node, start)
		if (w.hi != 0 && depth == w.hi) || closed || (mode == "acyclic" || mode == "simple") && depth > 0 && at && target != nil {
			return nil // a closed cycle goes no further, and no path through its target ends at it again
		}
		steps, err := w.stepsFrom(node)
		if err != nil {
			return err
		}
		for _, s := range steps {
			if err := w.b.countWork(); err != nil {
				return err
			}
			n := w.next(s)
			nk, sk := valueKey(n), trailKey(s)
			switch {
			case mode == "acyclic" && onPath[nk] > 0, mode == "trail" && used[sk]:
				continue
			case mode == "simple" && onPath[nk] > 0 && !valueEq(n, start):
				continue
			}
			path = append(path, s)
			onPath[nk]++
			used[sk] = true
			err := walk(n, depth+1)
			path = path[:len(path)-1]
			onPath[nk]--
			used[sk] = false
			if err != nil {
				return err
			}
		}
		return nil
	}
	return walk(start, 0)
}

// shortest visits, for each node start reaches in lo to hi steps (only target when it is set), one path
// of the fewest steps: breadth first, each node's steps in stepsFrom's order, so a node's path is the
// first to reach it. Below lo a node is its own state at each depth, as in closure.
func (w *pathWalker) shortest(start ns.Value, target *ns.Value, visit func([]closureStep) error) error {
	type reached struct {
		parent int
		step   closureStep
	}
	all := []reached{{parent: -1}}
	seen := map[closureState]bool{{valueKey(start), 0}: true}
	arrive := func(i int, node ns.Value) error {
		if target != nil && !valueEq(node, *target) {
			return nil
		}
		var steps []closureStep
		for ; all[i].parent >= 0; i = all[i].parent {
			steps = append(steps, all[i].step)
		}
		slices.Reverse(steps)
		if err := visit(steps); err != nil {
			return err
		}
		if target != nil {
			return errClosureFound
		}
		return nil
	}
	err := func() error {
		if w.lo == 0 {
			on, err := w.b.onStep(w.step, start)
			if err != nil {
				return err
			}
			if on {
				if err := arrive(0, start); err != nil {
					return err
				}
			}
		}
		frontier, nodes := []int{0}, []ns.Value{start}
		for depth := 1; len(frontier) > 0 && (w.hi == 0 || depth <= w.hi); depth++ {
			var next []int
			var nextNodes []ns.Value
			for f, i := range frontier {
				steps, err := w.stepsFrom(nodes[f])
				if err != nil {
					return err
				}
				for _, s := range steps {
					if err := w.b.countWork(); err != nil {
						return err
					}
					n := w.next(s)
					st := closureState{valueKey(n), min(depth, w.lo)}
					if seen[st] {
						continue
					}
					seen[st] = true
					all = append(all, reached{parent: i, step: s})
					next, nextNodes = append(next, len(all)-1), append(nextNodes, n)
					if st.depth == w.lo {
						if err := arrive(len(all)-1, n); err != nil {
							return err
						}
					}
				}
			}
			frontier, nodes = next, nextNodes
		}
		return nil
	}()
	if err == errClosureFound {
		return nil
	}
	return err
}
