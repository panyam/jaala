package ns

import "context"

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
			out = append(out, Tuple{Vals: t.Vals, Cites: t.CitesAt(last)})
		}
	}
	return out
}
