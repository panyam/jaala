package datalog

import (
	"sort"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Witness says how one fact of an answer holds: the relation and values it proves, and either the
// citations a Source or generator gave for it (a leaf) or the rule that derived it with the witnesses
// of that rule's body (a derived node). A Row's Witness lists one per goal literal, so an answer can be
// drawn as the tree of facts and rules behind it rather than a flat set of citations.
//
// Witnesses are recorded only when an Eval asks for them (see Witnesses), since keeping one per
// derived tuple costs memory a query that only wants answers should not pay.
type Witness struct {
	// Relation is the relation proved, as the program names it.
	Relation string
	// Values are the proved tuple's values. On a Negated node, an argument the negation did not bind
	// is the zero Value.
	Values []ns.Value
	// Rule is the rule that derived a derived node, as query text in its written form.
	Rule string
	// Negated marks a `not` literal that held: it proves an absence, so it has no citations and no
	// children.
	Negated bool
	// Cites are a leaf's citations in the order its Source or generator gave them, so a generator that
	// cites a path's steps in walk order keeps that order here (Row.Cites is the sorted set).
	Cites []string
	// Children are a derived node's body witnesses, one per literal that proves something (relations,
	// generators, filters and negations; not comparisons), in the order the rule is WRITTEN, whatever
	// order it was evaluated in.
	Children []*Witness
	// Versions are, for a witness an Across Eval answered (#180), the versions this node's fact holds
	// in, or this derivation was found for; nil otherwise. At gives the witness of one version.
	Versions ns.VersionSet
	// height is the number of rule steps above the facts, which CanonicalCites keeps shortest.
	height int
	// across and raw are, for a fact an Across Eval answered, its Source and its citations as made,
	// which At resolves to another version's.
	across *acrossSource
	raw    []string
}

// Witnesses asks an Eval to record how each answer was derived, in Row.Witness. Recording keeps the
// program's shape: SemiNaive does not inline relations into their callers for a witnessed Eval, so
// every derived relation the query reaches is its own node. Planning and demand still apply; their
// rewrites are invisible in the witness. An aggregate row has no witness, since a group merges many
// derivations.
func Witnesses() Option {
	return func(o *evalOptions) { o.witness = true }
}

// placed is a witness node and the written position of the literal it proves.
type placed struct {
	at   int
	node *Witness
}

// inWrittenOrder returns a binding's witness nodes ordered by the written position of their literals.
// A magic guard has no node (magic tuples record what was asked, not evidence, and are never given
// one), so it never appears, and nor do the values a set-bound variable ranges over (see bindSets).
func inWrittenOrder(ps []placed) []*Witness {
	kept := make([]placed, 0, len(ps))
	for _, p := range ps {
		if p.node != nil {
			kept = append(kept, p)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].at < kept[j].at })
	out := make([]*Witness, len(kept))
	for i, p := range kept {
		out[i] = p.node
	}
	return out
}

// tagWritten records each literal's written position, and each rule's written text, before any
// rewrite reorders or renames them, so a witness can follow the program as written.
func tagWritten(q Query) Query {
	out := q
	out.Rules = make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		if r.text == "" { // a lowered body aggregate's clause keeps its written text
			r.text = r.String()
		}
		r.Body = tagBody(r.Body)
		out.Rules[i] = r
	}
	out.Goal = tagBody(q.Goal)
	return out
}

func tagBody(b Body) Body {
	lits := make([]Literal, len(b.Literals))
	for i, l := range b.Literals {
		l.at = i + 1
		lits[i] = l
	}
	return Body{Literals: lits}
}

// shownName is a relation's name as the program wrote it, without the spellings rewrites give it: an
// adorned or delta relation's suffix, or a private member's module scope.
func shownName(rel string) string {
	if i := strings.IndexByte(rel, 0); i >= 0 {
		rel = rel[:i]
	}
	return displayName(rel)
}

// leaf is the witness of one fact a Source or generator gave.
func leaf(rel string, vals []ns.Value, cites []string) *Witness {
	return &Witness{Relation: shownName(rel), Values: append([]ns.Value(nil), vals...), Cites: append([]string(nil), cites...)}
}

// atomValues resolves an atom's arguments under a binding; an unbound one is the zero Value.
func atomValues(a *Atom, bnd *binding) []ns.Value {
	out := make([]ns.Value, len(a.Args))
	for i, t := range a.Args {
		if v, ok := resolve(t, bnd); ok {
			out[i] = v
		}
	}
	return out
}

// witnessing reports whether this Eval records witnesses.
func (b *Base) witnessing() bool { return b.run != nil && b.run.witness }

// negationWitnesses are the Negated nodes for the `not` literals a binding passed, or nil when the
// Eval records no witnesses.
func (b *Base) negationWitnesses(negs []Literal, bnd *binding) []placed {
	if !b.witnessing() {
		return nil
	}
	out := make([]placed, 0, len(negs))
	for _, l := range negs {
		if isAggReduce(l.Neg.Relation) {
			continue // a body aggregate's zero clause: its node is the aggregate's, with no children
		}
		out = append(out, placed{at: l.at, node: &Witness{Relation: shownName(l.Neg.Relation), Values: atomValues(l.Neg, bnd), Negated: true}})
	}
	return out
}
