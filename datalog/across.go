package datalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/panyam/jaala/ns"
)

// Across evaluates the query once over every version in vs of the Base's ns.TimedSource (#180), each
// answer row carrying the versions it holds in (Row.Versions): the rows whose Versions hold v are
// exactly the rows At(v) answers. A join holds in the versions all its facts hold in, a tuple derived
// two ways holds in either's, and a `not` holds in the versions the negated relation doesn't, so a
// recursive relation whose facts barely change between versions is derived about once rather than once
// per version (the provenance semiring, with version sets as the annotation).
//
// A generator, filter or comparison holds in every version, so a host generator reading versioned data
// of its own belongs under At. Where two versions of a row are derived differently (a witness, a
// spelling of a number), the row is answered once per way, each with the versions it holds in, so each
// version's row is the one At gives it; Row.CitesAt and Witness.At give a version's citations. A
// query that aggregates is refused for now (#181), and every version in vs must be one the Source
// serves. Across reads the Source's facts whole, without lookups, pushes or walks, and keeps no derived
// relation for a later query.
func Across(vs ns.VersionSet) Option {
	return func(o *evalOptions) { o.across = vs }
}

// factCite is the citation an Across Eval gives each fact in place of its own, which differ per version:
// it names the fact, so a row or witness gives any version's citations once it's answered.
const factCite = "\x00f:"

// acrossBase is the Base an Across Eval over vs runs on: one over every fact holding in some version of
// vs, each with the versions of vs it holds in.
func (b *Base) acrossBase(vs ns.VersionSet) (*Base, error) {
	if b.timed == nil {
		return nil, fmt.Errorf("query: Across needs a Source that serves versions (ns.TimedSource)")
	}
	if vs == nil || vs.Empty() {
		return nil, fmt.Errorf("query: Across needs at least one version")
	}
	if extra := vs.Minus(b.timed.Versions()); !extra.Empty() {
		return nil, fmt.Errorf("query: Across names versions the Source doesn't serve: %s", versionsText(extra))
	}
	src := &acrossSource{ts: b.timed, vs: vs, cache: b.versions, sets: map[string][]ns.VersionSet{}}
	return &Base{src: src, reg: b.reg, edb: newEDBCache(), lookups: newLookupCache(), work: b.work, noIndex: b.noIndex, across: src}, nil
}

// versionsText writes a version set as ranges, "3, 5-7, 9-".
func versionsText(vs ns.VersionSet) string {
	var parts []string
	for r := range vs.Ranges() {
		switch {
		case r.To == ns.Open:
			parts = append(parts, fmt.Sprintf("%d-", r.From))
		case r.To == r.From+1:
			parts = append(parts, strconv.FormatUint(uint64(r.From), 10))
		default:
			parts = append(parts, fmt.Sprintf("%d-%d", r.From, r.To-1))
		}
	}
	return strings.Join(parts, ", ")
}

// acrossSource is a TimedSource's facts across the versions vs as a plain Source: each fact holding in
// some version of vs, cited by factCite and its place in facts when it has citations, with the versions it holds in by
// relation and place in sets.
type acrossSource struct {
	ts    ns.TimedSource
	vs    ns.VersionSet
	cache *versionCache
	mu    sync.Mutex
	facts []*ns.TimedTuple
	sets  map[string][]ns.VersionSet
}

func (s *acrossSource) Schema(rel string) (ns.Schema, bool) { return s.ts.Schema(rel) }
func (s *acrossSource) Relations() []string                 { return s.ts.Relations() }

func (s *acrossSource) Tuples(rel string) []ns.Tuple {
	t, _ := s.TuplesContext(context.Background(), rel)
	return t
}

