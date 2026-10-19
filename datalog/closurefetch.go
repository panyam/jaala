package datalog

import "github.com/panyam/jaala/ns"

// A closure or a paths call (#281, #280) reads its step relation a node at a time, through
// closureNeighbours, so over a Source that looks the relation up a walk costs a lookup for every node
// it reaches, and prefetch, which sees only a body's atoms, never sees them (#396). Before a body is
// solved, prefetchClosures walks each such call's reach ahead of it, from every start the literals
// before it bind: in one Walk when the store walks the step relation, bounded by the call's max, and
// otherwise in one LookupBatch per level of a breadth-first walk from all the starts at once. The
// walks that follow find each node's steps kept. A node the prefetch didn't reach is looked up as
// before, so a miss costs a round trip, never an answer. Only the planned SemiNaive prefetches
// (evalRun.batch).

// closureNode stands for the node a walk is at, in a step read prefetchClosures asks a WalkSource
// about.
const closureNode = Var("\x00c.node")

// A walkFrom is the direction and max of a call site's walks, which its starts share.
type walkFrom struct {
	fwd bool
	hi  int
}

// A callSite is a call site's starts in one walkFrom, and, when every start had its other end bound,
// those ends: a batched walk can stop once it has reached them all.
type callSite struct {
	starts  []ns.Value
	seen    map[string]bool
	targets map[string]bool
	free    bool
}

