package datalog

import "github.com/panyam/jaala/ns"

// A goal that is the only reader of a relation the Base would read whole streams it into its answer
// (#264): the goal's first literal ranges over the Source's TuplesSeq, so a limit without an order by
// stops the read, and a projection or aggregate holds its answer rather than the relation. What a
// goal streams isn't kept in edbCache, so the next query on the Base reads it again; a relation an
// earlier query left there is read from the cache as before. Only the planned SemiNaive streams,
// since its rewrite fills evalRun.streamable.

// streamable lists the base relations a stream-reading Source serves that the program reads in exactly
// one place, a goal literal, and that the Base doesn't hold. plan reads it so it never reads one whole
// to size it (fanOut).
func streamable(b *Base, q Query) map[string]bool {
	if _, ok := b.src.(ns.StreamSource); !ok {
		return nil
	}
	reads := map[string]int{}
	count := func(lits []Literal, n int) {
		for _, lit := range lits {
			if lit.Pos != nil {
				reads[lit.Pos.Relation] += n
			}
			if lit.Neg != nil {
				reads[lit.Neg.Relation] += n
			}
		}
	}
	count(q.Goal.Literals, 1)
	for _, r := range q.Rules {
		count(r.Body.Literals, 2)
	}
	var out map[string]bool
	for rel, n := range reads {
		if n != 1 {
			continue
		}
		if _, base := b.schemaOf(rel); !base || (b.edb != nil && b.edb.holds(rel)) {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[rel] = true
	}
	return out
}

// streamFirst marks the goal's first literal, lits[0] in the order solve runs them, to stream when its
// relation is streamable and still not held, so solve reaches it once. A rule, demand's included, or
// a prefix prefetch ran through it read it whole already, and it reads it from the cache.
//
// A relation the Source looks up streams only for a call that binds nothing, the one it would read
// whole (#126), and only when stopping is set (a limit without an order by), where the read stops
// early. Otherwise it's kept as before, so asking again asks the Source nothing (#212); a store that
// can answer a whole aggregate or page itself is asked to (#240, #241).
func streamFirst(b *Base, lits []Literal, stopping bool) {
	if len(lits) == 0 || lits[0].Pos == nil {
		return
	}
	a := lits[0].Pos
	if !b.run.streamable[a.Relation] || (b.edb != nil && b.edb.holds(a.Relation)) {
		return
	}
	if b.looksUp(a.Relation) {
		if _, _, any := boundArgs(a.Args, newBinding()); any || !stopping {
			return
		}
	}
	b.run.stream = a
}

// streamEDB solves atom as the Source streams its relation. Each tuple is normalized as edbCache
// normalizes a whole read, and is a candidate exactly where extendEDB's would be: every tuple when
// the cached path scans, only those the index's bucket lists when it would index. That is decided by
// the relation's size, which a stream learns only at indexMinFacts tuples, so the first ones wait
// until then. Answers, Work and Explain then match the cached path's.
func (b *Base) streamEDB(atom *Atom, bnd *binding, yield func(*binding) error) error {
	rel := atom.Relation
	types := b.argTypesOf(rel)
	mask, vals, bound := boundArgs(atom.Args, bnd)
	indexed := bound && b.edb != nil && !b.noIndex
	key := tupleKey(vals)
	if b.run.explain != nil {
		b.explainStream(atom)
	}
	at := make([]ns.Value, 0, 4)
	consider := func(t ns.Tuple, filter bool) error {
		if filter {
			at = at[:0]
			for i, v := range t.Vals {
				if i < maskWidth && mask&(1<<uint(i)) != 0 {
					at = append(at, v)
				}
			}
			if tupleKey(at) != key {
				return nil
			}
		}
		if err := b.countWork(); err != nil {
			return err
		}
		next, ok := unify(atom.Args, t, bnd)
		if !ok {
			return nil
		}
		if b.witnessing() {
			next.last = leaf(rel, t.Vals, t.Cites)
		}
		return yield(next)
	}
	var early []ns.Tuple
	n := 0
	err := streamTuples(b.run, b.src.(ns.StreamSource), rel, func(t ns.Tuple) error {
		n++
		if vs, changed := normalizeValues(t.Vals, types); changed {
			t = ns.Tuple{Vals: vs, Cites: t.Cites}
		}
		if !indexed {
			return consider(t, false)
		}
		if n < indexMinFacts {
			early = append(early, t)
			return nil
		}
		for _, e := range early {
			if err := consider(e, true); err != nil {
				return err
			}
		}
		early = nil
		return consider(t, true)
	})
	for _, e := range early {
		if err != nil {
			break
		}
		err = consider(e, false) // fewer than indexMinFacts: the cached path scans them all
	}
	if b.run.explain != nil {
		b.run.explain.sources[rel].Tuples += n
	}
	return err
}
