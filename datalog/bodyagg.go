package datalog

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// aggSep starts the part of a body aggregate's relation names that keeps them apart (see
// lowerBodyAggregates). What comes before it is the aggregate as written, `count(?n)`, so a witness
// node and an error show that.
const aggSep = "\x00agg"

// isAggHelper reports any relation lowerBodyAggregates writes.
func isAggHelper(rel string) bool { return strings.Contains(rel, aggSep) }

// isAggDomain reports a relation of the values a body aggregate's shared variables take outside its
// braces. Like a magic relation it records what was asked, not evidence, so its tuples carry no
// citations and no witness.
func isAggDomain(rel string) bool { return strings.Contains(rel, aggSep+"d:") }

// isAggReduce reports a body aggregate's reduction, a rule head aggregate over its braces. Its tuples
// keep their citations but carry no witness, as an aggregate answer row has none.
func isAggReduce(rel string) bool { return strings.Contains(rel, aggSep+"a:") }

// lowerBodyAggregates rewrites every body aggregate (#71) into rules the evaluators already run, so
// Naive, SemiNaive and every check after it see none. For `?v = f(?x) : { body }`, sharing the
// variables S with the rest of its clause:
//
//	dom(S)     :- the clause's positive relations that don't name ?v    (no citations)
//	red(S, f(?x)) :- body                                               (a rule head aggregate)
//	val(S, ?v) :- red(S, ?v)
//	val(S, e)  :- dom(S), not red(S, _)                                 (e: 0, absent or "", #122)
//
// and the literal becomes val(S, ?v), in its written position. The reduction's body edges are strict
// (it aggregates), so the braces read only what is complete before the clause. With nothing shared,
// red already answers one row over nothing, and dom and the zero clause aren't needed. scope keeps
// two lowerings' names apart: a module's rules are lowered on their own.
//
// The clause keeps its written text, and val's rules carry the literal's, so a witness reads as
// written: the literal's node is val's tuple, named by the aggregate, with no children.
func lowerBodyAggregates(q Query, scope string) (Query, error) {
	l := &lowerer{scope: scope}
	out := q
	out.Rules = nil
	for _, r := range q.Rules {
		body, err := l.body(r.Body, r.Head.Args)
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
	body, err := l.body(q.Goal, q.Select)
	if err != nil {
		return Query{}, err
	}
	if body != nil {
		out.Goal = *body
	}
	out.Rules = append(out.Rules, l.rules...)
	return out, nil
}

// lowerRules is lowerBodyAggregates for rules alone, a module's.
func lowerRules(rules []Rule, scope string) ([]Rule, error) {
	if !slices.ContainsFunc(rules, func(r Rule) bool { return hasBodyAggregate(r.Body) }) {
		return rules, nil
	}
	l := &lowerer{scope: scope}
	var out []Rule
	for _, r := range rules {
		body, err := l.body(r.Body, r.Head.Args)
		if err != nil {
			return nil, err
		}
		if body != nil {
			r.text = r.String()
			r.Body = *body
		}
		out = append(out, r)
	}
	return append(out, l.rules...), nil
}

func hasBodyAggregate(b Body) bool {
	return slices.ContainsFunc(b.Literals, func(l Literal) bool { return l.Agg != nil })
}

type lowerer struct {
	scope string
	n     int
	rules []Rule
}

// body lowers a clause's body aggregates, or returns nil when it has none. outside is what else the
// clause names: its head, or the goal's projection.
func (l *lowerer) body(b Body, outside []Term) (*Body, error) {
	if !hasBodyAggregate(b) {
		return nil, nil
	}
	results := map[Var]bool{}
	for _, lit := range b.Literals {
		if lit.Agg != nil {
			if results[lit.Agg.Result] {
				return nil, fmt.Errorf("query: two aggregates bind ?%s", lit.Agg.Result)
			}
			results[lit.Agg.Result] = true
		}
	}
	// The domain: the positive relations outside the braces that don't need an aggregate's value.
	var dom []Literal
	bound := map[Var]bool{}
	for _, lit := range b.Literals {
		if lit.Pos != nil && !slices.ContainsFunc(lit.Pos.Args, func(t Term) bool { return results[t.Var] }) {
			dom = append(dom, Literal{Pos: lit.Pos})
			bindAll(lit.Pos, bound)
		}
	}
	out := Body{Literals: make([]Literal, len(b.Literals))}
	for i, lit := range b.Literals {
		if lit.Agg == nil {
			out.Literals[i] = lit
			continue
		}
		// Every variable the clause writes outside any braces. A variable inside other braces is
		// theirs alone, as one inside these is.
		elsewhere := map[Var]bool{}
		for j, other := range b.Literals {
			switch {
			case j == i:
			case other.Agg != nil:
				elsewhere[other.Agg.Result] = true
			default:
				for _, v := range literalVars(other) {
					elsewhere[v] = true
				}
			}
		}
		for _, t := range outside {
			elsewhere[t.Var] = true
			if t.Agg != nil {
				elsewhere[t.Agg.Var] = true
			}
		}
		val, err := l.aggregate(*lit.Agg, elsewhere, bound, dom)
		if err != nil {
			return nil, err
		}
		out.Literals[i] = val
	}
	return &out, nil
}

// aggregate writes one body aggregate's rules and returns the literal that reads its value.
func (l *lowerer) aggregate(a BodyAggregate, elsewhere, bound map[Var]bool, dom []Literal) (Literal, error) {
	text := (Literal{Agg: &a}).String()
	if !validAggFunc(a.Agg.Func) {
		return Literal{}, fmt.Errorf("query: unknown aggregate %q (want count/min/max/sum/list)", a.Agg.Func)
	}
	inner := map[Var]bool{} // what a positive relation inside the braces binds
	var all []Var
	for _, lit := range a.Body.Literals {
		if lit.Agg != nil {
			return Literal{}, fmt.Errorf("query: %s holds another aggregate in its braces, which is not supported", text)
		}
		if lit.Pos != nil {
			bindAll(lit.Pos, inner)
		}
		for _, v := range literalVars(lit) {
			if !slices.Contains(all, v) {
				all = append(all, v)
			}
		}
	}
	if inner[a.Result] || slices.Contains(all, a.Result) {
		return Literal{}, fmt.Errorf("query: %s uses ?%s inside its own braces; the aggregate binds it", text, a.Result)
	}
	if !inner[a.Agg.Var] {
		return Literal{}, fmt.Errorf("query: %s aggregates ?%s, which no relation in its braces binds", text, a.Agg.Var)
	}
	var shared []Var
	for _, v := range all {
		if !elsewhere[v] {
			continue
		}
		if !bound[v] {
			return Literal{}, fmt.Errorf("query: %s shares ?%s with the rest of its body, so a relation outside the braces must bind it (one that doesn't need an aggregate's value)", text, v)
		}
		if !inner[v] {
			return Literal{}, fmt.Errorf("query: %s shares ?%s with the rest of its body, so a relation inside the braces must bind it too", text, v)
		}
		shared = append(shared, v)
	}
	l.n++
	name := func(kind string) string {
		n := (Term{Agg: &a.Agg}).String() + aggSep + kind + ":" + strconv.Itoa(l.n)
		if l.scope != "" {
			n += "@" + l.scope
		}
		return n
	}
	keys := make([]Term, len(shared))
	for i, v := range shared {
		keys[i] = Term{Var: v}
	}
	with := func(rel string, last Term) *Atom {
		return &Atom{Relation: rel, Args: append(slices.Clone(keys), last)}
	}
	red, val := name("a"), name("z")
	l.rules = append(l.rules, Rule{Head: *with(red, Term{Agg: &a.Agg}), Body: a.Body, text: text})
	var types []ns.ArgType
	if t, ok := aggregateFixed(a.Agg.Func); ok {
		types = make([]ns.ArgType, len(keys)+1)
		types[len(keys)] = t
	}
	result := Term{Var: a.Result}
	l.rules = append(l.rules, Rule{Head: *with(val, result), Body: Body{Literals: []Literal{{Pos: with(red, result)}}}, HeadTypes: types, text: text})
	if len(shared) > 0 {
		domain := name("d")
		l.rules = append(l.rules, Rule{Head: Atom{Relation: domain, Args: keys}, Body: Body{Literals: dom}, text: text})
		empty := emptyAggregate(a.Agg.Func)
		l.rules = append(l.rules, Rule{
			Head: *with(val, Term{Const: &empty}),
			Body: Body{Literals: []Literal{
				{Pos: &Atom{Relation: domain, Args: keys}},
				{Neg: with(red, Term{Var: "_"})},
			}},
			text: text,
		})
	}
	return Literal{Pos: with(val, result)}, nil
}

// aggregateFixed is the type an aggregate's function fixes whatever it reduces: count a number, list
// text. sum, min and max take their input's, which the typer works out.
func aggregateFixed(fn string) (ns.ArgType, bool) {
	switch fn {
	case "count":
		return ns.ArgType{Type: ns.TypeNumber}, true
	case "list":
		return ns.ArgType{Type: ns.TypeString}, true
	}
	return ns.ArgType{}, false
}

// emptyAggregate is what an aggregate answers over no bindings, as reduce does (#122).
func emptyAggregate(fn string) ns.Value {
	switch fn {
	case "count", "sum":
		return ns.N(0)
	case "list":
		return ns.S("")
	}
	return ns.Absent()
}

// literalVars is every variable a literal names, in order, inside a body aggregate's braces too.
func literalVars(l Literal) []Var {
	var out []Var
	add := func(v Var) {
		if v != "" && v != "_" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	switch {
	case l.Agg != nil:
		add(l.Agg.Result)
		for _, inner := range l.Agg.Body.Literals {
			for _, v := range literalVars(inner) {
				add(v)
			}
		}
	default:
		for _, t := range literalTerms(l) {
			add(t.Var)
		}
	}
	return out
}
