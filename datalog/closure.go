package datalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/panyam/jaala/ns"
)

// graph.closure("imports", ?x, ?y, 1, 6, ?d) holds when ?y is reachable from ?x in 1 to 6 steps of the
// binary relation imports, base or derived, ?d the fewest such steps (#281). Max 0 is no bound, and
// min 0 lets ?x reach itself in none when the relation holds it (#280). Each answer cites one path of ?d steps, and its witness has a
// child per step. The engine walks breadth first from whichever end is bound, once per node and depth
// below min, so it costs the nodes and steps within the bound, where a recursive rule with a depth
// counter would derive every walk.
//
// lowerClosures writes each call as \x00c:imports with its six arguments as written, so every check
// reads the step relation from the name, as a fact relation's (facts.go). The step relation is read
// whole: stratify puts it below the closure, as below a negation, and no rewrite passes demand into it.

// closureName is how a query calls a closure.
const closureName = "graph.closure"

// closurePrefix starts a closure's relation name.
const closurePrefix = "\x00c:"

// closureRelation is the closure over step.
func closureRelation(step string) string { return closurePrefix + step }

// An engineWalk is a call the engine answers by walking a relation itself: graph.closure (#281) and
// graph.paths (#280). A call is written as a relation named by the walk's prefix and the relation it
// walks, its arguments as written, so every check reads the step relation from the name.
type engineWalk struct {
	name, prefix string
	builtin      ns.Builtin
	fix          string
	lo           int   // the argument holding min, max the one after it
	arities      []int // the step relation's arities it walks
}

var closureWalk = &engineWalk{name: closureName, prefix: closurePrefix, builtin: closureBuiltin, fix: closureFix, lo: 3, arities: []int{2}}

// engineWalks are the walks a query can call.
var engineWalks []*engineWalk

func init() { engineWalks = []*engineWalk{closureWalk, pathsWalk} }

// walkOf is the walk a relation name calls and the relation it walks.
func walkOf(rel string) (*engineWalk, string, bool) {
	for _, w := range engineWalks {
		if step, ok := strings.CutPrefix(rel, w.prefix); ok {
			return w, step, true
		}
	}
	return nil, "", false
}

// walkCalled is the walk a query calls by name, or nil.
func walkCalled(name string) *engineWalk {
	for _, w := range engineWalks {
		if w.name == name {
			return w
		}
	}
	return nil
}

// stepOf is the relation an engine walk walks, and whether rel is one.
func stepOf(rel string) (string, bool) {
	_, step, ok := walkOf(rel)
	return step, ok
}

// isClosure reports whether rel is a closure.
func isClosure(rel string) bool { return strings.HasPrefix(rel, closurePrefix) }

// isWalk reports whether rel is an engine walk: a closure or a paths call.
func isWalk(rel string) bool {
	_, _, ok := walkOf(rel)
	return ok
}

// walkName is how a query calls the engine walk rel.
func walkName(rel string) string {
	w, _, _ := walkOf(rel)
	return w.name
}

// closureBuiltin is what a closure is to the checks and the planner: a generator run from either end,
// or from every value the step relation leads from when neither is bound, with its relation and bounds
// bound too. extendAtom runs closures itself, so Gen is never called; it
// is set so the closure reads as a generator.
var closureBuiltin = ns.Builtin{
	Arity:  6,
	Modes:  [][]bool{{true, true, false, true, true, false}, {true, false, true, true, true, false}, {true, false, false, true, true, false}},
	Labels: []string{"relation", "from", "to", "min", "max", "hops"},
	Types:  []ns.ArgType{{Type: ns.TypeString}, {}, {}, {Type: ns.TypeNumber}, {Type: ns.TypeNumber}, {Type: ns.TypeNumber}},
	Doc:    "?to is reachable from ?from in min to max steps of the relation (max 0 for no bound), ?hops the fewest, walked from whichever end is bound",
	Gen:    engineRun,
}

// engineRun is an engine walk's Gen, set so it reads as a generator and never called.
func engineRun(context.Context, ns.Source, []ns.Arg, func([]ns.Value, []string) error) error {
	return errors.New("query: internal: an engine walk is run by the engine")
}

