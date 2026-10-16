package datalog

import (
	"fmt"

	"github.com/panyam/jaala/ns"
)

// A rule can take an argument from its caller (#200): a head variable no positive body relation
// binds, used by a comparison or a negation, is an input, as ?line is in
//
//	loc.at(?n, ?repo, ?uri, ?line) :- home(?n, ?repo, ?uri, ?s, ?e), ?s <= ?line, ?line <= ?e
//
// Such a relation has no tuples of its own to derive (?line ranges over every number), so it is
// answered per call: lowerInputs inlines each call into its caller, the body renamed to the call's
// arguments, before anything else reads the program, so every evaluator sees ordinary rules. The
// call has to bind each input, by a constant or by a variable its own clause binds.

// inputsBound is a qualifying rule's inputs as the bound set a check of its own body starts from, nil
// for any other rule (see inputRelations).
func inputsBound(r Rule, inputRels map[string]Rule, reg *ns.Vocabulary) map[Var][]ns.Value {
	if _, ok := inputRels[r.Head.Relation]; !ok {
		return nil
	}
	ins := inputsOf(r, inputRels, reg)
	if len(ins) == 0 {
		return nil
	}
	out := make(map[Var][]ns.Value, len(ins))
	for _, v := range ins {
		out[v] = nil
	}
	return out
}

// inputsOf is a rule's input arguments: head variables its body can't bind, which a comparison, a
// negation or a generator reads. What the body binds is what checkModes computes, so a generator
// binds its arguments only once one of its modes is satisfied, and a call to a relation in ins binds
// only its other arguments. When the body binds some head variables only from others, as num.mul
// binds any one of its three from the other two, the inputs are the head's earliest: each unbound
// head variable, left to right, becomes an input and the body is bound again, so power(?v, ?i, ?p) :-
// ?p = ?v * ?i takes ?v and ?i and computes its last argument (#208). A head variable the body
// doesn't use at all is still an error (validateRule's), so a typo isn't taken for an input.
func inputsOf(r Rule, ins map[string]Rule, reg *ns.Vocabulary) []Var {
	used := map[Var]bool{}
	for _, l := range r.Body.Literals {
		for _, t := range literalTerms(l) {
			used[t.Var] = true
		}
	}
	bound := map[Var]bool{}
	bindsFrom(r.Body.Literals, bound, r.Head.Relation, ins, reg)
	var out []Var
	for _, h := range r.Head.Args {
		if h.Agg == nil && h.Var != "" && h.Var != "_" && !bound[h.Var] && used[h.Var] {
			out = append(out, h.Var)
			bound[h.Var] = true
			bindsFrom(r.Body.Literals, bound, r.Head.Relation, ins, reg)
		}
	}
	return out
}

// bindsFrom adds to bound every variable lits bind, given what is bound already, as checkModesFrom
// computes it: a relation binds its arguments, a generator its arguments once a mode is satisfied, a
// filter or comparison nothing, and a call to another of ins (not self) its arguments but its inputs.
func bindsFrom(lits []Literal, bound map[Var]bool, self string, ins map[string]Rule, reg *ns.Vocabulary) {
	done := make([]bool, len(lits))
	for changed := true; changed; {
		changed = false
		for i, l := range lits {
			if done[i] || l.Pos == nil {
				continue
			}
			takes := map[int]bool{}
			if callee, ok := ins[l.Pos.Relation]; ok && l.Pos.Relation != self {
				for _, v := range inputsOf(callee, ins, reg) {
					takes[headIndexOf(callee.Head, v)] = true
				}
			} else if bi, ok := reg.Predicate(l.Pos.Relation); ok {
				if !bi.IsGenerator() {
					done[i] = true
					continue
				}
				if !bi.Satisfied(boundFlags(l.Pos, bound)) {
					continue
				}
			}
			done[i], changed = true, true
			for j, t := range l.Pos.Args {
				if t.Var != "" && t.Var != "_" && !takes[j] {
					bound[t.Var] = true
				}
			}
		}
	}
}

