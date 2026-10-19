package datalog

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/panyam/jaala/ns"
)

// lowerOptionals rewrites every `optional { body }` (#282) into rules the evaluators already run, so
// Naive, SemiNaive, Soufflé's translation and every check after it see none. An optional sharing the
// variables S with the rest of its clause, and binding N, everything else its body binds, becomes
//
//	dom(S)    :- the clause's positive relations that don't read N    (no citations, as a body aggregate's)
//	has(S)    :- body                                                 (dom(S), body when body needs S bound)
//	opt(S, N) :- dom(S), body
//	opt(S, A) :- dom(S), not has(S)                                   (A: absent for each of N)
//
// and the literal becomes opt(S, N), in its written position. So a row of the clause holds whether
// body matches or not, once per match when it does, and with N absent when it doesn't: Cypher's
// OPTIONAL MATCH, and Declaire's x10 without its second rule and "" sentinel. What absent means in a
// comparison and an aggregate is jaala's, as for any absent value; a front end with other rules for
// null (Cypher's) writes them into the program it compiles.
//
// The body reads only relations complete before the clause's own, as `not` does, since its empty
// clause negates it. has reads the domain only when body can't bind S itself (a generator in it needs
// them, as x10's loc.innermost does): the domain reads the clause's relations, so a recursive clause's
// optional would otherwise recurse through has's negation (the corpus found it). It runs after lowerBodyAggregates' sibling in every place that one runs, and
// before it, so a body aggregate inside the braces is lowered in the rule the braces become.
func lowerOptionals(q Query, scope string, reg *ns.Vocabulary) (Query, error) {
	if !hasOptionalIn(q) {
		return q, nil
	}
	o := &optLowerer{scope: scope, reg: reg}
	out := q
	out.Rules = nil
	for _, r := range q.Rules {
		body, err := o.body(r.Body, r.Head.Args)
		if err != nil {
			return Query{}, err
		}
		if body == nil {
			out.Rules = append(out.Rules, r)
			continue
		}
		lowered := r
		if lowered.text == "" {
			lowered.text = r.String()
		}
		lowered.Body = *body
		out.Rules = append(out.Rules, lowered)
	}
	outside := slices.Clone(q.Select)
	for _, h := range q.Having {
		outside = append(outside, h.Left, h.Right)
	}
	body, err := o.body(q.Goal, outside)
	if err != nil {
		return Query{}, err
	}
	if body != nil {
		out.Goal = *body
	}
	out.Rules = append(out.Rules, o.rules...)
	return out, nil
}

// lowerOptionalRules is lowerOptionals for rules alone, a module's.
func lowerOptionalRules(rules []Rule, scope string, reg *ns.Vocabulary) ([]Rule, error) {
	q, err := lowerOptionals(Query{Rules: rules}, scope, reg)
	return q.Rules, err
}

func hasOptionalIn(q Query) bool {
	if hasOptional(q.Goal) {
		return true
	}
	return slices.ContainsFunc(q.Rules, func(r Rule) bool { return hasOptional(r.Body) })
}

func hasOptional(b Body) bool {
	return slices.ContainsFunc(b.Literals, func(l Literal) bool { return l.Opt != nil })
}

type optLowerer struct {
	scope string
	reg   *ns.Vocabulary
	n     int
	rules []Rule
}

// body lowers a clause's optionals, or returns nil when it has none. outside is what else the clause
// names: its head, or the goal's projection.
func (o *optLowerer) body(b Body, outside []Term) (*Body, error) {
	if !hasOptional(b) {
		return nil, nil
	}
	out := Body{Literals: slices.Clone(b.Literals)}
	for i := range out.Literals {
		if out.Literals[i].Opt == nil {
			continue
		}
		lit, err := o.optional(out.Literals, i, outside)
		if err != nil {
			return nil, err
		}
		out.Literals[i] = lit
	}
	return &out, nil
}

