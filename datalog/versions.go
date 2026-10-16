package datalog

import (
	"container/list"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/panyam/jaala/ns"
)

// At answers the query at version v of the Base's ns.TimedSource (#179): over exactly the facts that
// hold in v, each with the citations recorded for v, as a Base over a Source serving only those facts
// would answer, witnesses included. Every evaluator, rewrite and option works as without it. The
// version's base relations, indexes, lookups and derived relations are cached apart from every other
// version's (see LimitVersions), and the context a host's generators and Source are handed carries v
// (ns.VersionOf). A Base whose Source isn't a TimedSource, or a version it doesn't serve, is refused.
func At(v ns.Version) Option {
	return func(o *evalOptions) { o.at = &v }
}

// defaultVersionLimit is how many versions' caches a Base keeps at once by default.
const defaultVersionLimit = 8

// LimitVersions sets how many versions' caches the Base keeps for At, the ones used least recently
// dropped first (8 by default; 0 keeps none, so each Eval at a version reads its facts again). A
// version's caches are as large as a Base's over its facts, so a host asking about many versions in
// turn bounds them here. The facts themselves are read from the TimedSource once, for every version.
func (b *Base) LimitVersions(n int) {
	c := b.versions
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = max(n, 0)
	c.evict()
}

// versionCache is a Base's per-version Bases for At, and the TimedSource's reads they share. Shared by
// every copy of the Base, behind a pointer, as the other caches are.
type versionCache struct {
	mu    sync.Mutex
	limit int
	bases map[ns.Version]*list.Element
	lru   *list.List // of *versionBase, most recently used first
	// reads are the TimedSource's relations, read once for every version.
	reads map[string]*timedRead
}

type versionBase struct {
	v    ns.Version
	base *Base
}

// timedRead is one relation's TimedTuples, read once however many versions need it at the same moment.
// A read that fails isn't kept, as an edbRead's isn't.
type timedRead struct {
	mu     sync.Mutex
	done   bool
	tuples []ns.TimedTuple
}

func newVersionCache() *versionCache {
	return &versionCache{limit: defaultVersionLimit, bases: map[ns.Version]*list.Element{}, lru: list.New(), reads: map[string]*timedRead{}}
}

// evict drops the versions used least recently until the limit holds. Called with mu held.
func (c *versionCache) evict() {
	for c.lru.Len() > c.limit {
		el := c.lru.Back()
		c.lru.Remove(el)
		delete(c.bases, el.Value.(*versionBase).v)
	}
}

// drop forgets every version's Base and every read, for a Source whose facts moved.
func (c *versionCache) drop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bases = map[ns.Version]*list.Element{}
	c.lru.Init()
	c.reads = map[string]*timedRead{}
}

// each calls f on every version's Base held, for a setting that applies to them too.
func (c *versionCache) each(f func(*Base)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.lru.Front(); el != nil; el = el.Next() {
		f(el.Value.(*versionBase).base)
	}
}

// read is rel's TimedTuples, read from ts once while the cache holds it.
func (c *versionCache) read(ctx context.Context, ts ns.TimedSource, rel string) ([]ns.TimedTuple, error) {
	c.mu.Lock()
	r, ok := c.reads[rel]
	if !ok {
		r = &timedRead{}
		c.reads[rel] = r
	}
	c.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return r.tuples, nil
	}
	t, err := ts.TimedTuples(ctx, rel)
	if err != nil {
		return nil, err
	}
	r.tuples, r.done = t, true
	return t, nil
}

// atVersion is the Base an Eval at version v runs on: one over the facts of v, kept per version.
func (b *Base) atVersion(v ns.Version) (*Base, error) {
	if b.timed == nil {
		return nil, fmt.Errorf("query: At needs a Source that serves versions (ns.TimedSource)")
	}
	if !b.timed.Versions().Contains(v) {
		return nil, fmt.Errorf("query: version %d is not one the Source serves", v)
	}
	// The settings a version's Base copies are read before the version cache is locked, since refresh
	// drops the version cache with the derived cache locked.
	b.derived.mu.Lock()
	limit, store := b.derived.limit, b.derived.store
	b.derived.mu.Unlock()
	b.lookups.mu.Lock()
	lookLimit := b.lookups.limit
	b.lookups.mu.Unlock()
	c := b.versions
	vb, err := c.base(v, func() (*Base, error) {
		vb, err := b.versionBase(v)
		if err == nil {
			vb.derived.limit, vb.derived.store, vb.lookups.limit = limit, store, lookLimit
		}
		return vb, err
	})
	if err != nil {
		return nil, err
	}
	if b.noIndex {
		return vb.Unindexed(), nil
	}
	return vb, nil
}

// base is v's Base, made by build on first use and marked used. build runs without the cache locked,
// since a SourceAt may open a store; when two Evals build v's at once, the first one kept wins.
func (c *versionCache) base(v ns.Version, build func() (*Base, error)) (*Base, error) {
	c.mu.Lock()
	if el, ok := c.bases[v]; ok {
		c.lru.MoveToFront(el)
		c.mu.Unlock()
		return el.Value.(*versionBase).base, nil
	}
	c.mu.Unlock()
	vb, err := build()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.bases[v]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(*versionBase).base, nil
	}
	if c.limit > 0 {
		c.bases[v] = c.lru.PushFront(&versionBase{v: v, base: vb})
		c.evict()
	}
	return vb, nil
}