func (s *acrossSource) TuplesContext(ctx context.Context, rel string) ([]ns.Tuple, error) {
	timed, err := s.cache.read(ctx, s.ts, rel)
	if err != nil {
		return nil, err
	}
	var out []ns.Tuple
	var sets []ns.VersionSet
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range timed {
		t := &timed[i]
		if t.Versions == nil {
			continue
		}
		in := t.Versions.Intersect(s.vs)
		if in.Empty() {
			continue
		}
		var cites []string
		if cited(t) {
			cites = []string{factCite + strconv.Itoa(len(s.facts))}
			s.facts = append(s.facts, t)
		}
		out = append(out, ns.Tuple{Vals: t.Vals, Cites: cites})
		sets = append(sets, in)
	}
	s.sets[rel] = sets
	return out, nil
}

// cited reports whether t has citations in any version, so a fact without needs no factCite to carry.
func cited(t *ns.TimedTuple) bool {
	for _, c := range t.Cites {
		if len(c.Cites) > 0 {
			return true
		}
	}
	return false
}

// setsOf are the versions each of rel's tuples holds in, in the order the edb cache holds them.
func (s *acrossSource) setsOf(rel string) []ns.VersionSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sets[rel]
}

// citesAt resolves citations made under Across to version v's: a fact's factCite becomes its own
// citations at v, and any other citation, such as a generator's, stays. The result is sorted and
// deduplicated, as Row.Cites is.
func (s *acrossSource) citesAt(cites []string, v ns.Version) []string {
	var out []string
	for _, c := range cites {
		if f := s.fact(c); f != nil {
			out = append(out, f.CitesAt(v)...)
		} else {
			out = append(out, c)
		}
	}
	return dedupStrings(out)
}

// fact is the fact a factCite names, nil for any other citation.
func (s *acrossSource) fact(cite string) *ns.TimedTuple {
	if !strings.HasPrefix(cite, factCite) {
		return nil
	}
	i, err := strconv.Atoi(cite[len(factCite):])
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.facts) {
		return nil
	}
	return s.facts[i]
}

// lastVersion is the latest version of vs, or of within when vs holds up to Open.
func lastVersion(vs, within ns.VersionSet) ns.Version {
	var last ns.Version
	for r := range vs.Ranges() {
		last = r.To - 1
		if r.To == ns.Open {
			for w := range within.Ranges() {
				if w.To != ns.Open {
					last = w.To - 1
				}
			}
		}
	}
	return last
}

// versions is the versions the binding holds in so far: the universe until a fact narrows it.
func (bnd *binding) versions(universe ns.VersionSet) ns.VersionSet {
	if n := len(bnd.vers); n > 0 {
		return bnd.vers[n-1]
	}
	return universe
}

// narrow intersects the binding's versions with vs, reporting false, and changing nothing, when no
// version is left. A nil vs holds in every version.
func (bnd *binding) narrow(universe, vs ns.VersionSet) bool {
	if vs == nil {
		return true
	}
	in := bnd.versions(universe).Intersect(vs)
	if in.Empty() {
		return false
	}
	bnd.vers = append(bnd.vers, in)
	return true
}

// passesNegationsAcross narrows bnd to the versions where none of its negated literals holds,
// reporting false when none is left. A negated literal holds in the versions some match of it holds in.
// The narrowed versions stay on bnd until whatever extended it last resets it.
func passesNegationsAcross(bnd *binding, negs []Literal, b *Base, counted func(k int)) (bool, error) {
	for k, lit := range negs {
		cur := bnd.versions(b.across.vs)
		var held ns.VersionSet = ns.IntervalSet{}
		err := b.extendAtom(lit.Neg, bnd, func(ext *binding) error {
			held = held.Union(ext.versions(b.across.vs))
			if cur.Minus(held).Empty() {
				return errStop
			}
			return nil
		})
		if err != nil && err != errStop {
			return false, err
		}
		left := cur.Minus(held)
		if left.Empty() {
			return false, nil
		}
		bnd.vers = append(bnd.vers, left)
		if counted != nil {
			counted(k)
		}
	}
	return true, nil
}

