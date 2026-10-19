package stdlib

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// PathValues registers the functions under path that read a path value (jaala#280), the text
// graph.paths, Paths and EdgePaths bind (ns.Path): path.length, path.node, path.step, path.edge,
// path.nodes and path.edges. Each reads its path from the text alone, so it works on a path from any
// of them, carried through rules or bound by a host. Positions count from 0. A value that isn't a path,
// absent included, gives no row, and neither do a path's edges when it names none: which values reach
// a function depends on the plan (a caller's constant reaches it through demand), so an error there
// would make one evaluator fail where another answers.
func PathValues(v *ns.Vocabulary) error {
	for _, p := range []struct {
		path string
		b    ns.Builtin
	}{
		{"path.length", pathFn("path.length", []string{"path", "steps"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeNumber}},
			"binds how many steps the path takes",
			func(p ns.Path, _ ns.Arg, emit func(...ns.Value) error) error { return emit(ns.N(float64(p.Steps()))) })},
		{"path.node", pathFn("path.node", []string{"path", "index", "node"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeNumber}, {}},
			"binds each node of the path, index from 0",
			func(p ns.Path, at ns.Arg, emit func(...ns.Value) error) error {
				return each(len(p.Nodes), at, func(i int) error { return emit(ns.N(float64(i)), p.Nodes[i]) })
			})},
		{"path.step", pathFn("path.step", []string{"path", "index", "from", "to"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeNumber}, {}, {}},
			"binds each step of the path, index from 0",
			func(p ns.Path, at ns.Arg, emit func(...ns.Value) error) error {
				return each(p.Steps(), at, func(i int) error { return emit(ns.N(float64(i)), p.Nodes[i], p.Nodes[i+1]) })
			})},
		{"path.edge", pathFn("path.edge", []string{"path", "index", "edge"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeNumber}, {}},
			"binds each edge of a path over a relation of three arguments, index from 0",
			func(p ns.Path, at ns.Arg, emit func(...ns.Value) error) error {
				return each(len(p.Edges), at, func(i int) error { return emit(ns.N(float64(i)), p.Edges[i]) })
			})},
		{"path.nodes", pathFn("path.nodes", []string{"path", "nodes"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeList}},
			"binds the path's nodes as a list",
			func(p ns.Path, _ ns.Arg, emit func(...ns.Value) error) error {
				return emit(ns.S(ns.ListText(p.Nodes)))
			})},
		{"path.edges", pathFn("path.edges", []string{"path", "edges"}, []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeList}},
			"binds the path's edges as a list",
			func(p ns.Path, _ ns.Arg, emit func(...ns.Value) error) error {
				if !p.HasEdges {
					return nil
				}
				return emit(ns.S(ns.ListText(p.Edges)))
			})},
	} {
		if err := v.AddPredicate(p.path, p.b); err != nil {
			return err
		}
	}
	return nil
}

// pathFn is a function of a path (its first argument) into the rest, run forward only: read is handed
// the path and the second argument, an index the function may read, and emits the arguments after the
// path.
func pathFn(name string, labels []string, types []ns.ArgType, doc string, read func(ns.Path, ns.Arg, func(...ns.Value) error) error) ns.Builtin {
	mode := make([]bool, len(labels))
	mode[0] = true
	return ns.Builtin{
		Arity:  len(labels),
		Modes:  [][]bool{mode},
		Labels: labels,
		Types:  types,
		Doc:    doc,
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if !args[0].Bound {
				return needsBound(name, "its path")
			}
			if args[0].Value.Absent || args[0].Value.Num != nil {
				return nil
			}
			p, err := ns.ParsePath(args[0].Value.S)
			if err != nil {
				return nil
			}
			var at ns.Arg
			if len(args) > 1 {
				at = args[1]
			}
			return read(p, at, func(vals ...ns.Value) error {
				return emit(append([]ns.Value{args[0].Value}, vals...), nil)
			})
		},
	}
}

