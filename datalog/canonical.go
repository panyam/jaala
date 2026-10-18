package datalog

import (
	"cmp"
	"slices"
	"strings"

	"github.com/panyam/jaala/ns"
)

// CanonicalCites makes every answer's citations, and its witness when Witnesses is set too, the same
// whichever evaluator answers and however its rules are written or planned (#22). Without it a derived
// tuple keeps its first derivation, and the evaluators find derivations in different orders, so a row
// reachable two ways can cite either path.
//
// Each derived tuple, and each answer row, keeps its shortest derivation: the one with the fewest rule
// steps above the facts. Of those, it keeps the one whose rule comes first by its written text, then
// whose body facts come first by relation and values, in written order, and then a source's facts by
// their citations.
// The shortest derivation is the same however it is found, and the rest reads only what was derived,
// so every evaluator keeps the same one. It is also the easiest to check by hand.
//
// It costs work: a tuple derived again is compared with the one kept rather than dropped, and one that
// a shorter derivation replaces is derived from again, so its own consumers can shorten too. Like
// Witnesses, it keeps SemiNaive from inlining and factoring relations, which would change the steps a
// derivation counts, or from storing a body prefix in a supplementary relation (see magic), which would
// merge derivations it has to tell apart. Witnesses are recorded to compare derivations, but Row.Witness is set only when
// Witnesses asks for it.
func CanonicalCites() Option {
	return func(o *evalOptions) { o.canonical = true }
}

// canonical reports whether this Eval keeps canonical derivations (see CanonicalCites).
func (b *Base) canonical() bool { return b.run != nil && b.run.canonical }

// derivedNode is the witness of a derivation by rule text from children, with its height: one more
// than its tallest child's.
func derivedNode(rel string, vals []ns.Value, text string, children []*Witness) *Witness {
	w := &Witness{Relation: shownName(rel), Values: vals, Rule: text, Children: children}
	for _, c := range children {
		w.height = max(w.height, c.height+1)
	}
	if len(children) == 0 {
		w.height = 1
	}
	return w
}

// derivation is what a derivation is compared on: its height, its rule's text, and the nodes it reads
// in written order. A supplementary tuple stands for the literals it stores, so it has their nodes and
// the tallest one's height, and no rule.
func (t idbTuple) derivation() (int, string, []*Witness) {
	if t.parts != nil {
		nodes := inWrittenOrder(t.parts)
		h := 0
		for _, n := range nodes {
			h = max(h, n.height)
		}
		return h, "", nodes
	}
	if t.wit == nil {
		return 0, "", nil
	}
	return t.wit.height, t.wit.Rule, t.wit.Children
}

// compareDerivations orders two derivations of one tuple, the one to keep first.
func compareDerivations(a, b idbTuple) int {
	ah, ar, an := a.derivation()
	bh, br, bn := b.derivation()
	if c := cmp.Compare(ah, bh); c != 0 {
		return c
	}
	if c := strings.Compare(ar, br); c != 0 {
		return c
	}
	// Not the citations themselves: two derivations that compare equal here are one derivation, made
	// again after a child took another of its own, and the newer one is kept (see keepCanonical).
	return compareNodes(an, bn)
}

// compareNodes orders two derivations' body nodes in written order: by relation, then values, and for
// a fact a Source or generator gave, its citations, since two such facts can share their values.
func compareNodes(a, b []*Witness) int {
	for i := range min(len(a), len(b)) {
		x, y := a[i], b[i]
		if c := strings.Compare(x.Relation, y.Relation); c != 0 {
			return c
		}
		if c := cmp.Compare(len(x.Values), len(y.Values)); c != 0 {
			return c
		}
		for j := range x.Values {
			if c := orderValues(x.Values[j], y.Values[j]); c != 0 {
				return c
			}
			if c := strings.Compare(keyText(x.Values[j]), keyText(y.Values[j])); c != 0 {
				return c
			}
		}
		if c := compareBool(x.Negated, y.Negated); c != 0 {
			return c
		}
		if c := strings.Compare(x.Fact, y.Fact); c != 0 {
			return c
		}
		if x.Rule == "" && !x.Negated {
			if c := slices.Compare(x.Cites, y.Cites); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(a), len(b))
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// sameEvidence reports whether two equal derivations still say the same thing: the same citations,
// and the same derived children. A child that has since kept a shorter derivation of its own is a new
// node, so the tuple derived from it is kept again and passed on.
func sameEvidence(a, b idbTuple) bool {
	if !slices.Equal(a.cites, b.cites) {
		return false
	}
	_, _, an := a.derivation()
	_, _, bn := b.derivation()
	for i := range an {
		if an[i].Rule != "" && an[i] != bn[i] {
			return false
		}
	}
	return true
}

// keepCanonical decides what a derived tuple already held at i becomes when t derives it again: t,
// when its derivation comes first, or comes equal with newer evidence; then it is marked revised, so
// a semi-naive round derives from it again. It reports whether the tuple changed.
func (b *Base) keepCanonical(rel string, i int, t idbTuple) bool {
	old := b.idb[rel][i]
	c := compareDerivations(t, old)
	if c > 0 || (c == 0 && sameEvidence(t, old)) {
		return false
	}
	b.idb[rel][i] = t
	if b.run.revised == nil {
		b.run.revised = map[string][]int{}
	}
	b.run.revised[rel] = append(b.run.revised[rel], i)
	return true
}

// bindingKey orders the bindings that project to one answer row, the one whose citations and witness
// the row keeps first, as compareDerivations orders a tuple's derivations.
func compareBindings(a, b *binding) int {
	return compareDerivations(idbTuple{parts: a.wit, cites: dedupStrings(a.cites)}, idbTuple{parts: b.wit, cites: dedupStrings(b.cites)})
}
