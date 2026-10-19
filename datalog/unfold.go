package datalog

import (
	"strconv"

	"github.com/panyam/jaala/ns"
)

// unfold inlines calls to single-rule, non-recursive derived relations, replacing each call with the
// body of the relation's one rule. It is SemiNaive's, run before plan (see SemiNaive), and it exists
// for what the planner can do afterwards: a bound argument at the call reaches the literals inside, so
// `go.covers(?t, "X")` over `covers(?t, ?f) :- test(?t), graph.reach(?t, ?f, ...)` becomes one
// walk back from "X", where materializing covers walked out from every test.
//
// A call is inlined only when doing so cannot change an answer:
//
//   - The relation is not recursive, so inlining terminates.
//   - It has one rule. Several rules are a disjunction, which a goal cannot hold (see #28).
//   - Its head is distinct variables, or constants the call matches with constants or "_". A head
//     constant meeting a caller variable would need "=" to bind, and it does not.
//   - The call is positive. "not p(...)" negates the whole body, not each literal of it.
//   - The relation's rule isn't a projection: every variable its positive body binds is in its head
//     (see projects). has_tp(?n) :- pin(?tp, ?n), ... holds a net once, and inlined, each probe of a
//     net re-lists its test points (#139). Demand still reaches it, deduplicated (see magic).
//   - No aggregate of the goal, or of the rule whose body holds the call, counts bindings: count,
//     sum, avg and list without distinct. A derived relation is a set, so has_tp(?n) yields each net once,
//     but its inlined body yields a net once per test point on it, and `=> count(?n)` would count
//     those. Any other rule body is safe, since its head is a set, and min, max and distinct
//     aggregates never count duplicates.
//   - The rule holding the call isn't one of a recursive relation's (#97), unless the call's rule only
//     renames one relation (renames, #319). The fixpoint runs such a body every round, so an inlined
//     join would be redone each time, where the relation is derived once and read through an index.
//   - The relation's rule doesn't aggregate (#4): its body yields what the aggregate reduces, not the
//     relation's tuples. An aggregate head argument is no variable, so the head rule below refuses it.
//   - No number the call passes reaches a comparison or a predicate in the body (#227). Inlined, it
//     reads the call's spelling, 1A, where the relation's rule reads the data's, 1 V, and arithmetic
//     over it answers in another unit. Demand reads the data's.
//
// A relation the goal no longer reaches once its calls are inlined loses its rules, so it is not
// materialized; a relation still called elsewhere, or under negation, keeps them.
func unfold(b *Base, q Query) Query {
	byHead := map[string][]Rule{}
	for _, r := range q.Rules {
		byHead[r.Head.Relation] = append(byHead[r.Head.Relation], r)
	}
	u := &unfolder{byHead: byHead, recursive: recursiveRelations(q.Rules), reg: b.reg}
	out := q
	if len(out.Select) == 0 {
		out.Select = defaultSelect(q.Goal) // the answer's columns are the written goal's, not the inlined one's
	}
	out.Rules = make([]Rule, 0, len(q.Rules))
	for _, r := range q.Rules {
		if !countsBindings(r.Head.Args, nil) {
			u.renamesOnly = u.recursive[r.Head.Relation]
			r.Body = u.body(r.Body)
			u.renamesOnly = false
		}
		out.Rules = append(out.Rules, r)
	}
	if !countsBindings(out.Select, q.Having) {
		out.Goal = u.body(q.Goal)
	}
	return dropUnreached(q, out)
}

type unfolder struct {
	byHead    map[string][]Rule
	recursive map[string]bool
	reg       *ns.Vocabulary
	fresh     int
	// renamesOnly is set while inlining into a recursive relation's rule, which takes only renames.
	renamesOnly bool
}

