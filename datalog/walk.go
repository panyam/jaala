package datalog

import (
	"slices"

	"github.com/panyam/jaala/ns"
)

// A Source that implements ns.WalkSource follows a relation from value to value itself (#239). A
// recursive rule such as walk(?a, ?c) :- walk(?a, ?b), next(?b, ?c) probes next with the ?b values its
// own last round derived, a call a round however the rounds batch (#231). findWalks finds such a probe
// after the rewrite: a looked-up literal whose argument at From is the recursive call's column j and
// whose argument at To is the head's column j, so each round's probes are the last round's facts' To
// values. When a body of that relation is about to probe it, prefetchWalks asks the Source for the
// whole walk from the probe's values in one call, and files each reached value's facts as the lookup
// that value would have made. The fixpoint then runs as before, finding every probe kept, so answers,
// citations and witnesses don't change; a probe the walk didn't cover is looked up as before, so a
// wrong guess costs fetches, never answers. Only the planned SemiNaive walks (evalRun.batch).

// A walkStep is a relation a recursive relation's bodies walk: a looked-up call to rel whose argument
// at from each round binds to the last round's facts' values at to, with consts fixed.
type walkStep struct {
	rel      string
	from, to int
	consts   map[int]ns.Value
}

// findWalks records, by head relation, the walks q's recursive rules make (see walkStep) that the
// Source walks.
func findWalks(b *Base, q Query) {
	if b.walker == nil || b.run == nil || !b.run.batch {
		return
	}
	for _, r := range q.Rules {
		for _, rec := range r.Body.Literals {
			if rec.Pos == nil || rec.Pos.Relation != r.Head.Relation {
				continue
			}
			for _, lit := range r.Body.Literals {
				if s, ok := b.walkIn(&r.Head, rec.Pos, lit); ok && !slices.ContainsFunc(b.run.walks[r.Head.Relation], s.same) {
					if b.run.walks == nil {
						b.run.walks = map[string][]walkStep{}
					}
					b.run.walks[r.Head.Relation] = append(b.run.walks[r.Head.Relation], s)
				}
			}
		}
	}
}

// walkIn is the walk lit makes in a rule with head and recursive call rec, if it makes one: lit calls a
// base relation the Source looks up and walks, its argument at from is rec's column j, its argument at
// to is head's column j, and its other arguments are constants or variables nothing else binds.
func (b *Base) walkIn(head, rec *Atom, lit Literal) (walkStep, bool) {
	a := lit.Pos
	if a == nil || !b.looksUpBase(a) {
		return walkStep{}, false
	}
	for j := range rec.Args {
		if j >= len(head.Args) {
			break
		}
		v, out := rec.Args[j].Var, head.Args[j].Var
		if v == "" || v == "_" || out == "" || out == "_" || v == out {
			continue
		}
		s := walkStep{rel: a.Relation, from: -1, to: -1, consts: map[int]ns.Value{}}
		for p, t := range a.Args {
			switch {
			case t.Const != nil:
				s.consts[p] = *t.Const
			case t.Var == v && s.from < 0:
				s.from = p
			case t.Var == out && s.to < 0:
				s.to = p
			}
		}
		if s.from < 0 || s.to < 0 {
			continue
		}
		var bound []int
		for p := range s.consts {
			bound = append(bound, p)
		}
		slices.Sort(bound)
		if b.walker.Walks(a.Relation, s.from, s.to, bound) {
			return s, true
		}
	}
	return walkStep{}, false
}

func (s walkStep) same(o walkStep) bool {
	if s.rel != o.rel || s.from != o.from || s.to != o.to || len(s.consts) != len(o.consts) {
		return false
	}
	for p, c := range s.consts {
		if oc, ok := o.consts[p]; !ok || !valueEq(c, oc) {
			return false
		}
	}
	return true
}

// mask is the probe a step answers: its from position and its constants'.
func (s walkStep) mask() patternMask {
	m := patternMask(1) << uint(s.from)
	for p := range s.consts {
		m |= 1 << uint(p)
	}
	return m
}

// matches reports whether atom is a call s answers: to s's relation, with s's constants and nothing
// else that could be bound but at from.
func (s walkStep) matches(atom *Atom) bool {
	if atom.Relation != s.rel || s.from >= maskWidth || s.to >= len(atom.Args) {
		return false
	}
	for p, t := range atom.Args {
		c, isConst := s.consts[p]
		switch {
		case isConst:
			if t.Const == nil || !valueEq(*t.Const, c) {
				return false
			}
		case t.Const != nil:
			return false
		}
	}
	return true
}

