package ns

import (
	"context"
	"fmt"
	"strconv"
)

// A TimedTuple is a fact with the versions it holds in (#179). A fact is its values: the same values
// in two versions are one TimedTuple whose Versions hold both, however their citations differ, so a
// host whose citations change from one export to the next still shares its unchanged facts across
// versions.
type TimedTuple struct {
	Vals     []Value
	Versions VersionSet
	// Cites are the fact's citations per version. A version the fact holds in that no entry names has
	// no citations, and an entry's versions outside Versions are ignored.
	Cites []TimedCites
	// ID is the fact's Tuple.ID, the same in every version it holds in.
	ID string
}

// TimedCites are a fact's citations in some of its versions.
type TimedCites struct {
	Versions VersionSet
	Cites    []string
}

// CitesAt are t's citations in version v, the first entry naming v; nil when none does.
func (t TimedTuple) CitesAt(v Version) []string {
	for _, c := range t.Cites {
		if c.Versions != nil && c.Versions.Contains(v) {
			return c.Cites
		}
	}
	return nil
}

// TimedSource is optionally implemented by a Source that serves several versions of its facts, so one
// Base answers at any of them (#179). An Eval given datalog.At(v) reads the facts of version v; one
// without reads the Source as any other, through Tuples and the optional interfaces it implements, so
// a TimedSource serves there whichever facts it wants a plain question to see, such as its latest
// version's. The schemas are the same in every version.
//
// The engine reads each relation's TimedTuples once per Base, as it reads Tuples, and keeps a version's
// facts apart from another's, so a question at one version never sees another's caches. A TimedSource
// that is Versioned too drops all of it when its version moves, such as when a new revision is added.
type TimedSource interface {
	Source
	// Versions are every version the Source serves. An Eval at a version outside it is refused.
	Versions() VersionSet
	// TimedTuples returns every fact of rel in any version, each once, with the versions it holds in.
	TimedTuples(ctx context.Context, rel string) ([]TimedTuple, error)
}

// SourceAt is optionally implemented by a TimedSource that serves each version's facts as a Source of
// its own (#312): the facts holding in v, with v's citations, through whichever of the interfaces a
// plain Source may implement it does (LookupSource, PushSource, StreamSource, WalkSource, SummarySource
// and the rest). An Eval under datalog.At(v) then reads version v as it reads any Source, asking the
// store for what a query binds rather than reading TimedTuples whole, and never calls TimedTuples.
// Across reads TimedTuples, or LookupTimed for a bound call (TimedLookupSource), and Diff reads
// TimedTuples.
//
// The engine calls SourceAt once for each version whose caches it holds (datalog's LimitVersions), and
// again after it drops them, so a host whose versions live in one store returns a small view of it, the
// store and v, rather than copying anything. The returned Source serves the TimedSource's relations with
// the same schemas; one that is Versioned keys its version's kept relations (KeepDerivedIn) by its own
// version too. An error fails the Eval that asked, and the next Eval at v asks again.
type SourceAt interface {
	TimedSource
	SourceAt(v Version) (Source, error)
}

// TimedLookupSource is optionally implemented by a TimedSource that answers a call with bound arguments
// across versions from its own index (#317), so an Eval under datalog.Across asks it for what a query
// binds, as an Eval asks a LookupSource, rather than reading the relation's TimedTuples whole. bound
// maps argument positions, from 0, to values, as Lookup's does, and LookupTimed returns the matching
// facts as TimedTuples returns them: each once, with the versions it holds in and its citations per
// version. It is under Lookup's contract otherwise: it may return facts that don't match, which the
// engine drops, and must not leave out one that does; each call counts one unit of work; a failed call
// fails the Eval, wrapped with the relation's name, and isn't kept. What it returns is kept for the rest
// of the Eval that asked, and not by the Base, since Across keeps nothing between Evals. A call with
// nothing bound still reads TimedTuples. At(v) doesn't call it: a version is read through SourceAt or
// from TimedTuples.
type TimedLookupSource interface {
	TimedSource
	LookupTimed(ctx context.Context, rel string, bound map[int]Value) ([]TimedTuple, error)
}

// TimedLookupRelations is optionally implemented by a TimedLookupSource that answers LookupTimed for
// some of its relations and not others, such as a TimedFederation whose members differ. Under Across
// the engine looks up a relation only when LooksUpTimed reports true for it, and reads any other from
// TimedTuples whole. A TimedLookupSource that doesn't implement it looks up every relation. The answer
// for a relation must not change for the life of a Base.
type TimedLookupRelations interface {
	LooksUpTimed(rel string) bool
}

// versionView is a TimedSource's facts at version v as a plain Source, for a federation member that
// doesn't implement SourceAt. Every read reads TimedTuples whole.
type versionView struct {
	ts TimedSource
	v  Version
}

func (s versionView) Schema(rel string) (Schema, bool) { return s.ts.Schema(rel) }
func (s versionView) Relations() []string              { return s.ts.Relations() }

func (s versionView) Tuples(rel string) []Tuple {
	t, _ := s.TuplesContext(context.Background(), rel)
	return t
}

func (s versionView) TuplesContext(ctx context.Context, rel string) ([]Tuple, error) {
	timed, err := s.ts.TimedTuples(WithVersion(ctx, s.v), rel)
	if err != nil {
		return nil, err
	}
	var out []Tuple
	for _, t := range timed {
		if t.Versions != nil && t.Versions.Contains(s.v) {
			out = append(out, Tuple{Vals: t.Vals, Cites: t.CitesAt(s.v), ID: t.ID})
		}
	}
	return out, nil
}

type versionKey struct{}

