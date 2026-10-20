package datalog

import (
	"maps"
	"slices"

	"github.com/panyam/jaala/ns"
)

// lowerConstants makes ?k = "tag" bind ?k (#457): an equality of a variable no positive literal binds
// with a constant (either way round) binds the variable to it, where only an atom used to. In a rule
// the constant is written into the head and body in the variable's place, since a head takes
// constants and the body then reads it as one; in the goal, whose columns must stay variables, it is
// a one-value binding, as a host's Bind is, and bindGoal fills its column in on every row. A variable
// the host binds keeps its equality as a test, and so does one an aggregate reduces. It runs with the
// other lowerings, before any check reads the program, so every evaluator and Validate see the same
// rules. bind is the host's bindings; the goal's constants come back merged into a copy of them, and
// whether the goal selects one, which then groups an aggregate as a column does.
func lowerConstants(q Query, bind map[Var][]ns.Value) (Query, map[Var][]ns.Value, bool) {
	q = constantColumns(q)
	out := q
	out.Rules = slices.Clone(q.Rules)
	for i, r := range out.Rules {
		consts := constantEqualities(r.Body, func(v Var) bool { return reducedIn(r.Head.Args, v) })
		if len(consts) == 0 {
			continue
		}
		if r.text == "" {
			r.text = r.String()
		}
		sub := func(t Term) Term { return substConst(t, consts) }
		r.Head.Args = slices.Clone(r.Head.Args)
		for j, t := range r.Head.Args {
			r.Head.Args[j] = sub(t)
		}
		r.Body = substBody(r.Body, sub)
		out.Rules[i] = r
	}
	consts := constantEqualities(q.Goal, func(v Var) bool { return bind[v] != nil || reducedIn(q.Select, v) })
	if len(consts) == 0 {
		return out, bind, false
	}
	merged := maps.Clone(bind)
	if merged == nil {
		merged = map[Var][]ns.Value{}
	}
	for v, c := range consts {
		merged[v] = []ns.Value{c}
	}
	sel := q.Select
	if len(sel) == 0 {
		sel = defaultSelect(q.Goal)
	}
	grouped := slices.ContainsFunc(sel, func(t Term) bool { _, ok := consts[t.Var]; return ok && t.Agg == nil })
	return out, merged, grouped
}

// constantEqualities are the variables body binds only by an equality with a constant, each to the
// constant its first such equality names; kept refuses a variable to leave alone.
func constantEqualities(body Body, kept func(Var) bool) map[Var]ns.Value {
	bound := map[Var]bool{}
	for _, v := range positiveVars(body) {
		bound[v] = true
	}
	var out map[Var]ns.Value
	for _, l := range body.Literals {
		c := l.Compare
		if c == nil || c.Op != "=" {
			continue
		}
		v, k := c.Left, c.Right
		if v.Const != nil {
			v, k = k, v
		}
		if v.Var == "" || v.Var == "_" || v.Expr != nil || k.Const == nil || bound[v.Var] || kept(v.Var) {
			continue
		}
		if _, ok := out[v.Var]; ok {
			continue // a second constant tests against the first once it's written in
		}
		if out == nil {
			out = map[Var]ns.Value{}
		}
		out[v.Var] = *k.Const
	}
	return out
}

// reducedIn is whether an aggregate among terms reduces v, which a constant can't stand in for.
func reducedIn(terms []Term, v Var) bool {
	return slices.ContainsFunc(terms, func(t Term) bool { return t.Agg != nil && t.Agg.Var == v })
}

// substConst writes consts' values in place of their variables, inside an expression's arguments too.
func substConst(t Term, consts map[Var]ns.Value) Term {
	if c, ok := consts[t.Var]; ok && t.Var != "" {
		return Term{Const: &c}
	}
	if t.Expr != nil {
		e := *t.Expr
		e.Args = slices.Clone(e.Args)
		for i, a := range e.Args {
			e.Args[i] = substConst(a, consts)
		}
		t.Expr = &e
	}
	return t
}

// constantColumns writes a constant in the projection, => ?x, "tag", as a column named for it bound
// by an equality, which lowerConstants then reads as a binding: => ?x, ?"\"tag\"" with ?"\"tag\"" =
// "tag" in the goal, as computeColumns writes arithmetic.
func constantColumns(q Query) Query {
	if !slices.ContainsFunc(q.Select, func(t Term) bool { return t.Const != nil }) {
		return q
	}
	out := q
	out.Select = slices.Clone(q.Select)
	out.Goal.Literals = slices.Clone(q.Goal.Literals)
	for i, t := range out.Select {
		if t.Const != nil {
			v := Term{Var: Var(t.String())}
			out.Select[i] = v
			out.Goal.Literals = append(out.Goal.Literals, Literal{Compare: &Compare{Left: v, Op: "=", Right: t}})
		}
	}
	return out
}