func containsVar(vs []Var, v Var) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// inputRelations is each relation that takes an input and can be inlined: one rule, a head of
// distinct variables with no aggregate, and no recursion. It grows to a fixpoint, since a relation
// passing its argument on to one that takes it takes it too. A relation that takes an input but isn't
// one of these is left to validateRule, which refuses its unbound head variable as it always has.
func inputRelations(rules []Rule, reg *ns.Vocabulary) map[string]Rule {
	byHead := map[string][]Rule{}
	for _, r := range rules {
		byHead[r.Head.Relation] = append(byHead[r.Head.Relation], r)
	}
	recursive := recursiveRelations(rules)
	out := map[string]Rule{}
	for changed := true; changed; {
		changed = false
		for rel, rs := range byHead {
			if _, done := out[rel]; done || len(rs) != 1 || recursive[rel] || len(inputsOf(rs[0], out, reg)) == 0 {
				continue
			}
			seen, ok := map[Var]bool{}, true
			for _, h := range rs[0].Head.Args {
				if h.Agg != nil || h.Const != nil || h.Var == "_" || seen[h.Var] {
					ok = false
				}
				seen[h.Var] = true
			}
			if ok {
				out[rel], changed = rs[0], true
			}
		}
	}
	return out
}

// lowerInputs inlines every call to a relation taking an input, in the goal and in every rule, and
// drops those relations' rules. bound is the goal variables the host binds, which count as bound
// there. A caller's rule keeps its written text, so a witness shows the rule as written.
func lowerInputs(q Query, bound map[Var][]ns.Value, reg *ns.Vocabulary) (Query, error) {
	ins := inputRelations(q.Rules, reg)
	if len(ins) == 0 {
		return q, nil
	}
	l := &inputLowerer{ins: ins, reg: reg}
	out := q
	out.Rules = nil
	for _, r := range q.Rules {
		if _, ok := ins[r.Head.Relation]; ok {
			continue
		}
		body, changed, err := l.clause(whereRule(r), r.Body, nil, countsBindings(r.Head.Args, nil))
		if err != nil {
			return Query{}, err
		}
		if changed {
			if r.text == "" {
				r.text = r.String()
			}
			r.Body = body
		}
		out.Rules = append(out.Rules, r)
	}
	hostBound := map[Var]bool{}
	for v := range bound {
		hostBound[v] = true
	}
	body, changed, err := l.clause("the query", q.Goal, hostBound, countsBindings(q.Select, q.Having))
	if err != nil {
		return Query{}, err
	}
	if changed {
		if len(out.Select) == 0 {
			out.Select = defaultSelect(q.Goal) // the answer's columns are the written goal's
		}
		out.Goal = body
	}
	return out, nil
}

type inputLowerer struct {
	ins   map[string]Rule
	reg   *ns.Vocabulary
	fresh int
}

// call is one inlined call, to check once its clause is lowered.
type call struct {
	rel  string
	args map[Var]Term // the rule's input variables, as the call passes them
}

// clause lowers one body, then requires each call to have bound its inputs: by a constant, or by a
// variable the lowered body binds (bindsFrom: the inlined body never binds an input, and a generator
// binds only once a mode is satisfied), or one the host binds.
func (l *inputLowerer) clause(where string, b Body, hostBound map[Var]bool, counts bool) (Body, bool, error) {
	var calls []call
	lits, err := l.body(where, b.Literals, &calls, counts)
	if err != nil || len(calls) == 0 {
		return b, false, err
	}
	out := Body{Literals: lits}
	bound := map[Var]bool{}
	for v := range hostBound {
		bound[v] = true
	}
	bindsFrom(out.Literals, bound, "", nil, l.reg)
	for _, c := range calls {
		for _, in := range inputsOf(l.ins[c.rel], l.ins, l.reg) {
			t := c.args[in]
			if t.Const == nil && (t.Var == "" || t.Var == "_" || !bound[t.Var]) {
				return Body{}, false, checkErr(KindUnbound, fmt.Sprintf("query: %s's ?%s is an input argument, and the call in %s doesn't bind it", displayName(c.rel), in, where)).call(c.rel, len(c.args)).about(in)
			}
		}
	}
	return out, true, nil
}