// prefetchWalks walks, for each looked-up call in lits that a walk of head answers, from the values
// the literals before it bind it to that no Eval has looked up, in one call per call site, and keeps
// what each reached value would have looked up. A Volatile predicate stops it, as it stops prefetch.
func prefetchWalks(b *Base, lits []Literal, head string) error {
	if b.run == nil || b.walker == nil {
		return nil
	}
	steps := b.run.walks[head]
	if len(steps) == 0 {
		return nil
	}
	for i, lit := range lits {
		atom := lit.Pos
		if atom == nil {
			continue
		}
		if bi, ok := b.reg.Predicate(atom.Relation); ok {
			if bi.Volatile {
				return nil
			}
			continue
		}
		if i == 0 || !b.looksUpBase(atom) {
			continue
		}
		k := slices.IndexFunc(steps, func(s walkStep) bool { return s.matches(atom) })
		if k < 0 {
			continue
		}
		s := steps[k]
		starts, err := walkStarts(b, lits[:i], atom, s)
		if err != nil {
			return err
		}
		if len(starts) == 0 {
			continue
		}
		if err := b.run.explain.at(i, func() error { return b.walk(atom, s, starts) }); err != nil {
			return err
		}
	}
	return nil
}

// walkStarts solves prefix and collects the values it binds atom's from argument to, when its
// probe is exactly s's, that neither this Eval nor the Base has kept a lookup of.
func walkStarts(b *Base, prefix []Literal, atom *Atom, s walkStep) ([]ns.Value, error) {
	e := b.run.explain
	if e != nil {
		e.quiet = true
		defer func() { e.quiet = false }()
	}
	var starts []ns.Value
	asked := map[string]bool{}
	err := solve(prefix, 0, newBinding(), b, func(bnd *binding) error {
		mask, vals, any := boundArgs(atom.Args, bnd)
		if !any || mask != s.mask() {
			return nil
		}
		key := lookupKey(atom.Relation, mask, vals)
		if _, done := b.run.looked[key]; done || asked[key] {
			return nil
		}
		if _, kept := b.lookups.get(key); kept {
			return nil
		}
		asked[key] = true
		v, _ := resolve(atom.Args[s.from], bnd)
		starts = append(starts, v)
		return nil
	})
	return starts, err
}

// walk asks the Source for s's walk from starts and keeps, for each value it reached, the facts a
// lookup of that value would have returned. A value is reached when it is a start value, or the value
// at s.to of a fact the walk returned for a reached value that matches s's constants, so a fact the
// Source sent that the walk doesn't reach is never filed as some value's whole answer.
func (b *Base) walk(atom *Atom, s walkStep, starts []ns.Value) error {
	if err := b.countWork(); err != nil {
		return err
	}
	r := b.run
	types := b.argTypesOf(s.rel)
	norm := func(v ns.Value, p int) ns.Value {
		return normalizeTuples([]ns.Tuple{{Vals: []ns.Value{v}}}, types[min(p, len(types)):min(p+1, len(types))])[0].Vals[0]
	}
	w := ns.Walk{Relation: s.rel, From: s.from, To: s.to, Bound: s.consts, Start: starts}
	byFrom := map[string][]ns.Tuple{}
	fetched := 0
	ctx := r.context()
	for t, err := range b.walker.Walk(ctx, w) {
		if err != nil {
			return lookupErr(ctx, s.rel, err)
		}
		if err := r.readOne(); err != nil {
			return err
		}
		if len(t.Vals) != len(atom.Args) {
			continue
		}
		t = normalizeTuples([]ns.Tuple{t}, types)[0]
		k := valueKey(t.Vals[s.from])
		byFrom[k] = append(byFrom[k], t)
		fetched++
	}
	mask := s.mask()
	seen := map[string]bool{}
	queue := make([]ns.Value, 0, len(starts))
	for _, v := range starts {
		queue = append(queue, norm(v, s.from))
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		k := valueKey(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		facts := byFrom[k]
		vals := make([]ns.Value, 0, len(s.consts)+1)
		for p := 0; p < maskWidth; p++ {
			if mask&(1<<uint(p)) == 0 {
				continue
			}
			if p == s.from {
				vals = append(vals, v)
			} else {
				vals = append(vals, s.consts[p])
			}
		}
		b.keepLookup(lookupKey(s.rel, mask, vals), facts)
		for _, t := range facts {
			if s.keeps(t) {
				queue = append(queue, t.Vals[s.to])
			}
		}
	}
	if r.explain != nil {
		b.explainWalk(atom, mask, fetched)
	}
	return nil
}

// keeps reports whether a fact the walk returned matches s's constants, so the walk went on from it.
func (s walkStep) keeps(t ns.Tuple) bool {
	for p, c := range s.consts {
		if p >= len(t.Vals) || !valueEq(t.Vals[p], c) {
			return false
		}
	}
	return true
}