// WithVersion is ctx carrying v, the version an Eval reads its TimedSource at. The engine sets it on
// the context a host's generators and Source are handed under datalog.At, so a generator that reads
// versioned data of its own reads v's.
func WithVersion(ctx context.Context, v Version) context.Context {
	return context.WithValue(ctx, versionKey{}, v)
}

// VersionOf is the version ctx carries (see WithVersion), and false when it carries none: the Eval
// reads the Source as a plain one.
func VersionOf(ctx context.Context) (Version, bool) {
	v, ok := ctx.Value(versionKey{}).(Version)
	return v, ok
}

// TimedMemSource is an in-memory TimedSource, for tests and for hosts whose versions are already a
// handful of tables. Declare a relation, then Add each fact with its versions. As a plain Source it
// serves the facts of its last version.
type TimedMemSource struct {
	*MemSource
	versions VersionSet
	timed    map[string][]TimedTuple
	commits  int
}

// NewTimedMemSource returns an empty TimedMemSource serving the versions vs.
func NewTimedMemSource(vs VersionSet) *TimedMemSource {
	return &TimedMemSource{MemSource: NewMemSource(), versions: vs, timed: map[string][]TimedTuple{}}
}

// Add adds a fact of rel holding in vs, with cites in every one of them.
func (m *TimedMemSource) Add(rel string, vs VersionSet, cites []string, vals ...Value) *TimedMemSource {
	return m.AddTimed(rel, TimedTuple{Vals: vals, Versions: vs, Cites: []TimedCites{{Versions: vs, Cites: cites}}})
}

// AddTimed adds a fact whose citations differ between its versions.
func (m *TimedMemSource) AddTimed(rel string, t TimedTuple) *TimedMemSource {
	m.timed[rel] = append(m.timed[rel], t)
	return m
}

func (m *TimedMemSource) Versions() VersionSet { return m.versions }

// Version moves with each Commit, so a Base over the source reads the committed version's facts.
func (m *TimedMemSource) Version() string { return strconv.Itoa(m.commits) }

// Commit adds version to, holding version from's facts with c applied (#288): each fact from holds that
// c doesn't name holds in to too, with from's citations, each fact c adds holds in to with c's
// citations, under the ID it already had when the source holds it in another version, and no other fact
// holds in to, whatever range it was added with. Base.Diff from
// from to to is then c's changes that change from, as a session's OverlaySource.Effective reports
// them. A fact is its values, each read as its argument's declared type, as the engine reads it.
//
// A host's own TimedSource commits a session the same way: the new version is the old one's facts with
// the change set applied, and the facts a version shares with another keep one TimedTuple. Commit
// refuses a from the source doesn't serve, a to it serves already, and a relation it doesn't declare.
// It is not safe to call while a Base over the source is evaluating.
func (m *TimedMemSource) Commit(c Changes, from, to Version) error {
	if !m.versions.Contains(from) {
		return fmt.Errorf("query: commit: version %d is not one the source serves", from)
	}
	if to == Open || m.versions.Contains(to) {
		return fmt.Errorf("query: commit: version %d is one the source serves already", to)
	}
	for _, rel := range c.rels {
		if _, ok := m.Schema(rel); !ok {
			return fmt.Errorf("query: commit: the source declares no relation %q", rel)
		}
	}
	at := Span(to, to+1)
	hold := func(t *TimedTuple, cites []string) {
		t.Versions = t.Versions.Union(at)
		t.Cites = append([]TimedCites{{Versions: at, Cites: cites}}, t.Cites...)
	}
	rels := append([]string(nil), c.rels...)
	for rel := range m.timed {
		if c.byRel[rel] == nil {
			rels = append(rels, rel)
		}
	}
	for _, rel := range rels {
		s, _ := m.Schema(rel)
		key := func(vals []Value) string { return typedKey(s, vals) }
		named := map[string]change{}
		if r := c.byRel[rel]; r != nil {
			for _, k := range r.keys {
				ch := r.facts[k]
				named[key(ch.t.Vals)] = ch
			}
		}
		ts := m.timed[rel]
		held := map[string]bool{}
		for i := range ts {
			k := key(ts[i].Vals)
			held[k] = true
			switch ch, ok := named[k]; {
			case ok && ch.add:
				hold(&ts[i], ch.t.Cites)
			case !ok && ts[i].Versions.Contains(from):
				hold(&ts[i], ts[i].CitesAt(from))
			default:
				ts[i].Versions = ts[i].Versions.Minus(at)
			}
		}
		if r := c.byRel[rel]; r != nil {
			for _, k := range r.keys {
				ch := r.facts[k]
				if ch.add && !held[key(ch.t.Vals)] {
					ts = append(ts, TimedTuple{Vals: ch.t.Vals, Versions: at, Cites: []TimedCites{{Versions: at, Cites: ch.t.Cites}}, ID: ch.t.ID})
				}
			}
		}
		m.timed[rel] = ts
	}
	m.versions = m.versions.Union(at)
	m.commits++
	return nil
}

func (m *TimedMemSource) TimedTuples(_ context.Context, rel string) ([]TimedTuple, error) {
	return m.timed[rel], nil
}

// Tuples serves rel's facts in the last of the source's versions.
func (m *TimedMemSource) Tuples(rel string) []Tuple {
	last, ok := Version(0), false
	for r := range m.versions.Ranges() {
		if r.To != Open {
			last, ok = r.To-1, true
		}
	}
	if !ok {
		return nil
	}
	var out []Tuple
	for _, t := range m.timed[rel] {
		if t.Versions.Contains(last) {
			out = append(out, Tuple{Vals: t.Vals, Cites: t.CitesAt(last), ID: t.ID})
		}
	}
	return out
}