// each calls f with every index below n, or with the one index at binds when it is a whole number in
// range.
func each(n int, at ns.Arg, f func(int) error) error {
	if at.Bound {
		v := at.Value
		if v.Num == nil || *v.Num != math.Trunc(*v.Num) || *v.Num < 0 || *v.Num >= float64(n) {
			return nil
		}
		return f(int(*v.Num))
	}
	for i := 0; i < n; i++ {
		if err := f(i); err != nil {
			return err
		}
	}
	return nil
}

// An EdgeStep is a Step whose edges have identities: it calls each once per edge with the node it leads
// to, the edge (a part, a call site, a fact's id), and the edge's citations.
type EdgeStep func(ctx context.Context, node ns.Value, out bool, extra []ns.Value, each func(next, edge ns.Value, cites []string) error) error

// Paths builds a path predicate, paths(?from, ?to, extra..., mode, within, ?path), over a host's step
// (jaala#280): ?path is each path from ?from to ?to of 1 to within steps (within 0 for no bound) that
// mode allows, as a list of its nodes (ns.Path), which path.* reads. The modes are graph.paths's:
//
//   - walk: anything may repeat, so within must bound it;
//   - trail: no step from one node to another twice;
//   - acyclic: no node twice;
//   - simple: no node twice, but a path may end where it started, closing a cycle;
//   - shortest: one path of the fewest steps per pair, the one whose nodes come first, step by step.
//
// It runs from whichever end a body binds, as Hops does, and each answer cites its steps in order. It
// asks for each node's steps once per call and stops when emit does, so a limit stops the walk. The
// extra arguments must be bound; extra gives their types. Arguments are labelled from, to, argN, mode,
// within and path.
func Paths(step Step, extra ...ns.ArgType) ns.Builtin {
	return paths(func(ctx context.Context, node ns.Value, out bool, ext []ns.Value, each func(ns.Value, ns.Value, []string) error) error {
		return step(ctx, node, out, ext, func(next ns.Value, cites []string) error { return each(next, ns.Value{}, cites) })
	}, false, extra)
}

// EdgePaths is Paths over a step whose edges have identities: ?path names its edges too
// ({"nodes": [...], "edges": [...]}), a trail never takes one edge twice, and two parallel edges make
// two paths. agni's parts between two nets are such edges.
func EdgePaths(step EdgeStep, extra ...ns.ArgType) ns.Builtin {
	return paths(step, true, extra)
}

