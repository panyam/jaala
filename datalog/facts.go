package datalog

import (
	"fmt"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A base relation's facts can be terms (#279): `edge(?a, ?b) as ?r` binds ?r to the fact the atom
// matched, its Tuple.ID or else its relation and values, so a query returns it, compares two with !=
// and joins on it, as Cypher reads a relationship's identity. lowerFacts writes each such atom as a
// call to the relation's fact relation, \x00f:edge(?a, ?b, ?r), whose last argument is the id, before
// any check or rewrite reads the program. Everything after sees an ordinary base relation, which the
// Base reads as the relation itself with each tuple's id after its values (factTuples): planned,
// indexed (by the id too) and looked up as any other, though never pushed, walked or summarized
// (isFact), since a store knows nothing of it.

// factPrefix starts a fact relation's name.
const factPrefix = "\x00f:"

// factRelation is the fact relation of base relation rel.
func factRelation(rel string) string { return factPrefix + rel }

// factOf is the base relation whose fact relation name is, and whether it is one.
func factOf(name string) (string, bool) { return strings.CutPrefix(name, factPrefix) }

// isFact reports whether rel is a fact relation.
func isFact(rel string) bool { return strings.HasPrefix(rel, factPrefix) }

// schemaIn is rel's schema in reg: a base relation's, or for a fact relation its relation's with one
// more argument, the id, labelled "fact" and read as text so an id that looks like a number stays one
// text.
func schemaIn(reg *ns.Vocabulary, rel string) (ns.Schema, bool) {
	base, ok := factOf(rel)
	if !ok {
		return reg.Schema(rel)
	}
	s, ok := reg.Schema(base)
	if !ok {
		return ns.Schema{}, false
	}
	labels := make([]string, s.Arity, s.Arity+1)
	copy(labels, s.Labels)
	types := make([]ns.ArgType, s.Arity, s.Arity+1)
	copy(types, s.Types)
	s.Labels = append(labels, "fact")
	s.Types = append(types, ns.ArgType{Type: ns.TypeString})
	s.Arity++
	return s, true
}

// factTuples is rel's tuples as its fact relation holds them, each with its id after its values.
// tuples are the relation's as the Base read them, so a default id spells its numbers canonically.
func factTuples(rel string, tuples []ns.Tuple) []ns.Tuple {
	out := make([]ns.Tuple, len(tuples))
	for i, t := range tuples {
		out[i] = factTuple(rel, t)
	}
	return out
}

func factTuple(rel string, t ns.Tuple) ns.Tuple {
	vals := make([]ns.Value, len(t.Vals), len(t.Vals)+1)
	copy(vals, t.Vals)
	return ns.Tuple{Vals: append(vals, ns.S(factID(rel, t))), Cites: t.Cites, ID: t.ID}
}

// factID is t's id: the Source's, or else rel and t's values as a query writes them, edge("a", 3).
func factID(rel string, t ns.Tuple) string {
	if t.ID != "" {
		return t.ID
	}
	parts := make([]string, len(t.Vals))
	for i, v := range t.Vals {
		switch {
		case v.Absent:
			parts[i] = "absent"
		case v.Num != nil:
			parts[i] = v.S
			if v.BaseUnit != "" && !strings.HasSuffix(v.S, v.BaseUnit) {
				parts[i] += v.BaseUnit
			}
		default:
			parts[i] = `"` + v.S + `"`
		}
	}
	return rel + "(" + strings.Join(parts, ", ") + ")"
}

// lowerFacts writes each atom binding its fact as a call to its relation's fact relation, with the
// fact's variable as one more argument. It refuses a fact bound on a negated atom, a predicate or a
// derived relation, which match no stored fact; an atom calling an unknown relation, or at the wrong
// arity, is left as it is for the checks after it to report as they would without its `as`.
func lowerFacts(q Query, reg *ns.Vocabulary) (Query, error) {
	derived := map[string]bool{}
	for _, r := range q.Rules {
		derived[r.Head.Relation] = true
	}
	var err error
	if q.Goal, err = lowerFactBody(q.Goal, reg, derived); err != nil {
		return q, err
	}
	if q.Rules, err = lowerFactsIn(q.Rules, reg, derived); err != nil {
		return q, err
	}
	return q, nil
}

// lowerFactRules is lowerFacts for rules alone, a module's, whose own heads are derived.
func lowerFactRules(rules []Rule, reg *ns.Vocabulary) ([]Rule, error) {
	q, err := lowerFacts(Query{Rules: rules}, reg)
	return q.Rules, err
}

func lowerFactsIn(rules []Rule, reg *ns.Vocabulary, derived map[string]bool) ([]Rule, error) {
	var out []Rule
	for i, r := range rules {
		body, err := lowerFactBody(r.Body, reg, derived)
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

func sameBody(a, b Body) bool {
	return len(a.Literals) == len(b.Literals) && (len(a.Literals) == 0 || &a.Literals[0] == &b.Literals[0])
}

func lowerFactBody(b Body, reg *ns.Vocabulary, derived map[string]bool) (Body, error) {
	var out []Literal
	for i, l := range b.Literals {
		lit, err := lowerFactLiteral(l, reg, derived)
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

func lowerFactLiteral(l Literal, reg *ns.Vocabulary, derived map[string]bool) (Literal, error) {
	switch {
	case l.Agg != nil:
		body, err := lowerFactBody(l.Agg.Body, reg, derived)
		if err != nil || sameBody(body, l.Agg.Body) {
			return l, err
		}
		agg := *l.Agg
		agg.Body = body
		l.Agg = &agg
		return l, nil
	case l.Neg != nil && l.Neg.Fact != "":
		return l, factErr(l.Neg, fmt.Sprintf("query: not %s as ?%s names no fact: a negated atom matched none", l.Neg.Relation, l.Neg.Fact), "drop `as ?"+string(l.Neg.Fact)+"`")
	case l.Pos == nil || l.Pos.Fact == "":
		return l, nil
	}
	a := *l.Pos
	switch _, isPred := reg.Predicate(a.Relation); {
	case isPred:
		return l, factErr(&a, fmt.Sprintf("query: %s is a predicate, whose matches are no stored facts, so \"as ?%s\" names none", a.Relation, a.Fact), "drop `as ?"+string(a.Fact)+"`")
	case derived[a.Relation] || (reg.Has(a.Relation) && !isBase(reg, a.Relation)):
		return l, factErr(&a, fmt.Sprintf("query: %s is derived, and only a base relation's facts have ids, so \"as ?%s\" names none", displayName(a.Relation), a.Fact), "bind the facts of the base relations its rules read instead")
	}
	s, ok := reg.Schema(a.Relation)
	if !ok || s.Arity != len(a.Args) {
		return l, nil
	}
	a.Args = append(append([]Term(nil), a.Args...), Term{Var: a.Fact})
	a.Relation, a.Fact = factRelation(a.Relation), ""
	l.Pos = &a
	return l, nil
}

func isBase(reg *ns.Vocabulary, rel string) bool {
	_, ok := reg.Schema(rel)
	return ok
}

// factErr is the CheckError for an atom whose `as` names no fact.
func factErr(a *Atom, msg, fix string) *CheckError {
	return checkErr(KindFact, msg).call(a.Relation, len(a.Args)).about(a.Fact).fix(fix)
}
