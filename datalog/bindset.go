package datalog

import (
	"sort"
	"strings"

	"github.com/panyam/jaala/ns"
)

// bindPrefix names the relation a set-bound variable ranges over (see bindSets).
const bindPrefix = "\x00b:"

func isBindSet(rel string) bool { return strings.HasPrefix(rel, bindPrefix) }

// bindSets turns each goal variable the host bound to several values, or to none, into a join with a
// relation holding exactly those values (#132). The relation is a set of facts, read first in the
// goal, so the answer is the union over the values and an aggregate reduces across them; the planner
// keeps it first (planGoal) and the demand rewrite starts from it like a guard, so a derived relation
// the goal calls with the variable is evaluated only for the values bound. A variable bound to no
// values ranges over a relation that derives nothing, from a rule reading only itself.
//
// It runs on the linked, coerced goal, after the checks made on the program as written, so its
// relation is never named in an error, and its literal has no written position, so a witness leaves it
// out. Each value is checked as bindGoal and coerceConstants check a single one: substituted into the
// goal, coerced, and checked against a closed Domain, so an error names the value refused. It is stored
// as a number when any place it stands in reads it as one, an argument or a comparison with a number.
func (b *Base) bindSets(q Query, bind map[Var][]ns.Value) (Query, error) {
	var vars []Var
	for v, vals := range bind {
		if len(vals) != 1 {
			vars = append(vars, v)
		}
	}
	if len(vars) == 0 {
		return q, nil
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i] < vars[j] })
	t := newTyper(b.reg, q.Rules)
	out := q
	out.Rules = append([]Rule(nil), q.Rules...)
	var guards []Literal
	for _, v := range vars {
		rel := bindPrefix + string(v)
		for _, val := range bind[v] {
			c, err := b.checkBoundValue(t, q.Goal, v, val)
			if err != nil {
				return q, err
			}
			out.Rules = append(out.Rules, Rule{Head: Atom{Relation: rel, Args: []Term{{Const: &c}}}})
		}
		self := Atom{Relation: rel, Args: []Term{{Var: v}}}
		if len(bind[v]) == 0 {
			out.Rules = append(out.Rules, Rule{Head: self, Body: Body{Literals: []Literal{{Pos: &self}}}})
		}
		guards = append(guards, Literal{Pos: &self})
	}
	out.Goal = Body{Literals: append(guards, q.Goal.Literals...)}
	return out, nil
}

// checkBoundValue checks one value of a set-bound variable as a constant in its place in the goal
// would be: coerced to each argument's type, and against each argument's closed Domain. It returns the
// value to store, read as a number if any of its places coerced it to one, else as text if one read it
// as text, since a relation's values there are text (normalizeTuples) and text never equals a number.
func (b *Base) checkBoundValue(t *typer, goal Body, v Var, val ns.Value) (ns.Value, error) {
	one := substBody(goal, func(term Term) Term {
		if term.Var == v {
			c := val
			return Term{Const: &c}
		}
		return term
	})
	one, err := t.coerceBody(one)
	if err != nil {
		return val, err
	}
	stored, asNumber := val, false
	for i, l := range one.Literals {
		for j, term := range literalTerms(goal.Literals[i]) {
			c := literalTerms(l)[j].Const
			if term.Var != v || c == nil {
				continue
			}
			switch {
			case c.Num != nil:
				stored, asNumber = *c, true
			case !asNumber:
				stored = *c // read as text, as a relation's values in that argument are (normalizeTuples)
			}
		}
		a, written := l.Pos, goal.Literals[i].Pos
		if a == nil {
			a, written = l.Neg, goal.Literals[i].Neg
		}
		if a == nil || !mentions(written, v) {
			continue
		}
		s, ok := b.schemaOf(a.Relation)
		if !ok {
			s, ok = b.derivedSchema(a.Relation)
		}
		if ok && len(a.Args) == s.Arity {
			if err := b.checkArgValues(a, s); err != nil {
				return val, err
			}
		}
	}
	return b.units().FoldValue(stored), nil // 5 mV bound is 0.005 V, as one bound value is (foldUnits, #205)
}

func mentions(a *Atom, v Var) bool {
	for _, t := range a.Args {
		if t.Var == v {
			return true
		}
	}
	return false
}