// addAcross adds t, holding in t.vs, to rel under Across (see addTuple). A tuple may be held several
// times, each copy holding in versions none of the others does: one per way it's derived, where the
// way At would keep differs by version. In the versions a copy already holds, t replaces it only where
// At would keep t's derivation (CanonicalCites) or a better spelling of its values; elsewhere t is new.
// It reports whether anything changed, a new copy or a revised one, so the fixpoint runs again.
func (b *Base) addAcross(rel string, t idbTuple) (bool, error) {
	x := b.idbIndexFor(rel, fullMask(len(t.vals)))
	x.sync(b.idb[rel], fullMask(len(t.vals)))
	left := t.vs
	changed := false
	for _, i := range slices.Clone(x.buckets[tupleKey(t.vals)]) {
		if err := b.countWork(); err != nil {
			return false, err
		}
		old := b.idb[rel][i]
		if !valsEqual(old.vals, t.vals) {
			continue
		}
		overlap := left.Intersect(old.vs)
		if overlap.Empty() {
			continue
		}
		left = left.Minus(overlap)
		kept, replaced := old, false
		kept.vals = firstAnnotations(old.vals, t.vals)
		if b.canonical() && (t.wit != nil || t.parts != nil) {
			if c := compareDerivations(t, old); c < 0 || (c == 0 && !sameEvidence(t, old)) {
				kept, replaced = t, true
				kept.vals = firstAnnotations(t.vals, old.vals)
			}
		}
		if !replaced && sameAnnotations(kept.vals, old.vals) {
			continue // At keeps what it holds in these versions
		}
		if kept.wit != nil && !sameAnnotations(kept.vals, kept.wit.Values) {
			w := *kept.wit
			w.Values = kept.vals
			kept.wit = &w
		}
		kept.vs = overlap
		if rest := old.vs.Minus(overlap); rest.Empty() {
			b.idb[rel][i] = kept
			if b.run.revised == nil {
				b.run.revised = map[string][]int{}
			}
			b.run.revised[rel] = append(b.run.revised[rel], i)
		} else {
			b.idb[rel][i].vs = rest
			b.idb[rel] = append(b.idb[rel], kept)
		}
		changed = true
	}
	if !left.Empty() {
		t.vs = left
		b.idb[rel] = append(b.idb[rel], t)
		changed = true
	}
	return changed, nil
}

// rowAlts are an answer row's copies under Across, as addAcross keeps a tuple's: one per way the row is
// answered, each with the versions it's answered that way in.
type rowAlts []*answerRow

// addAcross adds the row r holding in vs to the copies of its key, as projector.add would to one row:
// in the versions a copy holds, r replaces it where it survives it (see order.survives).
func (p *projector) addAcross(r *answerRow, bnd, canon *binding, vs ns.VersionSet) {
	alts := p.alts[string(p.key)]
	left := vs
	var added rowAlts
	for _, cur := range alts {
		overlap := left.Intersect(cur.vs)
		if overlap.Empty() {
			continue
		}
		left = left.Minus(overlap)
		if !p.ord.survives(r, cur) {
			continue
		}
		kept := &answerRow{vals: append([]ns.Value(nil), r.vals...), key: string(p.key), vs: overlap}
		p.keep(kept, bnd, canon)
		cur.vs = cur.vs.Minus(overlap)
		added = append(added, kept)
	}
	if !left.Empty() {
		kept := &answerRow{vals: append([]ns.Value(nil), r.vals...), key: string(p.key), vs: left}
		p.keep(kept, bnd, canon)
		added = append(added, kept)
	}
	alts = slices.DeleteFunc(append(alts, added...), func(a *answerRow) bool { return a.vs.Empty() })
	p.alts[string(p.key)] = alts
	n := 0
	for _, a := range p.alts {
		n += len(a)
	}
	p.held = max(p.held, n)
}

// answerAcross is the copies kept, in the answer's order, a row's copies by their first version.
func (p *projector) answerAcross() []*answerRow {
	var out []*answerRow
	for _, alts := range p.alts {
		out = append(out, alts...)
	}
	slices.SortFunc(out, func(a, b *answerRow) int {
		if c := p.ord.compare(a, b); c != 0 {
			return c
		}
		return compareVersions(a.vs, b.vs)
	})
	return out
}