// relationsRead is what a call to rel reads: rel, and for an engine walk its step relation too.
func relationsRead(rel string) []string {
	if step, ok := stepOf(rel); ok {
		return []string{rel, step}
	}
	return []string{rel}
}

// predicateIn is rel's predicate in reg, or for an engine walk its builtin.
func predicateIn(reg *ns.Vocabulary, rel string) (ns.Builtin, bool) {
	if w, _, ok := walkOf(rel); ok {
		return w.builtin, true
	}
	return reg.Predicate(rel)
}

// lowerClosures writes each graph.closure call as a call to its relation's closure. It refuses a call
// whose relation isn't a text constant or that takes other than six arguments.
func lowerClosures(q Query) (Query, error) {
	var err error
	if q.Goal, err = lowerClosureBody(q.Goal); err != nil {
		return q, err
	}
	q.Rules, err = lowerClosureRules(q.Rules)
	return q, err
}

// lowerClosureRules is lowerClosures for rules alone, a module's.
func lowerClosureRules(rules []Rule) ([]Rule, error) {
	var out []Rule
	for i, r := range rules {
		body, err := lowerClosureBody(r.Body)
		if err != nil {
			return nil, err
		}
		if out == nil && !sameBody(body, r.Body) {
			out = append([]Rule(nil), rules...)
		}
		if out != nil {
			out[i].Body = body
		}
	}
	if out == nil {
		return rules, nil
	}
	return out, nil
}

func lowerClosureBody(b Body) (Body, error) {
	var out []Literal
	for i, l := range b.Literals {
		lit, err := lowerClosureLiteral(l)
		if err != nil {
			return b, err
		}
		if out == nil && lit != l {
			out = append([]Literal(nil), b.Literals...)
		}
		if out != nil {
			out[i] = lit
		}
	}
	if out == nil {
		return b, nil
	}
	b.Literals = out
	return b, nil
}

func lowerClosureLiteral(l Literal) (Literal, error) {
	var err error
	switch {
	case l.Agg != nil:
		body, err := lowerClosureBody(l.Agg.Body)
		if err != nil || sameBody(body, l.Agg.Body) {
			return l, err
		}
		agg := *l.Agg
		agg.Body = body
		l.Agg = &agg
	case l.Pos != nil && walkCalled(l.Pos.Relation) != nil:
		l.Pos, err = lowerClosureAtom(l.Pos)
	case l.Neg != nil && walkCalled(l.Neg.Relation) != nil:
		l.Neg, err = lowerClosureAtom(l.Neg)
	}
	return l, err
}

func lowerClosureAtom(a *Atom) (*Atom, error) {
	w := walkCalled(a.Relation)
	if n := len(a.Args); n != w.builtin.Arity {
		return a, checkErr(KindArity, fmt.Sprintf("query: %s takes %d args, got %d", w.name, w.builtin.Arity, n)).call(w.name, n).fix(w.fix)
	}
	rel := a.Args[0].Const
	if rel == nil || rel.Num != nil || rel.Absent || rel.S == "" {
		return a, checkErr(KindType, fmt.Sprintf("query: %s's relation must be a text constant naming one, not %s", w.name, a.Args[0])).call(w.name, len(a.Args)).argument(0, a.Args[0].String()).fix(w.fix)
	}
	if a.Fact != "" {
		return a, factErr(a, fmt.Sprintf("query: %s is a predicate, whose matches are no stored facts, so \"as ?%s\" names none", w.name, a.Fact), "drop `as ?"+string(a.Fact)+"`")
	}
	out := *a
	out.Relation = w.prefix + rel.S
	return &out, nil
}

const closureFix = `write graph.closure("relation", ?from, ?to, min, max, ?hops), max 0 for no bound and _ for hops you don't read`

