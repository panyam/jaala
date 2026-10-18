package datalog

import (
	"sort"
	"strconv"
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
	// 5 mV bound is 0.005 V, as one bound value is (foldUnits, #205): a host's bound value is a query's
	// constant, folded whether or not the vocabulary folds a Source's values (#303).
	return b.reg.Units().FoldValue(stored), nil
}

func mentions(a *Atom, v Var) bool {
	for _, t := range a.Args {
		if t.Var == v {
			return true
		}
	}
	return false
}

// bindEqualities binds each goal variable an equality fixes to a text constant, `?h = "X"` or
// `"X" = ?h`, through a relation holding that one value, read first as a set-bound variable's is
// (#370). Agents write the constant last, `go.covers(?t, ?h), ?h = "X"`, and the planner then ran
// go.covers with ?h unbound and filtered afterwards: coverage for every test, where the constant
// written in place walked back from one handler. Bound first, ?h reaches demand, pushdown and a
// generator's mode as the constant in place does.
//
// The equality stays, and holds of every row. The relation holds the comparison's own constant,
// coerced as the comparison reads it, and each literal meets it through unify, the strict equality
// the comparison applies, so no answer changes. A number is left alone: a query's number only
// filters and never lends its spelling or unit to an answer (#227), and bound first it would. Only
// the planned SemiNaive rewrites, so Naive stays the reference it's compared against.
func bindEqualities(q Query) Query {
	var guards []Literal
	var facts []Rule
	named := map[string]int{}
	for _, l := range q.Goal.Literals {
		v, c, ok := equalsText(l)
		if !ok {
			continue
		}
		rel := bindPrefix + "=" + string(v)
		if named[rel]++; named[rel] > 1 {
			rel += "#" + strconv.Itoa(named[rel])
		}
		facts = append(facts, Rule{Head: Atom{Relation: rel, Args: []Term{{Const: c}}}})
		guards = append(guards, Literal{Pos: &Atom{Relation: rel, Args: []Term{{Var: v}}}})
	}
	if len(guards) == 0 {
		return q
	}
	out := q
	out.Rules = append(append([]Rule(nil), q.Rules...), facts...)
	out.Goal = Body{Literals: append(guards, q.Goal.Literals...)}
	return out
}

// equalities are the goal variables bindEqualities bound, each to its one value: a goal literal over
// a \x00b:= relation, whose one rule is the value's fact.
func equalities(goal Body, byHead map[string][]Rule) map[Var]*ns.Value {
	var out map[Var]*ns.Value
	for _, l := range goal.Literals {
		if l.Pos == nil || !strings.HasPrefix(l.Pos.Relation, bindPrefix+"=") || len(l.Pos.Args) != 1 || l.Pos.Args[0].Var == "" {
			continue
		}
		rules := byHead[l.Pos.Relation]
		if len(rules) != 1 || len(rules[0].Body.Literals) != 0 || rules[0].Head.Args[0].Const == nil {
			continue
		}
		if out == nil {
			out = map[Var]*ns.Value{}
		}
		out[l.Pos.Args[0].Var] = rules[0].Head.Args[0].Const
	}
	return out
}

// equalsText is the variable and text constant of an equality between them, either way round.
func equalsText(l Literal) (Var, *ns.Value, bool) {
	c := l.Compare
	if c == nil || c.Op != "=" {
		return "", nil, false
	}
	v, k := c.Left, c.Right
	if v.Var == "" {
		v, k = k, v
	}
	if v.Var == "" || v.Var == "_" || strings.ContainsRune(string(v.Var), 0) || v.Expr != nil || k.Const == nil || k.Const.Num != nil || k.Const.Absent {
		return "", nil, false
	}
	return v.Var, k.Const, true
}