// body inlines every inlinable call in a body, including calls inside what it inlines.
func (u *unfolder) body(b Body) Body {
	var out []Literal
	queue := append([]Literal(nil), b.Literals...)
	for len(queue) > 0 {
		lit := queue[0]
		queue = queue[1:]
		if lit.Pos != nil {
			if lits, ok := u.inline(*lit.Pos); ok {
				queue = append(lits, queue...)
				continue
			}
		}
		out = append(out, lit)
	}
	return Body{Literals: out}
}

// inline returns the body that replaces a call, or false when the call must stay.
func (u *unfolder) inline(call Atom) ([]Literal, bool) {
	rules := u.byHead[call.Relation]
	if len(rules) != 1 || u.recursive[call.Relation] {
		return nil, false
	}
	r := rules[0]
	if len(r.Head.Args) != len(call.Args) || projects(r) || u.renamesOnly && !u.renames(r) {
		return nil, false
	}
	u.fresh++
	sub := map[Var]Term{}
	for i, h := range r.Head.Args {
		c := call.Args[i]
		switch {
		case h.Const != nil:
			if !(c.Var == "_" || (c.Const != nil && valueEq(*h.Const, *c.Const))) {
				return nil, false
			}
		case h.Var == "" || h.Var == "_":
			return nil, false
		default:
			if _, repeated := sub[h.Var]; repeated {
				return nil, false
			}
			if c.Var == "_" {
				c = Term{Var: u.freshVar(h.Var)}
			}
			if c.Const != nil && c.Const.Num != nil && u.readsValue(r.Body, h.Var) {
				return nil, false
			}
			sub[h.Var] = c
		}
	}
	rename := func(t Term) Term {
		if t.Var == "" || t.Var == "_" {
			return t
		}
		if s, ok := sub[t.Var]; ok {
			return s
		}
		s := Term{Var: u.freshVar(t.Var)}
		sub[t.Var] = s
		return s
	}
	atom := func(a Atom) *Atom {
		out := Atom{Relation: a.Relation, Args: make([]Term, len(a.Args))}
		for i, t := range a.Args {
			out.Args[i] = rename(t)
		}
		return &out
	}
	var lits []Literal
	for _, l := range r.Body.Literals {
		switch {
		case l.Pos != nil:
			lits = append(lits, Literal{Pos: atom(*l.Pos), computed: l.computed})
		case l.Neg != nil:
			lits = append(lits, Literal{Neg: atom(*l.Neg)})
		default:
			c := *l.Compare
			c.Left, c.Right = rename(c.Left), rename(c.Right)
			lits = append(lits, Literal{Compare: &c})
		}
	}
	return lits, true
}

// renames reports whether r only renames a relation: its body is one positive call to a relation, not a
// predicate. Inlined into a recursive relation's rule it adds no join or call for the fixpoint to redo
// each round, and the body then reads the relation itself, so a store can walk it (#319).
func (u *unfolder) renames(r Rule) bool {
	if len(r.Body.Literals) != 1 || r.Body.Literals[0].Pos == nil {
		return false
	}
	_, pred := u.reg.Predicate(r.Body.Literals[0].Pos.Relation)
	return !pred
}

// readsValue reports whether a body reads v as a value rather than only matching it: in a comparison,
// or as an argument of a predicate, which may compute from it. Expressions are lowered to predicate
// calls before unfold runs.
func (u *unfolder) readsValue(body Body, v Var) bool {
	for _, l := range body.Literals {
		switch {
		case l.Compare != nil:
			if l.Compare.Left.Var == v || l.Compare.Right.Var == v {
				return true
			}
		case l.Pos != nil:
			if _, ok := u.reg.Predicate(l.Pos.Relation); !ok {
				continue
			}
			for _, t := range l.Pos.Args {
				if t.Var == v {
					return true
				}
			}
		}
	}
	return false
}