// checkClosure checks an engine walk as written: its step relation is a relation of an arity it walks,
// base or derived (derived holds the program's own, by arity), that takes nothing from its caller, its
// constant bounds are steps, and a paths call's constant mode is one.
func (b *Base) checkClosure(a *Atom, derived map[string]int) error {
	w, step, _ := walkOf(a.Relation)
	if strings.HasPrefix(step, walkPrefix) {
		return nil // walkSteps checked it, after the call as written was
	}
	call := func(kind ErrorKind, msg string) *CheckError {
		return checkErr(kind, msg).call(w.name, len(a.Args)).argument(0, a.Args[0].String())
	}
	n, ok := derived[step]
	if !ok {
		if _, isPred := b.reg.Predicate(step); isPred {
			return call(KindDefinition, fmt.Sprintf("query: %s can't walk %s, a predicate: it walks a relation's facts", w.name, step)).fix("walk a relation, or a rule that calls " + step)
		}
		s, found := b.schemaOf(step)
		if !found {
			e := b.unknownErr(&Atom{Relation: step, Args: a.Args[1:3]}, "query: %s over %s", w.name)
			e.Relation, e.Args = w.name, len(a.Args)
			return e
		}
		n = s.Arity
	}
	if !slices.Contains(w.arities, n) {
		if w == closureWalk {
			return call(KindArity, fmt.Sprintf("query: %s needs a relation of 2 args, from and to; %s takes %d", w.name, displayName(step), n)).fix("write a rule of two arguments that reads " + displayName(step) + " and walk that")
		}
		return call(KindArity, fmt.Sprintf("query: %s needs a relation of 2 args, from and to, or 3, from, edge and to; %s takes %d", w.name, displayName(step), n)).fix("write a rule of two or three arguments that reads " + displayName(step) + " and walk that")
	}
	if _, ok := b.inputRels[step]; ok {
		return call(KindDefinition, fmt.Sprintf("query: %s can't walk %s, which takes an argument from its caller", w.name, displayName(step)))
	}
	var mode *ns.Value
	if w == pathsWalk && a.Args[3].Const != nil {
		mode = a.Args[3].Const
		if _, err := pathMode(*mode); err != nil {
			return err
		}
	}
	var lo, hi *ns.Value
	if t := a.Args[w.lo]; t.Const != nil {
		lo = t.Const
	}
	if t := a.Args[w.lo+1]; t.Const != nil {
		hi = t.Const
	}
	if lo != nil || hi != nil {
		_, h, err := w.bounds(lo, hi)
		if err != nil {
			return err
		}
		if mode != nil && mode.S == "walk" && hi != nil && h == 0 {
			return errWalkUnbounded()
		}
	}
	return nil
}

// closureBounds reads a closure's bounds, as bounds does.
func closureBounds(lo, hi *ns.Value) (int, int, error) { return closureWalk.bounds(lo, hi) }

// bounds reads a walk's bounds, either nil when not yet known: a whole number of steps, min at least 0,
// and max 0 for none or at least min.
func (w *engineWalk) bounds(lo, hi *ns.Value) (int, int, error) {
	steps := func(v *ns.Value) (int, bool) {
		if v == nil {
			return 0, true
		}
		if v.Num == nil || *v.Num < 0 || *v.Num != math.Trunc(*v.Num) {
			return 0, false
		}
		return int(min(*v.Num, math.MaxInt32)), true
	}
	l, ok := steps(lo)
	if !ok {
		return 0, 0, checkErr(KindType, fmt.Sprintf("query: %s's min must be a whole number of steps, 0 or more, not %s", w.name, lo.S)).call(w.name, w.builtin.Arity).argument(w.lo, lo.S)
	}
	h, ok := steps(hi)
	if !ok || (hi != nil && h != 0 && h < l) {
		return 0, 0, checkErr(KindType, fmt.Sprintf("query: %s's max must be a whole number of steps, 0 for no bound or at least min, not %s", w.name, hi.S)).call(w.name, w.builtin.Arity).argument(w.lo+1, hi.S)
	}
	return l, h, nil
}

