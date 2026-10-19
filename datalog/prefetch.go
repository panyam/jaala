package datalog

import (
	"cmp"
	"slices"

	"github.com/panyam/jaala/ns"
)

// A Source that batches lookups (ns.BatchLookupSource, #153) is asked once for every value the
// literals before a looked-up call bind it to, rather than once per value as solve reaches the call.
// solve is depth-first and never holds a body's bindings (#120), so the values aren't known until the
// body runs: prefetch solves the body up to the call, collects the distinct values not yet looked up,
// and asks for them batchSize at a time, and the solve that follows finds each in the Eval's cache. The
// prefix runs once more per looked-up call, work in memory spent to save a round trip per value.
// Anything the prefix misses is looked up as before, so the answer never depends on it.
//
// Only the planned SemiNaive prefetches (evalRun.batch): Naive is the reference, and the written order
// matches its citations, which a batch's tuple order could change.

// batchSize is the most values one batch carries, which bounds what prefetch holds at once.
const batchSize = 1024

// prefetch asks the Source for each looked-up call in lits, a body's positive literals in the order
// solve runs them, and in negs, its negations, in batches. A negation is checked once the positive
// literals have all matched, so its values come from one more run of the whole positive body, which
// collects every negated call's at once (#230). A Volatile predicate stops it, since running the
// prefix again could answer differently.
func prefetch(b *Base, lits, negs []Literal) error {
	if b.run == nil || !b.run.batch {
		return nil
	}
	if err := prefetchPushed(b, lits); err != nil {
		return err
	}
	if err := prefetchClosures(b, lits); err != nil {
		return err
	}
	bl, ok := b.looker.(ns.BatchLookupSource)
	if !ok {
		return nil
	}
	for i, lit := range lits {
		atom := lit.Pos
		if atom == nil {
			continue
		}
		if bi, ok := predicateIn(b.reg, atom.Relation); ok {
			if bi.Volatile {
				return nil
			}
			continue
		}
		if i == 0 || !b.looksUpBase(atom) {
			continue // the first literal has only its constants bound, one lookup
		}
		atom = lookedUpAtom(atom)
		if err := prefetchFor(b, bl, lits[:i], []prefetchCall{{atom, i}}); err != nil {
			return err
		}
	}
	var negated []prefetchCall
	for k, lit := range negs {
		if lit.Neg != nil && b.looksUpBase(lit.Neg) {
			negated = append(negated, prefetchCall{lit.Neg, len(lits) + k})
		}
	}
	if len(negated) == 0 {
		return nil
	}
	return prefetchFor(b, bl, lits, negated)
}

// looksUpBase reports whether a call to atom goes to the Source's Lookup: a base relation it looks up.
func (b *Base) looksUpBase(atom *Atom) bool {
	if _, base := b.schemaOf(atom.Relation); !base {
		return false
	}
	return b.looksUp(atom.Relation)
}

// A prefetchCall is a looked-up call to prefetch for, and its literal's place in the body as Explain
// lists it (the positive literals, then the negations).
type prefetchCall struct {
	atom *Atom
	at   int
}

// prefetchFor solves prefix and asks, for each of calls, for the values it binds the call to that no
// Eval has looked up, grouped by the positions bound.
func prefetchFor(b *Base, bl ns.BatchLookupSource, prefix []Literal, calls []prefetchCall) error {
	type batch struct {
		call int
		mask patternMask
	}
	pending := map[batch][][]ns.Value{}
	asked := map[string]bool{}
	flush := func(k batch) error {
		vals := pending[k]
		delete(pending, k)
		c := calls[k.call]
		return b.run.explain.at(c.at, func() error { return b.lookupBatch(bl, c.atom, k.mask, vals) })
	}
	e := b.run.explain
	if e != nil {
		e.quiet = true
	}
	err := solve(prefix, 0, newBinding(), b, func(bnd *binding) error {
		for ci, c := range calls {
			mask, vals, any := boundArgs(c.atom.Args, bnd)
			if !any {
				continue
			}
			key := lookupKey(c.atom.Relation, mask, vals)
			if _, done := b.run.looked[key]; done || asked[key] {
				continue
			}
			if _, kept := b.lookups.get(key); kept {
				continue // left for solve to read, so Explain counts it as kept
			}
			asked[key] = true
			k := batch{ci, mask}
			pending[k] = append(pending[k], vals)
			if len(pending[k]) < batchSize {
				continue
			}
			if err := flush(k); err != nil {
				return err
			}
		}
		return nil
	})
	if e != nil {
		e.quiet = false
	}
	if err != nil {
		return err
	}
	keys := make([]batch, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(x, y batch) int {
		if x.call != y.call {
			return x.call - y.call
		}
		return cmp.Compare(x.mask, y.mask)
	})
	for _, k := range keys {
		if err := flush(k); err != nil {
			return err
		}
	}
	return nil
}
