package datalog

import "strings"

// String renders the term as query text: ?x, _, "a string", 42, or an aggregate such as
// count(distinct ?x).
func (t Term) String() string {
	switch {
	case t.Agg != nil:
		d := ""
		if t.Agg.Distinct {
			d = "distinct "
		}
		return t.Agg.Func + "(" + d + "?" + string(t.Agg.Var) + ")"
	case t.Var == "_":
		return "_"
	case t.Var != "":
		return "?" + string(t.Var)
	case t.Expr != nil:
		return t.Expr.String()
	case t.Const != nil && t.Const.Num != nil && t.Const.BaseUnit != "" && !strings.HasSuffix(t.Const.S, t.Const.BaseUnit):
		return t.Const.S + t.Const.BaseUnit
	case t.Const != nil && t.Const.Num != nil:
		return t.Const.S
	case t.Const != nil:
		return `"` + t.Const.S + `"`
	}
	return ""
}

// String renders the atom as query text: rel(?a, "b").
func (a Atom) String() string {
	args := make([]string, len(a.Args))
	for i, t := range a.Args {
		args[i] = t.String()
	}
	rel := a.Relation
	if isWalk(rel) {
		rel = walkName(rel) // its relation is its first argument
	}
	s := rel + "(" + strings.Join(args, ", ") + ")"
	if a.Fact != "" {
		s += " as ?" + string(a.Fact)
	}
	return s
}

// String renders the literal as query text: an atom, `not` an atom, a comparison, or an aggregate
// over a body of its own.
func (l Literal) String() string {
	switch {
	case l.Agg != nil:
		return "?" + string(l.Agg.Result) + " = " + (Term{Agg: &l.Agg.Agg}).String() + " : { " + l.Agg.Body.String() + " }"
	case l.Opt != nil:
		return "optional { " + l.Opt.String() + " }"
	case l.Pos != nil:
		return l.Pos.String()
	case l.Neg != nil:
		return "not " + l.Neg.String()
	case l.Compare != nil:
		return l.Compare.Left.String() + " " + l.Compare.Op + " " + l.Compare.Right.String()
	}
	return ""
}

// String renders the body as query text, its literals joined by ", ".
func (b Body) String() string {
	lits := make([]string, len(b.Literals))
	for i, l := range b.Literals {
		lits[i] = l.String()
	}
	return strings.Join(lits, ", ")
}

// String renders the rule as query text, head types included: `x(?n: net) :- y(?n)`. Parse reads
// it back as the same rule.
func (r Rule) String() string {
	args := make([]string, len(r.Head.Args))
	for i, t := range r.Head.Args {
		args[i] = t.String()
		if i < len(r.HeadTypes) && !r.HeadTypes[i].IsZero() {
			args[i] += ": " + r.HeadTypes[i].String()
		}
	}
	return r.Head.Relation + "(" + strings.Join(args, ", ") + ") :- " + r.Body.String()
}