// extendClosure yields each binding of a closure's call under bnd: walking out from a bound ?from, or
// back from a bound ?to, and with both bound stopping at ?to.
func (b *Base) extendClosure(atom *Atom, bnd *binding, yield func(*binding) error) error {
	if b.run != nil && b.run.explain != nil {
		b.run.explain.access("closure")
	}
	if b.across != nil {
		return checkErr(KindDefinition, fmt.Sprintf("query: %s isn't evaluated across versions: ask each version with At", closureName)).call(closureName, len(atom.Args))
	}
	step, _ := stepOf(atom.Relation)
	var vals [6]ns.Value
	var bound [6]bool
	for i, t := range atom.Args {
		vals[i], bound[i] = resolve(t, bnd)
	}
	if !bound[3] || !bound[4] {
		return checkErr(KindUnbound, fmt.Sprintf("query: %s needs its min and max bound (a variable must appear in a relation before %s reads it)", closureName, closureName)).call(closureName, len(atom.Args))
	}
	lo, hi, err := closureBounds(&vals[3], &vals[4])
	if err != nil {
		// A constant bound was refused when the program was checked, so this one came from data, and
		// which data reaches a call depends on the plan: an error here failed Naive where the planned
		// evaluator, which never ran the rule, answered (FuzzEval). A bound that isn't one walks nothing.
		return nil
	}
	if !bound[1] && !bound[2] {
		// from every value a step leads from, and with min 0 every value one leads to as well
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
			err := b.extendClosure(&from, bnd, func(bnd *binding) error {
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
	fwd := bound[1]
	start, target := vals[2], (*ns.Value)(nil)
	if fwd {
		start = vals[1]
		if bound[2] {
			target = &vals[2]
		}
	}
	m := bnd.mark()
	return b.closure(step, start, fwd, lo, hi, target, func(node ns.Value, depth int, path func() ([]closureStep, error)) error {
		if err := b.countWork(); err != nil {
			return err
		}
		row := vals
		if fwd {
			row[2] = node
		} else {
			row[1] = node
		}
		row[5] = ns.N(float64(depth))
		steps, err := path()
		if err != nil {
			return err
		}
		t := ns.Tuple{Vals: row[:]}
		for _, s := range steps {
			t.Cites = append(t.Cites, s.cites...)
		}
		if !unify(atom.Args, t, bnd) {
			bnd.reset(m)
			return nil
		}
		if b.witnessing() {
			bnd.last = closureWitness(closureName, row[:], steps)
		}
		err = yield(bnd)
		bnd.reset(m)
		return err
	})
}

// A closureStep is one fact of a closure's path: its values, citations and, when witnessing, witness.
type closureStep struct {
	vals  []ns.Value
	cites []string
	wit   *Witness
}

// closureState is a node reached at a depth, the depth capped at the closure's min: below min every
// depth is its own state, since a node reached in 1 step and in 2 may reach min at different times.
type closureState struct {
	key   string
	depth int
}

// errClosureFound stops a walk that reached its bound target.
var errClosureFound = errors.New("query: closure found its target")

// closure walks step breadth first from start, out along its facts (fwd) or back against them, and
// visits each node reached in lo to hi steps (hi 0: any number) once, at the fewest steps, with a
// function returning the path of that many steps from ?from to ?to. With target set it visits only
// target, and stops there. Naive and SemiNaive both walk through it, reading step through extendAtom,
// so a step's facts come as any atom's would: indexed, looked up, and counted as work.
func (b *Base) closure(step string, start ns.Value, fwd bool, lo, hi int, target *ns.Value, visit func(ns.Value, int, func() ([]closureStep, error)) error) error {
	type reached struct {
		node   ns.Value
		parent int
		step   closureStep
	}
	all := []reached{{node: start, parent: -1}}
	seen := map[closureState]bool{{valueKey(start), 0}: true}
	pathOf := func(i int) func() ([]closureStep, error) {
		return func() ([]closureStep, error) {
			if b.canonical() {
				from, to := start, all[i].node
				if !fwd {
					from, to = to, from
				}
				return b.leastPath(step, from, to, lo, hi)
			}
			var out []closureStep
			for ; all[i].parent >= 0; i = all[i].parent {
				out = append(out, all[i].step)
			}
			if fwd {
				slices.Reverse(out)
			}
			return out, nil
		}
	}
	// a node arrives once, at the first state of its depth capped at lo, which is its fewest steps
	arrive := func(i, depth int) error {
		if target != nil && !valueEq(all[i].node, *target) {
			return nil
		}
		if err := visit(all[i].node, depth, pathOf(i)); err != nil {
			return err
		}
		if target != nil {
			return errClosureFound
		}
		return nil
	}
	err := func() error {
		if lo == 0 {
			on, err := b.onStep(step, start)
			if err != nil {
				return err
			}
			if on {
				if err := arrive(0, 0); err != nil {
					return err
				}
			}
		}
		frontier := []int{0}
		for depth := 1; len(frontier) > 0 && (hi == 0 || depth <= hi); depth++ {
			var next []int
			for _, i := range frontier {
				err := b.closureNeighbours(step, all[i].node, fwd, func(n ns.Value, s closureStep) error {
					st := closureState{valueKey(n), min(depth, lo)}
					if seen[st] {
						return nil
					}
					seen[st] = true
					all = append(all, reached{node: n, parent: i, step: s})
					next = append(next, len(all)-1)
					if st.depth == lo {
						return arrive(len(all)-1, depth)
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			frontier = next
		}
		return nil
	}()
	if errors.Is(err, errClosureFound) {
		return nil
	}
	return err
}

// walkPrefix starts the name a closure calls a step relation it walks through its rules (walkSteps).
const walkPrefix = "\x00w:"

// walkSteps is the planned evaluator's first rewrite for closures: one whose step relation's rules
// each read one base relation, as cypher_step1(?x, ?y) :- edge(?x, "CALLS", ?y) does, walks those
// atoms instead, so it reads only the facts its walk reaches, through the index or the Source's
// lookups, rather than the step relation derived in full. The rules stay for anything else that reads
// the relation, and withoutUnreached drops them otherwise.
func walkSteps(b *Base, q Query) Query {
	byHead := map[string][]Rule{}
	for _, r := range q.Rules {
		byHead[r.Head.Relation] = append(byHead[r.Head.Relation], r)
	}
	walked := func(a *Atom) *Atom {
		w, step, ok := walkOf(a.Relation)
		if !ok || strings.HasPrefix(step, walkPrefix) {
			return a
		}
		rules := byHead[step]
		if len(rules) == 0 {
			return a
		}
		for _, r := range rules {
			if !b.walksThrough(r) {
				return a
			}
		}
		if b.run.closureWalks == nil {
			b.run.closureWalks = map[string][]Rule{}
		}
		b.run.closureWalks[walkPrefix+step] = rules
		out := *a
		out.Relation = w.prefix + walkPrefix + step
		return &out
	}
	body := func(bd Body) Body {
		out := bd
		out.Literals = slices.Clone(bd.Literals)
		for i, l := range out.Literals {
			switch {
			case l.Pos != nil:
				out.Literals[i].Pos = walked(l.Pos)
			case l.Neg != nil:
				out.Literals[i].Neg = walked(l.Neg)
			}
		}
		return out
	}
	q.Goal = body(q.Goal)
	rules := make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		rules[i] = r
		rules[i].Body = body(r.Body)
	}
	q.Rules = rules
	return q
}

// walksThrough reports whether a walk can walk r's one atom for its relation: r's head is two or three
// distinct variables, its body one positive atom over a base relation reading each once.
func (b *Base) walksThrough(r Rule) bool {
	h := r.Head.Args
	if len(h) < 2 || len(h) > 3 || r.aggregates() || len(r.Body.Literals) != 1 {
		return false
	}
	heads := map[Var]bool{}
	for _, t := range h {
		if t.Var == "" || heads[t.Var] {
			return false
		}
		heads[t.Var] = true
	}
	a := r.Body.Literals[0].Pos
	if a == nil || isFact(a.Relation) || isPushed(a.Relation) {
		return false
	}
	if _, ok := b.schemaOf(a.Relation); !ok {
		return false
	}
	seen := map[Var]int{}
	for _, t := range a.Args {
		if t.Var != "" {
			seen[t.Var]++
		}
	}
	for v := range heads {
		if seen[v] != 1 {
			return false
		}
	}
	return true
}

// closureVar is the variable a closure's step binds to the next node, and closureFrom the start a
// closure called with neither end bound walks from, which no query can write.
const (
	closureVar  = Var("\x00c")
	closureFrom = Var("\x00c.from")
	closureEdge = Var("\x00c.edge") // a three-argument step's edge, for graph.paths
)

// stepArgs are the arguments of a call to a step relation of arity n from from to to, an edge variable
// between them when it has three.
func stepArgs(n int, from, to Term) []Term {
	if n == 3 {
		return []Term{from, {Var: closureEdge}, to}
	}
	return []Term{from, to}
}

// stepArity is the arity of the relation a walk steps through.
func (b *Base) stepArity(step string) int {
	if rules, ok := b.run.closureWalks[step]; ok {
		return len(rules[0].Head.Args)
	}
	if n, ok := b.idbArity[step]; ok {
		return n
	}
	if s, ok := b.schemaOf(step); ok {
		return s.Arity
	}
	return 2
}

// closureStarts is every value a fact of step leads from, and with ends every value one leads to too,
// once each, in the order the facts come. It reads through stepReads, so a step walkSteps walks
// through its rules is read through them too: the renamed step itself holds nothing (#438).
func (b *Base) closureStarts(step string, ends bool) ([]ns.Value, error) {
	var out []ns.Value
	seen := map[string]bool{}
	for _, a := range b.stepReads(step, Term{Var: closureFrom}, true) {
		err := b.extendAtom(&a, newBinding(), func(sb *binding) error {
			vars := []Var{closureFrom}
			if ends {
				vars = append(vars, closureVar)
			}
			for _, v := range vars {
				val := sb.value(v)
				if k := valueKey(val); !seen[k] {
					seen[k] = true
					out = append(out, val)
				}
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// errOnStep stops onStep's read at the first fact.
var errOnStep = errors.New("query: closure found a fact")

// onStep reports whether a fact of step leads from node or to it: a node reaches itself in no steps
// only when the relation holds it, as when neither end is bound and the walk starts from what the
// facts hold (closureStarts), so binding an end, as demand does, can't add a path.
func (b *Base) onStep(step string, node ns.Value) (bool, error) {
	for _, fwd := range []bool{true, false} {
		err := b.closureNeighbours(step, node, fwd, func(ns.Value, closureStep) error { return errOnStep })
		if errors.Is(err, errOnStep) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

// closureNeighbours calls each with every node one fact of step leads to from node, out or back, and
// that fact.
func (b *Base) closureNeighbours(step string, node ns.Value, fwd bool, each func(ns.Value, closureStep) error) error {
	arity := b.stepArity(step)
	for _, a := range b.stepReads(step, Term{Const: &node}, fwd) {
		err := b.extendAtom(&a, newBinding(), func(sb *binding) error {
			n := sb.value(closureVar)
			s := closureStep{vals: stepValues(arity, node, n, sb, fwd), cites: slices.Clone(sb.cites)}
			if b.witnessing() {
				s.wit = sb.last
				if s.wit == nil {
					s.wit = leaf(step, s.vals, s.cites)
				}
			}
			return each(n, s)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// stepReads are the atoms a walk reads a node's steps of step through, out (fwd) or back, with at
// where the node goes, closureVar where the step leads and closureEdge at its edge: the step relation
// itself, or each of its rules' one atom when walkSteps walks through them (walksThrough).
func (b *Base) stepReads(step string, at Term, fwd bool) []Atom {
	if rules, ok := b.run.closureWalks[step]; ok {
		out := make([]Atom, len(rules))
		for i, r := range rules {
			out[i] = ruleRead(r, at, fwd)
		}
		return out
	}
	from, to := at, Term{Var: closureVar}
	if !fwd {
		from, to = to, from
	}
	return []Atom{{Relation: step, Args: stepArgs(b.stepArity(step), from, to)}}
}

// stepValues is a step's fact in from, (edge,) to order, read from sb: node where the walk is, n where
// it goes.
func stepValues(arity int, node, n ns.Value, sb *binding, fwd bool) []ns.Value {
	from, to := node, n
	if !fwd {
		from, to = n, node
	}
	if arity == 3 {
		return []ns.Value{from, sb.value(closureEdge), to}
	}
	return []ns.Value{from, to}
}

// ruleRead is the atom of one rule walksThrough accepted, read from at: the head's first variable is
// where the walk is and its last where it goes when walking out, the other way round walking back, and
// a middle one the edge.
func ruleRead(r Rule, at Term, fwd bool) Atom {
	h := r.Head.Args
	here, next := h[0].Var, h[len(h)-1].Var
	if !fwd {
		here, next = next, here
	}
	edge := Var("\x00none")
	if len(h) == 3 {
		edge = h[1].Var
	}
	a := *r.Body.Literals[0].Pos
	a.Args = slices.Clone(a.Args)
	for i, t := range a.Args {
		switch t.Var {
		case here:
			a.Args[i] = at
		case next:
			a.Args[i] = Term{Var: closureVar}
		case edge:
			a.Args[i] = Term{Var: closureEdge}
		}
	}
	return a
}

// leastPath is, for CanonicalCites, the path from from to to of the fewest steps whose facts come
// first, compared step by step in walk order (stepBefore), so it is the same whichever end the walk
// started from and in whatever order the facts arrived. It walks out from from, keeping every fact that
// reaches a state first seen one level on, marks the states on a shortest path back from to's, and
// takes the first fact at each level that stays on one.
func (b *Base) leastPath(step string, from, to ns.Value, lo, hi int) ([]closureStep, error) {
	type edge struct {
		to int
		s  closureStep
	}
	root := closureState{valueKey(from), 0}
	index := map[closureState]int{root: 0}
	states := []closureState{root}
	nodes := []ns.Value{from}
	var out [][]edge
	out = append(out, nil)
	level := []int{0}
	goal := -1
	if lo == 0 && valueEq(from, to) {
		return nil, nil
	}
	frontier := []int{0}
	for depth := 1; len(frontier) > 0 && goal < 0 && (hi == 0 || depth <= hi); depth++ {
		var next []int
		for _, i := range frontier {
			err := b.closureNeighbours(step, nodes[i], true, func(n ns.Value, s closureStep) error {
				st := closureState{valueKey(n), min(depth, lo)}
				j, ok := index[st]
				if !ok {
					j = len(states)
					index[st] = j
					states, nodes, level = append(states, st), append(nodes, n), append(level, depth)
					out = append(out, nil)
					next = append(next, j)
					if st.depth == lo && valueEq(n, to) {
						goal = j
					}
				}
				if level[j] == depth {
					out[i] = append(out[i], edge{j, s})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
		frontier = next
	}
	if goal < 0 {
		return nil, fmt.Errorf("query: internal: %s found no path from %s to %s", closureName, from.S, to.S)
	}
	onPath := map[int]bool{goal: true}
	for d := level[goal] - 1; d >= 0; d-- {
		for i := range states {
			if level[i] != d {
				continue
			}
			for _, e := range out[i] {
				if onPath[e.to] {
					onPath[i] = true
					break
				}
			}
		}
	}
	var path []closureStep
	for i := 0; i != goal; {
		best := -1
		for k, e := range out[i] {
			if onPath[e.to] && (best < 0 || stepBefore(e.s, out[i][best].s)) {
				best = k
			}
		}
		path = append(path, out[i][best].s)
		i = out[i][best].to
	}
	return path, nil
}

// stepBefore orders two facts leaving one node: by the node they lead to, then by citations.
func stepBefore(a, b closureStep) bool {
	if c := strings.Compare(valueKey(a.vals[1]), valueKey(b.vals[1])); c != 0 {
		return c < 0
	}
	return slices.Compare(a.cites, b.cites) < 0
}

// closureWitness is a closure answer's witness: the call as graph.closure, with a child per step of its
// path in walk order, and the path's citations.
func closureWitness(name string, vals []ns.Value, path []closureStep) *Witness {
	w := &Witness{Relation: name, Values: slices.Clone(vals)}
	for _, s := range path {
		w.Children = append(w.Children, s.wit)
		w.Cites = append(w.Cites, s.cites...)
		w.height = max(w.height, s.wit.height)
	}
	return w
}
