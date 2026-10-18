package datalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Source that implements ns.LookupSource answers a call with bound arguments from its own index
// (#126), so a relation the queries only probe is never read whole. What a lookup returns is kept on
// the Eval's run, not the Base: across queries the Base would otherwise grow into the whole relation
// one value at a time, holding it without ever having read it.

// looksUp reports whether a call to rel goes to the Source's Lookup: the Source answers lookups for
// rel, the Base indexes (Unindexed scans, as the oracle an equivalence test compares against), and the
// Base doesn't already hold the relation whole, whose index answers without asking the Source.
// A fact relation is looked up as its relation is, unless the Base holds either whole.
func (b *Base) looksUp(rel string) bool {
	if base, ok := factOf(rel); ok {
		return b.looksUp(base) && (b.edb == nil || !b.edb.holds(rel))
	}
	return b.lookable(rel) && !b.noIndex && b.run != nil && (b.edb == nil || !b.edb.holds(rel))
}

// lookable reports whether the Source answers lookups for rel (ns.LookupRelations, #211), a fact
// relation's being its relation's.
func (b *Base) lookable(rel string) bool {
	if base, ok := factOf(rel); ok {
		rel = base
	}
	return b.looker != nil && (b.lookRels == nil || b.lookRels.LooksUp(rel))
}

// lookedUpAtom is the call the Source is asked for atom: a fact relation's (#279) is its relation's
// over the same arguments but the id, so its lookups are the relation's own, kept under their keys.
func lookedUpAtom(atom *Atom) *Atom {
	base, ok := factOf(atom.Relation)
	if !ok {
		return atom
	}
	return &Atom{Relation: base, Args: atom.Args[:len(atom.Args)-1]}
}

// lookup asks the Source for rel's facts matching atom's bound arguments, once per Eval for each
// pattern and values, and not at all when an earlier Eval's answer is still kept (#212). ok is false
// when nothing is bound, so the caller reads the relation whole. Under Across, sets are the versions
// each row holds in (#317), and nil otherwise.
func (b *Base) lookup(atom *Atom, bnd *binding) (rows []ns.Tuple, sets []ns.VersionSet, ok bool, err error) {
	if base, fact := factOf(atom.Relation); fact {
		rows, sets, ok, err = b.lookup(lookedUpAtom(atom), bnd)
		return factTuples(base, rows), sets, ok, err
	}
	mask, vals, any := boundArgs(atom.Args, bnd)
	if !any {
		return nil, nil, false, nil
	}
	r := b.run
	key := lookupKey(atom.Relation, mask, vals)
	if rows, hit, kept := b.lookedUp(key); hit {
		if r.explain != nil {
			b.explainLookupHit(atom, mask, kept)
		}
		if b.across != nil {
			sets = b.across.lookedSets(key)
		}
		return rows, sets, true, nil
	}
	if err := b.countWork(); err != nil {
		return nil, nil, true, err
	}
	bound := make(map[int]ns.Value, len(vals))
	j := 0
	for i := range atom.Args {
		if i < maskWidth && mask&(1<<uint(i)) != 0 {
			bound[i] = vals[j]
			j++
		}
	}
	ctx := r.context()
	if b.across != nil {
		rows, sets, err = b.across.lookup(ctx, atom.Relation, bound)
	} else {
		rows, err = b.looker.Lookup(ctx, atom.Relation, bound)
	}
	if err != nil {
		return nil, nil, true, lookupErr(ctx, atom.Relation, err)
	}
	rows = normalizeTuples(b.units(), rows, b.argTypesOf(atom.Relation))
	if b.across != nil {
		b.across.keepLooked(key, sets)
	}
	b.keepLookup(key, rows)
	if r.explain != nil {
		b.explainLookup(atom, mask, len(rows))
	}
	return rows, sets, true, nil
}

// lookedUp returns what this Eval or, failing that, an earlier one kept for a lookup's key (kept says
// which), copying an earlier Eval's into this one's cache so it holds for the rest of the Eval
// whatever the Base's cache evicts meanwhile.
func (b *Base) lookedUp(key string) (rows []ns.Tuple, hit, kept bool) {
	r := b.run
	if rows, hit := r.looked[key]; hit {
		return rows, true, false
	}
	rows, hit = b.lookups.get(key)
	if !hit {
		return nil, false, false
	}
	if r.looked == nil {
		r.looked = map[string][]ns.Tuple{}
	}
	r.looked[key] = rows
	return rows, true, true
}

// keepLookup keeps a lookup's result for the rest of the Eval, and for later ones on the Base.
func (b *Base) keepLookup(key string, rows []ns.Tuple) {
	r := b.run
	if r.looked == nil {
		r.looked = map[string][]ns.Tuple{}
	}
	r.looked[key] = rows
	b.lookups.put(r.lookGen, key, rows)
}

// lookupErr is a failed lookup's error, wrapped with the relation's name.
func lookupErr(ctx context.Context, rel string, err error) error {
	if be, ok := spentAll(err); ok {
		return be
	}
	if ctx.Err() != nil {
		return fmt.Errorf("query: evaluation stopped looking up %s: %w", rel, ctx.Err())
	}
	return fmt.Errorf("query: looking up %s: %w", rel, err)
}

// lookupBatch asks the Source in one call (ns.BatchLookupSource, #153) for rel's facts matching each
// of vals at mask's positions, and keeps each value's result in the Eval's cache as lookup would have,
// so the calls it answers are hits. Each value counts a unit of work, as its own lookup would.
func (b *Base) lookupBatch(bl ns.BatchLookupSource, atom *Atom, mask patternMask, vals [][]ns.Value) error {
	for range vals {
		if err := b.countWork(); err != nil {
			return err
		}
	}
	var positions []int
	for i := range atom.Args {
		if i < maskWidth && mask&(1<<uint(i)) != 0 {
			positions = append(positions, i)
		}
	}
	r := b.run
	ctx := r.context()
	res, err := bl.LookupBatch(ctx, atom.Relation, positions, vals)
	if err != nil {
		return lookupErr(ctx, atom.Relation, err)
	}
	if len(res) != len(vals) {
		return fmt.Errorf("query: looking up %s: a batch of %d lookups answered %d", atom.Relation, len(vals), len(res))
	}
	types, fetched := b.argTypesOf(atom.Relation), 0
	for k, v := range vals {
		rows := normalizeTuples(b.units(), res[k], types)
		b.keepLookup(lookupKey(atom.Relation, mask, v), rows)
		fetched += len(rows)
	}
	if r.explain != nil {
		b.explainBatch(atom, mask, fetched)
	}
	return nil
}

// lookupKey is a lookup's key in the Eval's cache: the relation, the bound positions, and their values'
// keys (valueKey), so a number and text never share an entry (#162).
func lookupKey(rel string, mask patternMask, vals []ns.Value) string {
	var sb strings.Builder
	sb.WriteString(rel)
	sb.WriteByte(0)
	sb.WriteString(strconv.FormatUint(uint64(mask), 16))
	for _, v := range vals {
		k := valueKey(v)
		sb.WriteByte(0)
		sb.WriteString(strconv.Itoa(len(k)))
		sb.WriteByte(':')
		sb.WriteString(k)
	}
	return sb.String()
}