// projects reports whether a rule's positive body binds a variable its head drops, "_" included. Such
// a relation is a projection: has_tp(?n) :- pin(?tp, ?n), part(?tp, "test_point") holds each net
// once, but its body yields a net once per test point on it. Inlined into a rule, every probe of one
// net re-lists those test points, once per caller binding (#139), where the relation is derived once,
// or under demand once per distinct net. A variable only a negation reads enumerates nothing.
func projects(r Rule) bool {
	head := map[Var]bool{}
	for _, t := range r.Head.Args {
		head[t.Var] = true
	}
	for _, l := range r.Body.Literals {
		if l.Pos == nil {
			continue
		}
		for _, t := range l.Pos.Args {
			if t.Var == "_" || (t.Var != "" && !head[t.Var]) {
				return true
			}
		}
	}
	return false
}

// freshVar is a variable no query can spell, distinct per inlined call, so an inlined body's own
// variables never meet the caller's.
func (u *unfolder) freshVar(name Var) Var {
	return Var("\x00" + strconv.Itoa(u.fresh) + "." + string(name))
}

// recursiveRelations is every rule head that depends on itself, through positive or negated reads.
func recursiveRelations(rules []Rule) map[string]bool {
	reads := map[string][]string{}
	for _, r := range rules {
		for _, a := range ruleAtoms(r.Body) {
			reads[r.Head.Relation] = append(reads[r.Head.Relation], a.Relation)
		}
	}
	out := map[string]bool{}
	for head := range reads {
		seen := map[string]bool{}
		stack := append([]string(nil), reads[head]...)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == head {
				out[head] = true
				break
			}
			if !seen[n] {
				seen[n] = true
				stack = append(stack, reads[n]...)
			}
		}
	}
	return out
}

// countsBindings reports whether an aggregate in the answer, or in a rule head, counts bindings rather
// than values: count, sum or list without distinct. Inlining into such a body could change its answer.
func countsBindings(sel []Term, having []Compare) bool {
	aggs := make([]*Aggregate, 0, len(sel)+len(having))
	for _, t := range sel {
		aggs = append(aggs, t.Agg)
	}
	for _, h := range having {
		aggs = append(aggs, h.Left.Agg)
	}
	for _, a := range aggs {
		if a != nil && !a.Distinct && (a.Func == "count" || a.Func == "sum" || a.Func == "avg" || a.Func == "list") {
			return true
		}
	}
	return false
}

// withoutUnreached drops the rules the goal never reaches, before any rewrite. They can't change the
// answer, and a rewrite renames or removes what they read, which left them reading a relation with
// no rules (#90). evaluate has already checked them as linked, so their mistakes are still reported.
func withoutUnreached(q Query) Query {
	is := reached(q)
	kept := q.Rules[:0:0]
	for _, r := range q.Rules {
		if is[r.Head.Relation] {
			kept = append(kept, r)
		}
	}
	q.Rules = kept
	return q
}

// dropUnreached removes the rules that the goal reached before unfolding and no longer does: a relation
// whose every call was inlined, and anything only it read. Rules the goal didn't reach before, which
// are a rewrite's own relations, are left as they were.
func dropUnreached(before, after Query) Query {
	was, is := reached(before), reached(after)
	kept := after.Rules[:0:0]
	for _, r := range after.Rules {
		if is[r.Head.Relation] || !was[r.Head.Relation] {
			kept = append(kept, r)
		}
	}
	after.Rules = kept
	return after
}

// reached is every relation the goal reads, directly or through rules.
func reached(q Query) map[string]bool {
	reads := map[string][]string{}
	for _, r := range q.Rules {
		for _, a := range ruleAtoms(r.Body) {
			reads[r.Head.Relation] = append(reads[r.Head.Relation], a.Relation)
		}
	}
	out := map[string]bool{}
	var stack []string
	for _, a := range ruleAtoms(q.Goal) {
		stack = append(stack, a.Relation)
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !out[n] {
			out[n] = true
			stack = append(stack, reads[n]...)
		}
	}
	return out
}