func paths(step EdgeStep, edges bool, extra []ns.ArgType) ns.Builtin {
	n := 5 + len(extra)
	mode, within := n-3, n-2
	out, back := make([]bool, n), make([]bool, n)
	out[0], back[1] = true, true
	labels := []string{"from", "to"}
	for i := 2; i < mode; i++ {
		out[i], back[i] = true, true
		labels = append(labels, "arg"+strconv.Itoa(i))
	}
	out[mode], back[mode], out[within], back[within] = true, true, true, true
	labels = append(labels, "mode", "within", "path")
	types := append([]ns.ArgType{{}, {}}, extra...)
	types = append(types, ns.ArgType{Type: ns.TypeString}, ns.ArgType{Type: ns.TypeNumber}, ns.ArgType{Type: ns.TypeString})
	return ns.Builtin{
		Arity:  n,
		Modes:  [][]bool{out, back},
		Labels: labels,
		Types:  types,
		Doc:    "binds each path from ?from to ?to within ?within steps (0 for no bound) that the mode (walk, trail, acyclic, simple or shortest) allows",
		Gen: func(ctx context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			m, limit, err := pathsArgs(args[mode], args[within])
			if err != nil {
				return err
			}
			ext := make([]ns.Value, 0, mode-2)
			for _, a := range args[2:mode] {
				ext = append(ext, a.Value)
			}
			w := &hostWalk{ctx: ctx, step: step, edges: edges, extra: ext, limit: limit}
			found := func(from ns.Value, steps []hostStep) error {
				p := w.path(from, steps)
				row := append([]ns.Value{p.Nodes[0], p.Nodes[len(p.Nodes)-1]}, ext...)
				row = append(row, args[mode].Value, args[within].Value, ns.S(p.Text()))
				var cites []string
				for _, s := range steps {
					cites = append(cites, s.cites...)
				}
				return emit(row, cites)
			}
			from, to := args[0], args[1]
			var target *ns.Value
			if to.Bound {
				target = &to.Value
			}
			if from.Bound {
				w.out = true
				if m == "shortest" {
					return w.shortest(from.Value, target, func(s []hostStep) error { return found(from.Value, s) })
				}
				return w.enumerate(from.Value, m, target, func(s []hostStep) error { return found(from.Value, s) })
			}
			if m != "shortest" {
				return w.enumerate(to.Value, m, nil, func(s []hostStep) error {
					fwd := slices.Clone(s)
					slices.Reverse(fwd)
					return found(fwd[0].from, fwd)
				})
			}
			// the shortest path from each start reaching ?to is chosen walking out from it, as a call with
			// ?from bound chooses it
			starts := []ns.Value{}
			err = w.shortest(to.Value, nil, func(s []hostStep) error {
				starts = append(starts, s[len(s)-1].from)
				return nil
			})
			if err != nil {
				return err
			}
			fwd := &hostWalk{ctx: ctx, step: step, edges: edges, extra: ext, limit: limit, out: true}
			for _, start := range starts {
				err := fwd.shortest(start, target, func(s []hostStep) error { return found(start, s) })
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// pathsArgs reads a Paths call's mode and within.
func pathsArgs(mode, within ns.Arg) (string, int, error) {
	if !mode.Bound || !within.Bound {
		return "", 0, errors.New("query: a paths call needs its mode and within bound")
	}
	m := mode.Value.S
	if mode.Value.Num != nil || !slices.Contains([]string{"walk", "trail", "acyclic", "simple", "shortest"}, m) {
		return "", 0, fmt.Errorf("query: a paths call's mode must be one of walk, trail, acyclic, simple, shortest, not %s", m)
	}
	v := within.Value
	if v.Absent || v.Num == nil || *v.Num < 0 || *v.Num != math.Trunc(*v.Num) {
		return "", 0, errors.New("query: a paths call's within must be a whole number of steps, 0 for no bound")
	}
	limit := int(min(*v.Num, math.MaxInt32))
	if m == "walk" && limit == 0 {
		return "", 0, errors.New("query: a paths call in mode walk needs a within: around a cycle its paths never end")
	}
	return m, limit, nil
}

// A hostStep is one step a host's step took, in from-to order.
type hostStep struct {
	from, edge, to ns.Value
	cites          []string
}

// A hostWalk walks a host's step out or back within limit steps (0: any), asking for each node's steps
// once.
type hostWalk struct {
	ctx   context.Context
	step  EdgeStep
	edges bool
	extra []ns.Value
	limit int
	out   bool
	asked map[string][]hostStep
}

// next is where s leads the walk.
func (w *hostWalk) next(s hostStep) ns.Value {
	if w.out {
		return s.to
	}
	return s.from
}

// key is a step's identity for a trail and for dropping a step the host gave twice: its ends, and its
// edge when edges have identities.
func (w *hostWalk) key(s hostStep) string {
	k := nodeKey(s.from) + "\x00" + nodeKey(s.to)
	if w.edges {
		k += "\x00" + nodeKey(s.edge)
	}
	return k
}

// trailKey is what a trail may take once: the edge when edges have identities, either way, and
// otherwise the step from one node to the other.
func (w *hostWalk) trailKey(s hostStep) string {
	if w.edges {
		return nodeKey(s.edge)
	}
	return w.key(s)
}

// stepsFrom is node's steps, once each, ordered by where they lead (then by edge), so the paths and the
// shortest don't depend on the order the host gave them in.
func (w *hostWalk) stepsFrom(node ns.Value) ([]hostStep, error) {
	k := nodeKey(node)
	if s, ok := w.asked[k]; ok {
		return s, nil
	}
	var out []hostStep
	err := w.step(w.ctx, node, w.out, w.extra, func(next, edge ns.Value, cites []string) error {
		s := hostStep{from: node, edge: edge, to: next, cites: cites}
		if !w.out {
			s.from, s.to = next, node
		}
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b hostStep) int {
		if c := strings.Compare(nodeKey(w.next(a)), nodeKey(w.next(b))); c != 0 {
			return c
		}
		return strings.Compare(w.key(a), w.key(b))
	})
	out = slices.CompactFunc(out, func(a, b hostStep) bool { return w.key(a) == w.key(b) })
	if w.asked == nil {
		w.asked = map[string][]hostStep{}
	}
	w.asked[k] = out
	return out, nil
}

// path is the path from from through steps, which are in from-to order.
func (w *hostWalk) path(from ns.Value, steps []hostStep) ns.Path {
	p := ns.Path{Nodes: []ns.Value{from}, HasEdges: w.edges}
	if w.edges {
		p.Edges = []ns.Value{}
	}
	for _, s := range steps {
		p.Nodes = append(p.Nodes, s.to)
		if w.edges {
			p.Edges = append(p.Edges, s.edge)
		}
	}
	return p
}

// enumerate visits each path of 1 to limit steps from start that mode allows, ending at target when it
// is set, depth first, with its steps in walk order.
func (w *hostWalk) enumerate(start ns.Value, mode string, target *ns.Value, visit func([]hostStep) error) error {
	var path []hostStep
	onPath := map[string]int{nodeKey(start): 1}
	used := map[string]bool{}
	var walk func(node ns.Value, depth int) error
	walk = func(node ns.Value, depth int) error {
		at := target == nil || sameValue(node, *target)
		if depth > 0 && at {
			if err := visit(path); err != nil {
				return err
			}
		}
		closed := mode == "simple" && depth > 0 && sameValue(node, start)
		if (w.limit != 0 && depth == w.limit) || closed || (mode == "acyclic" || mode == "simple") && depth > 0 && at && target != nil {
			return nil
		}
		steps, err := w.stepsFrom(node)
		if err != nil {
			return err
		}
		for _, s := range steps {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			n := w.next(s)
			nk, sk := nodeKey(n), w.trailKey(s)
			if mode == "acyclic" && onPath[nk] > 0 || mode == "trail" && used[sk] || mode == "simple" && onPath[nk] > 0 && !sameValue(n, start) {
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

// shortest visits, for each node start reaches in 1 to limit steps (only target when it is set), the
// path of the fewest steps whose nodes come first, breadth first, with its steps in walk order. start
// itself is reached only through a cycle.
func (w *hostWalk) shortest(start ns.Value, target *ns.Value, visit func([]hostStep) error) error {
	type reached struct {
		parent int
		step   hostStep
		node   ns.Value
	}
	all := []reached{{parent: -1, node: start}}
	seen := map[string]bool{}
	frontier := []int{0}
	for depth := 1; len(frontier) > 0 && (w.limit == 0 || depth <= w.limit); depth++ {
		var next []int
		for _, i := range frontier {
			steps, err := w.stepsFrom(all[i].node)
			if err != nil {
				return err
			}
			for _, s := range steps {
				n := w.next(s)
				k := nodeKey(n)
				if seen[k] {
					continue
				}
				seen[k] = true
				all = append(all, reached{parent: i, step: s, node: n})
				j := len(all) - 1
				if !sameValue(n, start) {
					next = append(next, j)
				}
				if target != nil && !sameValue(n, *target) {
					continue
				}
				var path []hostStep
				for c := j; all[c].parent >= 0; c = all[c].parent {
					path = append(path, all[c].step)
				}
				slices.Reverse(path)
				if err := visit(path); err != nil {
					return err
				}
				if target != nil {
					return nil
				}
			}
		}
		frontier = next
	}
	return nil
}