// prefetchClosures prefetches, for each engine walk in lits, a body's positive literals in the order
// solve runs them, the steps its walks will read. A Volatile predicate stops it, as it stops prefetch.
func prefetchClosures(b *Base, lits []Literal) error {
	bl, batches := b.looker.(ns.BatchLookupSource)
	if b.walker == nil && !batches {
		return nil
	}
	// the prefix and the levels are read again here, work no literal counts as reached
	if e := b.run.explain; e != nil {
		e.quiet = true
		defer func() { e.quiet = false }()
	}
	for i, lit := range lits {
		atom := lit.Pos
		if atom == nil {
			continue
		}
		w, step, ok := walkOf(atom.Relation)
		if !ok {
			if bi, ok := predicateIn(b.reg, atom.Relation); ok && bi.Volatile {
				return nil
			}
			continue
		}
		looked := false
		for _, fwd := range []bool{true, false} {
			for _, a := range b.stepReads(step, Term{Var: closureNode}, fwd) {
				looked = looked || b.looksUpBase(&a)
			}
		}
		if !looked {
			continue
		}
		groups, order, err := callStarts(b, lits[:i], atom, w)
		if err != nil {
			return err
		}
		for _, g := range order {
			err := b.run.explain.at(i, func() error { return b.prefetchWalk(bl, batches, step, g, groups[g]) })
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// callStarts solves prefix and collects, by direction and max, the values it binds atom's walk to
// start from, in the order they come.
func callStarts(b *Base, prefix []Literal, atom *Atom, w *engineWalk) (map[walkFrom]*callSite, []walkFrom, error) {
	groups := map[walkFrom]*callSite{}
	var order []walkFrom
	vals := make([]ns.Value, len(atom.Args))
	bound := make([]bool, len(atom.Args))
	err := solve(prefix, 0, newBinding(), b, func(bnd *binding) error {
		for k, t := range atom.Args {
			vals[k], bound[k] = resolve(t, bnd)
		}
		if !bound[w.lo] || !bound[w.lo+1] {
			return nil
		}
		lo, hi, err := w.bounds(&vals[w.lo], &vals[w.lo+1])
		if err != nil {
			return nil // a bound from data walks nothing
		}
		if w == pathsWalk {
			mode, err := pathMode(vals[3])
			if !bound[3] || err != nil || mode == "walk" && hi == 0 {
				return nil
			}
		}
		var g walkFrom
		var start ns.Value
		switch {
		case bound[1]:
			g, start = walkFrom{true, hi}, vals[1]
		case bound[2]:
			g, start = walkFrom{false, hi}, vals[2]
		default:
			return nil
		}
		s, ok := groups[g]
		if !ok {
			s = &callSite{seen: map[string]bool{}, targets: map[string]bool{}}
			groups[g] = s
			order = append(order, g)
		}
		if k := valueKey(start); !s.seen[k] {
			s.seen[k] = true
			s.starts = append(s.starts, start)
		}
		// a walk with both ends bound stops at its target, once it is at least min steps out
		if g.fwd && bound[2] && lo <= 1 {
			s.targets[valueKey(vals[2])] = true
		} else {
			s.free = true
		}
		return nil
	})
	return groups, order, err
}

// prefetchWalk prefetches one call site's walks from s's starts: in one Walk when the store walks its
// one step read, else batched by level.
func (b *Base) prefetchWalk(bl ns.BatchLookupSource, batches bool, step string, g walkFrom, s *callSite) error {
	if b.walker != nil {
		reads := b.stepReads(step, Term{Var: closureNode}, g.fwd)
		if len(reads) == 1 {
			if ws, ok := b.walkedStep(&reads[0]); ok {
				var fresh []ns.Value
				for _, v := range s.starts {
					if !b.keptRead(b.stepReads(step, Term{Const: &v}, g.fwd)[0]) {
						fresh = append(fresh, v)
					}
				}
				if len(fresh) == 0 {
					return nil
				}
				return b.walk(&reads[0], ws, fresh, g.hi)
			}
		}
	}
	if !batches {
		return nil
	}
	targets := s.targets
	if s.free {
		targets = nil
	}
	return b.prefetchLevels(bl, step, g, s.starts, targets)
}

// walkedStep is the walkStep the WalkSource walks for a step read with the node at closureNode, if it
// walks it.
func (b *Base) walkedStep(a *Atom) (walkStep, bool) {
	if !b.looksUpBase(a) {
		return walkStep{}, false
	}
	s := walkStep{rel: a.Relation, from: -1, to: -1, consts: map[int]ns.Value{}}
	var bound []int
	for p, t := range a.Args {
		switch {
		case t.Const != nil:
			s.consts[p] = *t.Const
			bound = append(bound, p)
		case t.Var == closureNode:
			s.from = p
		case t.Var == closureVar:
			s.to = p
		}
	}
	if s.from < 0 || s.to < 0 || s.from >= maskWidth {
		return walkStep{}, false
	}
	return s, b.walker.Walks(a.Relation, s.from, s.to, bound)
}

// keptRead reports whether a step read's lookup is kept already, by this Eval or the Base.
func (b *Base) keptRead(a Atom) bool {
	mask, vals, _ := boundArgs(a.Args, newBinding())
	key := lookupKey(a.Relation, mask, vals)
	if _, done := b.run.looked[key]; done {
		return true
	}
	_, kept := b.lookups.get(key)
	return kept
}

// prefetchLevels walks step breadth first from starts, out or back, to g's max, asking in one batch
// per level (batchSize values at most) for the steps of the level's nodes not kept yet, and stops
// early once it has reached every target when targets is set.
func (b *Base) prefetchLevels(bl ns.BatchLookupSource, step string, g walkFrom, starts []ns.Value, targets map[string]bool) error {
	seen := map[string]bool{}
	found := 0
	reach := func(v ns.Value, into []ns.Value) []ns.Value {
		k := valueKey(v)
		if seen[k] {
			return into
		}
		seen[k] = true
		if targets[k] {
			found++
		}
		return append(into, v)
	}
	var frontier []ns.Value
	for _, v := range starts {
		frontier = reach(v, frontier)
	}
	for depth := 0; len(frontier) > 0 && (g.hi == 0 || depth < g.hi); depth++ {
		if targets != nil && found == len(targets) {
			return nil
		}
		if err := b.batchSteps(bl, step, g.fwd, frontier); err != nil {
			return err
		}
		var next []ns.Value
		for _, n := range frontier {
			err := b.closureNeighbours(step, n, g.fwd, func(m ns.Value, _ closureStep) error {
				next = reach(m, next)
				return nil
			})
			if err != nil {
				return err
			}
		}
		frontier = next
	}
	return nil
}

// batchSteps asks, in one batch per step read and pattern, for the steps of nodes no Eval has kept.
func (b *Base) batchSteps(bl ns.BatchLookupSource, step string, fwd bool, nodes []ns.Value) error {
	type batch struct {
		read int
		mask patternMask
	}
	pending := map[batch][][]ns.Value{}
	atoms := map[batch]*Atom{}
	var order []batch
	asked := map[string]bool{}
	for _, n := range nodes {
		for k, a := range b.stepReads(step, Term{Const: &n}, fwd) {
			if !b.looksUpBase(&a) {
				continue
			}
			mask, vals, _ := boundArgs(a.Args, newBinding())
			key := lookupKey(a.Relation, mask, vals)
			if asked[key] || b.keptRead(a) {
				continue
			}
			asked[key] = true
			bk := batch{k, mask}
			if _, ok := atoms[bk]; !ok {
				atoms[bk] = &a
				order = append(order, bk)
			}
			pending[bk] = append(pending[bk], vals)
			if len(pending[bk]) == batchSize {
				if err := b.lookupBatch(bl, atoms[bk], bk.mask, pending[bk]); err != nil {
					return err
				}
				delete(pending, bk)
			}
		}
	}
	for _, bk := range order {
		if vals, ok := pending[bk]; ok {
			if err := b.lookupBatch(bl, atoms[bk], bk.mask, vals); err != nil {
				return err
			}
		}
	}
	return nil
}
