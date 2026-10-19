package stdlib

import (
	"context"
	"errors"
	"math"
	"strconv"

	"github.com/panyam/jaala/ns"
)

// A Step lists one node's neighbours: out along edges (out true) or back against them, calling each
// once per edge with the node it leads to and the edge's citations. The host decides what an edge is,
// as for a Walk, and extra holds the step's other arguments. A Step should stop and return ctx.Err()
// when ctx ends, and return each's error as is.
type Step func(ctx context.Context, node ns.Value, out bool, extra []ns.Value, each func(next ns.Value, cites []string) error) error

// Hops builds a bounded reachability predicate, reach(?from, ?to, extra..., ?within, ?hops), from a
// host's step (#281). Where Reach hands the whole walk to the host, Hops walks breadth first itself,
// so it can promise what Reach can't:
//
//   - ?within is an input, the most steps a path may take, and the walk stops there: no step is asked
//     past it, so a radius of 2 costs two levels whatever lies beyond. It must be a whole number of at
//     least 1.
//   - ?hops is the fewest steps from ?from to ?to, and each answer cites one path of that length, its
//     steps in order from ?from to ?to (a backward walk's reversed to read that way).
//   - It runs from whichever end a body binds, as Reach does, and with both bound stops at ?to.
//   - ?from reaches itself only through a cycle, at the cycle's length, as a recursive rule's closure
//     does.
//
// The extra arguments must be bound; extra gives their types. Arguments are labelled from, to, argN,
// within and hops.
func Hops(step Step, extra ...ns.ArgType) ns.Builtin {
	n := 4 + len(extra)
	within := n - 2
	out, back := make([]bool, n), make([]bool, n)
	out[0], back[1] = true, true
	labels := []string{"from", "to"}
	for i := 2; i < within; i++ {
		out[i], back[i] = true, true
		labels = append(labels, "arg"+strconv.Itoa(i))
	}
	out[within], back[within] = true, true
	labels = append(labels, "within", "hops")
	types := append([]ns.ArgType{{}, {}}, extra...)
	types = append(types, ns.ArgType{Type: ns.TypeNumber}, ns.ArgType{Type: ns.TypeNumber})
	return ns.Builtin{
		Arity:  n,
		Modes:  [][]bool{out, back},
		Labels: labels,
		Types:  types,
		Doc:    "reports what ?from reaches, or what reaches ?to, within ?within steps, ?hops the fewest, with that path as its citations",
		Gen: func(ctx context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			limit, err := withinOf(args[within])
			if err != nil {
				return err
			}
			ext := make([]ns.Value, 0, within-2)
			for _, a := range args[2:within] {
				ext = append(ext, a.Value)
			}
			from, to := args[0], args[1]
			fwd := from.Bound
			start, target := from.Value, to
			if !fwd {
				start, target = to.Value, ns.Arg{}
			}
			err = bfs(ctx, step, start, fwd, ext, limit, func(node ns.Value, depth int, path []string) error {
				if target.Bound && !sameValue(node, target.Value) {
					return nil
				}
				f, t := start, node
				if !fwd {
					f, t = node, start
				}
				row := append([]ns.Value{f, t}, ext...)
				row = append(row, args[within].Value, ns.N(float64(depth)))
				if err := emit(row, path); err != nil {
					return err
				}
				if target.Bound {
					return errFound
				}
				return nil
			})
			if errors.Is(err, errFound) {
				return nil
			}
			return err
		},
	}
}

// withinOf is a Hops call's bound: a whole number of at least 1.
func withinOf(a ns.Arg) (int, error) {
	v := a.Value
	if !a.Bound || v.Absent || v.Num == nil {
		return 0, errors.New("query: a reach's within needs a number bound (the most steps a path may take)")
	}
	f := *v.Num
	if f < 1 || f != math.Trunc(f) {
		return 0, errors.New("query: a reach's within must be a whole number of steps, at least 1")
	}
	if f > math.MaxInt32 {
		return math.MaxInt32, nil
	}
	return int(f), nil
}

// bfs visits each node start reaches in 1 to limit steps, once, at the fewest steps it takes, with
// the citations of one such path in order from start. Levels are walked in turn, so every node of
// level d is visited before any of level d+1. start itself isn't visited until a cycle returns to it.
func bfs(ctx context.Context, step Step, start ns.Value, out bool, extra []ns.Value, limit int, visit func(ns.Value, int, []string) error) error {
	type reached struct {
		node ns.Value
		path []string
	}
	seen := map[string]bool{}
	frontier := []reached{{node: start}}
	for depth := 1; depth <= limit && len(frontier) > 0; depth++ {
		var next []reached
		for _, r := range frontier {
			err := step(ctx, r.node, out, extra, func(n ns.Value, cites []string) error {
				k := nodeKey(n)
				if seen[k] {
					return nil
				}
				seen[k] = true
				if sameValue(n, start) {
					// back at the start through a cycle: its neighbours were the first level
					return visit(n, depth, append(append([]string(nil), r.path...), cites...))
				}
				var path []string
				if out {
					path = append(append([]string(nil), r.path...), cites...)
				} else {
					path = append(append([]string(nil), cites...), r.path...)
				}
				next = append(next, reached{node: n, path: path})
				return visit(n, depth, path)
			})
			if err != nil {
				return err
			}
		}
		frontier = next
	}
	return nil
}

// nodeKey keys a node as sameValue compares it: absent apart from any text, a number by its value.
func nodeKey(v ns.Value) string {
	switch {
	case v.Absent:
		return "a"
	case v.Num != nil:
		if *v.Num == 0 {
			return "n0" // -0 is 0
		}
		return "n" + strconv.FormatFloat(*v.Num, 'g', -1, 64)
	}
	return "s" + v.S
}