// versionBase makes the Base for version v: over the TimedSource's own Source for v when it serves one
// (ns.SourceAt, #312), read through whatever that Source implements, and otherwise over v's facts
// filtered from TimedTuples (atSource).
func (b *Base) versionBase(v ns.Version) (*Base, error) {
	var vb *Base
	if sa, ok := b.timed.(ns.SourceAt); ok {
		src, err := sa.SourceAt(v)
		if err != nil {
			return nil, fmt.Errorf("query: version %d: %w", v, err)
		}
		if vb, err = NewBase(b.reg, src); err != nil {
			return nil, fmt.Errorf("query: version %d: %s", v, strings.TrimPrefix(err.Error(), "query: "))
		}
		vb.timed, vb.versions = nil, nil
	} else {
		vb = &Base{src: &atSource{ts: b.timed, v: v, cache: b.versions}, reg: b.reg, edb: newEDBCache(), derived: newDerivedCache(), lookups: newLookupCache()}
	}
	vb.work, vb.at = b.work, &v
	if b.versioned != nil || vb.versioned != nil {
		vb.versioned = versionTag{outer: b.versioned, inner: vb.versioned, v: v}
	}
	return vb, nil
}

// versionTag is a version's Base's ns.Versioned: the TimedSource's version, the version's number, and
// the version Source's own version when it has one, so what the Base keeps is filed under all three and
// dropped when either Source moves.
type versionTag struct {
	outer, inner ns.Versioned
	v            ns.Version
}

func (t versionTag) Version() string {
	s := "@" + strconv.FormatUint(uint64(t.v), 10)
	if t.outer != nil {
		s = t.outer.Version() + s
	}
	if t.inner != nil {
		s += "/" + t.inner.Version()
	}
	return s
}

// atSource is a TimedSource's facts at one version, as a plain Source: the facts holding in v, with v's
// citations.
type atSource struct {
	ts    ns.TimedSource
	v     ns.Version
	cache *versionCache
}

func (s *atSource) Schema(rel string) (ns.Schema, bool) { return s.ts.Schema(rel) }
func (s *atSource) Relations() []string                 { return s.ts.Relations() }

func (s *atSource) Tuples(rel string) []ns.Tuple {
	t, _ := s.TuplesContext(context.Background(), rel)
	return t
}

func (s *atSource) TuplesContext(ctx context.Context, rel string) ([]ns.Tuple, error) {
	timed, err := s.cache.read(ns.WithVersion(ctx, s.v), s.ts, rel)
	if err != nil {
		return nil, err
	}
	var out []ns.Tuple
	for _, t := range timed {
		if t.Versions != nil && t.Versions.Contains(s.v) {
			out = append(out, ns.Tuple{Vals: t.Vals, Cites: t.CitesAt(s.v)})
		}
	}
	return out, nil
}

func (s *atSource) NoVocabularyHint() string {
	if h, ok := s.ts.(ns.NoVocabularyHinter); ok {
		return h.NoVocabularyHint()
	}
	return "no relations are installed"
}

// A Diff is what changed in a base relation between two versions of a TimedSource: the facts that hold
// in the second and not the first, with the second's citations, and those that held in the first and
// not the second, with the first's (see Base.Diff).
type Diff struct {
	Added, Removed []ns.Tuple
}

// Diff is how rel's facts changed from version from to version to of the Base's ns.TimedSource (#179).
// A fact is its values, read as the engine reads them (as rel's declared types, units folded), so a
// fact whose citations changed and nothing else is in neither list. Each list is in the order the
// Source serves its facts. A relation the vocabulary doesn't hold, a Base whose Source isn't a
// TimedSource, or a version it doesn't serve is refused.
func (b *Base) Diff(ctx context.Context, rel string, from, to ns.Version) (Diff, error) {
	if b.timed == nil {
		return Diff{}, fmt.Errorf("query: Diff needs a Source that serves versions (ns.TimedSource)")
	}
	for _, v := range []ns.Version{from, to} {
		if !b.timed.Versions().Contains(v) {
			return Diff{}, fmt.Errorf("query: version %d is not one the Source serves", v)
		}
	}
	if _, ok := b.schemaOf(rel); !ok {
		return Diff{}, fmt.Errorf("query: %s", b.reg.Unknown(rel))
	}
	timed, err := b.versions.read(ctx, b.timed, rel)
	if err != nil {
		return Diff{}, readFailed(ctx, rel, err)
	}
	at := func(v ns.Version) (map[string]bool, []ns.Tuple) {
		keys := map[string]bool{}
		var facts []ns.Tuple
		for _, t := range timed {
			if t.Versions == nil || !t.Versions.Contains(v) {
				continue
			}
			vals, _ := normalizeValues(b.units(), t.Vals, b.argTypesOf(rel))
			if k := tupleKey(vals); !keys[k] {
				keys[k] = true
				facts = append(facts, ns.Tuple{Vals: vals, Cites: t.CitesAt(v)})
			}
		}
		return keys, facts
	}
	inFrom, fromFacts := at(from)
	inTo, toFacts := at(to)
	var d Diff
	for _, t := range toFacts {
		if !inFrom[tupleKey(t.Vals)] {
			d.Added = append(d.Added, t)
		}
	}
	for _, t := range fromFacts {
		if !inTo[tupleKey(t.Vals)] {
			d.Removed = append(d.Removed, t)
		}
	}
	return d, nil
}