// compareVersions orders two disjoint version sets by their first version.
func compareVersions(a, b ns.VersionSet) int {
	first := func(s ns.VersionSet) ns.Version {
		for r := range s.Ranges() {
			return r.From
		}
		return ns.Open
	}
	fa, fb := first(a), first(b)
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	}
	return 0
}

// hasAggregates reports whether q, its body aggregates lowered, reduces anything, in its goal or a rule.
func hasAggregates(q Query) bool {
	if hasAggregate(q.Select) || len(q.Having) > 0 {
		return true
	}
	for _, r := range q.Rules {
		if r.aggregates() {
			return true
		}
	}
	return false
}

// At is the witness as At(v) gives it, for a witness an Across Eval answered: each fact's citations
// those recorded for v, and no versions. A witness from any other Eval comes back as it is.
func (w *Witness) At(v ns.Version) *Witness {
	if w == nil || w.across == nil {
		return w
	}
	c := *w
	c.Versions, c.across = nil, nil
	if w.Rule == "" && !w.Negated {
		var cites []string
		for _, cite := range w.raw {
			if f := w.across.fact(cite); f != nil {
				cites = append(cites, f.CitesAt(v)...)
			} else {
				cites = append(cites, cite)
			}
		}
		c.Cites, c.raw = cites, nil
	}
	c.Children = make([]*Witness, len(w.Children))
	for i, ch := range w.Children {
		c.Children[i] = ch.At(v)
	}
	if len(w.Children) == 0 {
		c.Children = w.Children
	}
	return &c
}

// answered is w as an Across Eval answers it: each fact's citations at version v, the row's latest,
// with what At(v) needs to give another version's.
func (s *acrossSource) answered(w *Witness, v ns.Version) *Witness {
	if w == nil {
		return nil
	}
	c := *w
	c.across = s
	if w.Rule == "" && !w.Negated {
		c.raw = w.Cites
		var cites []string
		for _, cite := range w.Cites {
			if f := s.fact(cite); f != nil {
				cites = append(cites, f.CitesAt(v)...)
			} else {
				cites = append(cites, cite)
			}
		}
		c.Cites = cites
	}
	if len(w.Children) > 0 {
		c.Children = make([]*Witness, len(w.Children))
		for i, ch := range w.Children {
			c.Children[i] = s.answered(ch, v)
		}
	}
	return &c
}

// CitesAt are the row's citations in version v, for a row an Across Eval answered (see Row.Versions);
// for any other row, its Cites.
func (r Row) CitesAt(v ns.Version) []string {
	if r.across == nil {
		return r.Cites
	}
	return r.across.citesAt(r.raw, v)
}

// pageAcross applies offset and limit to an Across answer's rows, in order, version by version: a row
// keeps the versions where it is one of that version's rows from offset on, at most limit of them (0,
// no limit), as At(v) pages that version's answer. versions are split by how many rows came before in
// them, so each split counts once whatever the versions number.
func pageAcross(rows []*answerRow, universe ns.VersionSet, offset, limit int) []*answerRow {
	if offset == 0 && limit == 0 {
		return rows
	}
	type part struct {
		vs     ns.VersionSet
		before int
	}
	parts := []part{{universe, 0}}
	var out []*answerRow
	for _, r := range rows {
		var kept ns.VersionSet = ns.IntervalSet{}
		var next []part
		for _, p := range parts {
			in := p.vs.Intersect(r.vs)
			if in.Empty() {
				next = append(next, p)
				continue
			}
			if p.before >= offset {
				kept = kept.Union(in)
			}
			if rest := p.vs.Minus(in); !rest.Empty() {
				next = append(next, part{rest, p.before})
			}
			if limit == 0 || p.before+1 < offset+limit {
				next = append(next, part{in, p.before + 1}) // a version whose page is full needs no count
			}
		}
		parts = next
		if !kept.Empty() {
			r.vs = r.vs.Intersect(kept)
			out = append(out, r)
		}
	}
	return out
}