// optional writes the rules of lits[i], an optional, and returns the literal that reads them. The
// optionals before it are already lowered, so a later optional's domain reads an earlier one's
// relation, as one OPTIONAL MATCH follows another.
func (o *optLowerer) optional(lits []Literal, i int, outside []Term) (Literal, error) {
	opt := lits[i].Opt
	text := lits[i].String()
	if hasOptional(*opt) {
		return Literal{}, checkErr(KindDefinition, fmt.Sprintf("query: %s holds another optional in its braces: write them one after the other", text))
	}
	// What the rest of the clause names, and what a positive relation there binds.
	elsewhere, outsideBound := map[Var]bool{}, map[Var]bool{}
	for j, l := range lits {
		if j == i {
			continue
		}
		for _, v := range optVars(l) {
			elsewhere[v] = true
		}
		switch {
		case l.Pos != nil && o.binds(l.Pos):
			bindAll(l.Pos, outsideBound)
		case l.Agg != nil:
			outsideBound[l.Agg.Result] = true
		}
	}
	for _, t := range outside {
		for _, v := range termVars(t) {
			elsewhere[v] = true
		}
	}
	inner, innerBound, ownBound := []Var{}, map[Var]bool{}, map[Var]bool{}
	for _, l := range opt.Literals {
		for _, v := range optVars(l) {
			if !slices.Contains(inner, v) {
				inner = append(inner, v)
			}
		}
		switch {
		case l.Pos != nil && o.binds(l.Pos):
			bindAll(l.Pos, innerBound)
			if _, pred := o.reg.Predicate(l.Pos.Relation); !pred && walkCalled(l.Pos.Relation) == nil {
				bindAll(l.Pos, ownBound) // a relation binds its variables with nothing bound first
			}
		case l.Agg != nil:
			innerBound[l.Agg.Result] = true
		case l.Compare != nil && l.Compare.Op == "=":
			// ?y = expr computes into ?y (#5), so it binds it, as a function call would
			for _, side := range [][2]Term{{l.Compare.Left, l.Compare.Right}, {l.Compare.Right, l.Compare.Left}} {
				if side[0].Var != "" && side[1].Expr != nil {
					innerBound[side[0].Var] = true
				}
			}
		}
	}
	// What the braces bind they all hand on, read elsewhere or not, so the clause has a row per match
	// as a join does, which a count of its bindings sees (a variable only one literal names is still a
	// binding of a join).
	var shared, binds []Var
	for _, v := range inner {
		switch {
		case elsewhere[v] && outsideBound[v]:
			shared = append(shared, v)
		case innerBound[v]:
			binds = append(binds, v)
		case elsewhere[v]:
			return Literal{}, checkErr(KindUnbound, fmt.Sprintf("query: %s reads ?%s, which no relation binds: one inside the braces or outside them must", text, v)).about(v)
		}
	}
	// The domain: the positive relations outside that don't read what the braces bind.
	var dom []Literal
	domBound := map[Var]bool{}
	for j, l := range lits {
		if j == i || l.Pos == nil || slices.ContainsFunc(binds, func(v Var) bool { return slices.Contains(literalVars(l), v) }) {
			continue
		}
		dom = append(dom, Literal{Pos: l.Pos})
		if o.binds(l.Pos) {
			bindAll(l.Pos, domBound)
		}
	}
	if len(dom) == 0 {
		return Literal{}, checkErr(KindDefinition, fmt.Sprintf("query: %s needs a relation outside its braces to match first: optional keeps a row the rest of its clause matched", text))
	}
	for _, v := range shared {
		if !domBound[v] {
			return Literal{}, checkErr(KindUnbound, fmt.Sprintf("query: %s shares ?%s with the rest of its clause, so a relation outside the braces that reads nothing they bind must bind it", text, v)).about(v)
		}
	}
	o.n++
	name := func(kind string) string {
		n := "optional" + aggSep + kind + ":" + strconv.Itoa(o.n)
		if o.scope != "" {
			n += "@" + o.scope
		}
		return n
	}
	keys := make([]Term, len(shared))
	for k, v := range shared {
		keys[k] = Term{Var: v}
	}
	domain, has, val := name("d"), name("h"), name("o")
	domAtom := &Atom{Relation: domain, Args: keys}
	vals := slices.Clone(keys)
	absents := slices.Clone(keys)
	for _, v := range binds {
		vals = append(vals, Term{Var: v})
		absent := ns.Absent()
		absents = append(absents, Term{Const: &absent})
	}
	matched := append([]Literal{{Pos: domAtom}}, opt.Literals...)
	tested := matched
	if !slices.ContainsFunc(shared, func(v Var) bool { return !ownBound[v] }) {
		tested = opt.Literals
	}
	o.rules = append(o.rules,
		Rule{Head: *domAtom, Body: Body{Literals: dom}, text: text},
		Rule{Head: Atom{Relation: has, Args: keys}, Body: Body{Literals: tested}, text: text},
		Rule{Head: Atom{Relation: val, Args: vals}, Body: Body{Literals: matched}, text: text},
		Rule{Head: Atom{Relation: val, Args: absents}, Body: Body{Literals: []Literal{
			{Pos: domAtom},
			{Neg: &Atom{Relation: has, Args: keys}},
		}}, text: text},
	)
	return Literal{Pos: &Atom{Relation: val, Args: vals}}, nil
}

// optVars is every variable a literal names, an expression's in a comparison included, which
// literalVars leaves out: a second optional computing from what the first binds shares it.
func optVars(l Literal) []Var {
	out := literalVars(l)
	if l.Compare != nil {
		for _, t := range []Term{l.Compare.Left, l.Compare.Right} {
			for _, v := range termVars(t) {
				if v != "_" && !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// binds reports whether a call binds its variables: a relation or a generator does, a filter such as
// absent(?w) only tests what something else bound.
func (o *optLowerer) binds(a *Atom) bool {
	b, pred := o.reg.Predicate(a.Relation)
	return !pred || b.IsGenerator()
}

// termVars is every variable a term names: itself, an aggregate's, or an expression's.
func termVars(t Term) []Var {
	switch {
	case t.Var != "":
		return []Var{t.Var}
	case t.Agg != nil:
		return []Var{t.Agg.Var}
	case t.Expr != nil:
		var out []Var
		for _, a := range t.Expr.Args {
			out = append(out, termVars(a)...)
		}
		return out
	}
	return nil
}