// body inlines the calls among lits, and the calls inside what it inlines. It ends, since a recursive
// relation is never an input relation (inputRelations).
func (l *inputLowerer) body(where string, lits []Literal, calls *[]call, counts bool) ([]Literal, error) {
	var out []Literal
	for _, lit := range lits {
		switch {
		case lit.Neg != nil && l.isInput(lit.Neg.Relation):
			return nil, checkErr(KindDefinition, fmt.Sprintf("query: %s takes an argument from its caller, so %s can't negate it", displayName(lit.Neg.Relation), where)).call(lit.Neg.Relation, len(lit.Neg.Args))
		case lit.Pos == nil || !l.isInput(lit.Pos.Relation):
			out = append(out, lit)
			continue
		}
		rel := lit.Pos.Relation
		r := l.ins[rel]
		if counts && projects(r) {
			return nil, checkErr(KindAggregate, fmt.Sprintf("query: %s counts its bindings, and %s, which takes an argument from its caller, binds variables its head drops, so the count would include them; count(distinct ...) counts its tuples", where, displayName(rel))).call(rel, len(lit.Pos.Args))
		}
		inlined, c := l.inline(r, *lit.Pos)
		*calls = append(*calls, c)
		inner, err := l.body(where, inlined, calls, counts)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

func (l *inputLowerer) isInput(rel string) bool {
	_, ok := l.ins[rel]
	return ok
}

// inline is r's body with its head's variables renamed to the call's arguments and its other
// variables to fresh ones. It runs before tagWritten, so the caller's lowered body is what a witness
// numbers.
func (l *inputLowerer) inline(r Rule, a Atom) ([]Literal, call) {
	l.fresh++
	sub := map[Var]Term{}
	for i, h := range r.Head.Args {
		c := a.Args[i]
		if c.Var == "_" {
			c = Term{Var: Var(fmt.Sprintf("\x00in%d_%s", l.fresh, h.Var))}
		}
		sub[h.Var] = c
	}
	c := call{rel: r.Head.Relation, args: map[Var]Term{}}
	for _, in := range inputsOf(r, l.ins, l.reg) {
		c.args[in] = a.Args[headIndexOf(r.Head, in)]
	}
	rename := func(t Term) Term {
		if t.Var == "" || t.Var == "_" {
			return t
		}
		if s, ok := sub[t.Var]; ok {
			return s
		}
		s := Term{Var: Var(fmt.Sprintf("\x00in%d_%s", l.fresh, t.Var))}
		sub[t.Var] = s
		return s
	}
	atom := func(x Atom) *Atom {
		out := Atom{Relation: x.Relation, Args: make([]Term, len(x.Args))}
		for i, t := range x.Args {
			out.Args[i] = rename(t)
		}
		return &out
	}
	var lits []Literal
	for _, lit := range r.Body.Literals {
		switch {
		case lit.Pos != nil:
			lits = append(lits, Literal{Pos: atom(*lit.Pos), computed: lit.computed})
		case lit.Neg != nil:
			lits = append(lits, Literal{Neg: atom(*lit.Neg)})
		case lit.Compare != nil:
			cmp := *lit.Compare
			cmp.Left, cmp.Right = rename(cmp.Left), rename(cmp.Right)
			lits = append(lits, Literal{Compare: &cmp})
		}
	}
	return lits, c
}

func headIndexOf(head Atom, v Var) int {
	for i, t := range head.Args {
		if t.Var == v {
			return i
		}
	}
	return -1
}
