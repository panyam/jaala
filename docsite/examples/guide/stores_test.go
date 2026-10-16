package guide_test

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// looker is source()'s facts behind a store that answers lookups, printing
// each call, as a store over a database makes a round trip per call. The
// engine checks every fact it gets back, so a store may return more than
// matches, but never fewer.
type looker struct {
	*ns.MemSource
	name string
}

func (s looker) Lookup(_ context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	fmt.Printf("%s: lookup %s %s\n", s.name, rel, s.text(rel, bound))
	return s.matching(rel, bound), nil
}

func (s looker) matching(rel string, bound map[int]ns.Value) []ns.Tuple {
	var out []ns.Tuple
	for _, t := range s.Tuples(rel) {
		ok := true
		for i, v := range bound {
			ok = ok && t.Vals[i].S == v.S
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// text writes bound values by their labels, from=api.
func (s looker) text(rel string, bound map[int]ns.Value) string {
	sch, _ := s.Schema(rel)
	var parts []string
	for i, l := range sch.Labels {
		if v, ok := bound[i]; ok {
			parts = append(parts, l+"="+v.S)
		}
	}
	return strings.Join(parts, " ")
}

func storeBase(src ns.Source) *datalog.Base {
	v, err := vocabulary(src)
	if err != nil {
		panic(err)
	}
	return datalog.MustBase(v, src)
}

func answer(b *datalog.Base, query, col string) {
	rows, err := datalog.SemiNaive{}.Eval(context.Background(), datalog.MustParse(query), b)
	if err != nil {
		fmt.Println(err)
	}
	var vals []string
	for _, r := range rows {
		vals = append(vals, r.Bind[datalog.Var(col)].S)
	}
	fmt.Println(strings.Join(vals, " "))
}

// A store that looks facts up is asked only for what the query binds. The
// demand rewrite binds each package depends_on reaches, so the store is
// asked about app, then api, then log, then util, and never read whole.
func Example_storeLookup() {
	answer(storeBase(looker{source(), "store"}), `deps.depends_on("app", ?d) => ?d`, "d")
	// Output:
	// store: lookup imports from=app
	// store: lookup imports from=api
	// store: lookup imports from=log
	// store: lookup imports from=util
	// api log util
}

// A federation is one source over several stores. Each member's Holds says
// which calls it can match, so a lookup goes only to the members that might
// answer: app's and api's imports live in one repo, log's in another, and
// nobody is asked about util, which no member holds.
func Example_storeFederation() {
	all := source()
	split := func(froms ...string) *ns.MemSource {
		m := ns.NewMemSource().Declare("imports", "from", "to").Declare("pkg", "name", "license")
		for _, t := range all.Tuples("imports") {
			if slices.Contains(froms, t.Vals[0].S) {
				m.Add("imports", t)
			}
		}
		return m
	}
	holds := func(froms ...string) func(string, map[int]ns.Value) bool {
		return func(rel string, bound map[int]ns.Value) bool {
			v, ok := bound[0]
			return rel != "imports" || !ok || slices.Contains(froms, v.S)
		}
	}
	f, err := ns.NewFederation(
		ns.Member{Name: "web", Source: looker{split("app", "api"), "web"}, Holds: holds("app", "api")},
		ns.Member{Name: "lib", Source: looker{split("log"), "lib"}, Holds: holds("log")},
	)
	if err != nil {
		panic(err)
	}
	answer(storeBase(f), `deps.depends_on("app", ?d) => ?d`, "d")
	// Output:
	// web: lookup imports from=app
	// web: lookup imports from=api
	// lib: lookup imports from=log
	// api log util
}

// joiner is a looker that also evaluates a join itself (ns.PushSource), as
// SQLite would run a conjunction as one statement. This one takes a join of
// plain atoms and nothing else: Pushes is asked while planning, and a
// conjunction it refuses is asked literal by literal instead.
type joiner struct{ looker }

func (s joiner) Pushes(c ns.Conjunction) bool {
	return len(c.Compare) == 0 && len(c.Filter) == 0 && !c.Partial && c.Page == nil && len(c.Inputs) == 0
}

func (s joiner) Push(_ context.Context, c ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	var atoms []string
	for _, a := range c.Atoms {
		var args []string
		for _, arg := range a.Args {
			args = append(args, fmt.Sprintf("?%d", arg.Var))
		}
		atoms = append(atoms, a.Relation+"("+strings.Join(args, ", ")+")")
	}
	fmt.Printf("%s: push %s\n", s.name, strings.Join(atoms, ", "))
	return func(yield func([]ns.Tuple, error) bool) {
		s.join(c, 0, map[int]string{}, nil, yield)
	}
}

// join finds every combination of facts satisfying c's atoms from atom i on,
// given the variables bound so far.
func (s joiner) join(c ns.Conjunction, i int, vars map[int]string, row []ns.Tuple, yield func([]ns.Tuple, error) bool) bool {
	if i == len(c.Atoms) {
		return yield(slices.Clone(row), nil)
	}
	for _, t := range s.Tuples(c.Atoms[i].Relation) {
		next, ok := map[int]string{}, true
		for k, v := range vars {
			next[k] = v
		}
		for j, arg := range c.Atoms[i].Args {
			switch {
			case arg.Const != nil:
				ok = ok && t.Vals[j].S == arg.Const.S
			case arg.Var != 0:
				if v, seen := next[arg.Var]; seen {
					ok = ok && v == t.Vals[j].S
				}
				next[arg.Var] = t.Vals[j].S
			}
		}
		if ok && !s.join(c, i+1, next, append(row, t), yield) {
			return false
		}
	}
	return true
}

// A join the store can evaluate goes to it as one call, which ships only the
// rows that join, where lookups would cost a call per literal.
func Example_storePush() {
	answer(storeBase(joiner{looker{source(), "store"}}), `imports(?a, ?b), imports(?b, ?c) => ?c`, "c")
	// Output:
	// store: push imports(?1, ?2), imports(?2, ?3)
	// log util
}

// walker is a looker that also follows a relation from value to value
// itself (ns.WalkSource), as SQLite does with WITH RECURSIVE. It returns
// every fact it reaches, since the engine asks nothing more about a value it
// reached.
type walker struct{ looker }

func (s walker) Walks(rel string, from, to int, bound []int) bool { return rel == "imports" }

func (s walker) Walk(_ context.Context, w ns.Walk) iter.Seq2[ns.Tuple, error] {
	var start []string
	for _, v := range w.Start {
		start = append(start, v.S)
	}
	fmt.Printf("%s: walk %s from %s\n", s.name, w.Relation, strings.Join(start, " "))
	return func(yield func(ns.Tuple, error) bool) {
		seen, todo := map[string]bool{}, slices.Clone(start)
		for len(todo) > 0 {
			at := todo[0]
			todo = todo[1:]
			if seen[at] {
				continue
			}
			seen[at] = true
			for _, t := range s.matching(w.Relation, map[int]ns.Value{w.From: ns.S(at)}) {
				if !yield(t, nil) {
					return
				}
				todo = append(todo, t.Vals[w.To].S)
			}
		}
	}
}

// A recursive rule following a relation probes it round by round, a call
// per level. A store that walks is asked once for the whole walk instead.
// The recursive rule reads imports itself: deps.depends_on reads it through
// _step, which isn't inlined into a recursive rule, so it's probed per round.
func Example_storeWalk() {
	reach := `reach(?a, ?b) :- imports(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), imports(?b, ?c); `
	answer(storeBase(walker{looker{source(), "store"}}), reach+`reach("app", ?d) => ?d`, "d")
	// Output:
	// store: walk imports from app
	// api log util
}
