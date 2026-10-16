package ns

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// A Vocabulary is the namespace tree a query's names resolve in. Every callable name lives at a
// PATH, its segments separated by ".": `edge`, `str.contains`, `acme.power.rail_budget`. A path's
// last segment is a member; the segments before it name the module holding it, and the root module
// ("") holds the members with no prefix.
//
// A member is one of three kinds: a base relation (a Schema, whose tuples a Source serves), a
// predicate (a Builtin the host computes), or a derived relation defined by a module written in a
// registered Language (see AddModule).
//
// Two rules keep a path unambiguous, and the vocabulary enforces both when a member is added:
//
//   - A segment is a module or a member, never both. `pin` beside `pin.net` is refused, because a
//     reader of `pin.net(...)` could not tell a member of module `pin` from a relation named with a
//     dot.
//   - One definer per path. Registering a path twice is an error naming both definitions, whatever
//     their kinds, so a host predicate cannot silently shadow a base relation or the reverse.
//
// A Vocabulary holds names, never data. An engine pairs it with a Source per query (in
// jaala/datalog, NewBase(v, src)), so a host composes its vocabulary once, checks it at load, and
// binds each dataset as it reads it. It is not safe for concurrent registration, but once built it
// may be read by any number of engines and queries at once.
type Vocabulary struct {
	members map[string]member
	// modules counts, per module path, how many members sit anywhere beneath it. A module exists
	// exactly while something is registered under it, which is what lets a registration refuse a
	// member whose path is already a module.
	modules map[string]int
	// baseOrder is the base relations in registration order: a did-you-mean tie goes to the earlier
	// one, so a host registering from a Source keeps control of which name wins.
	baseOrder []string
	mods      []Module
	langs     map[string]Language
	// hinter explains an empty vocabulary in the host's words, when the Source it was built from
	// offered one.
	hinter NoVocabularyHinter
	// cache holds Check's result and whatever engines Memo, and is replaced on every registration.
	cache *memo
	// units names units and prefixes (AddUnit), nil until one is added.
	units *Units
	// foldSource is whether a Source's values are folded through units as they are read
	// (FoldSourceUnits, jaala#303).
	foldSource bool
}

// memberKind is what defines a path.
type memberKind int

const (
	kindBase memberKind = iota
	kindPredicate
	kindDerived
)

func (k memberKind) String() string {
	switch k {
	case kindPredicate:
		return "predicate"
	case kindDerived:
		return "derived relation"
	}
	return "base relation"
}

type member struct {
	kind   memberKind
	schema Schema  // kindBase
	pred   Builtin // kindPredicate
	module int     // kindDerived: index into Modules()
}

// NewVocabulary returns a vocabulary holding every relation src serves, each as a base relation at
// the path it is named by. src's relations and schemas are read once, here; its tuples never are.
// A nil src gives an empty vocabulary, which a host fills with AddRelation.
//
// It fails when two of the Source's own names break the tree's rules, such as `pin` beside
// `pin.net`, because a query could not name both.
func NewVocabulary(src Source) (*Vocabulary, error) {
	v := &Vocabulary{members: map[string]member{}, modules: map[string]int{}, langs: map[string]Language{}, cache: newMemo()}
	if src == nil {
		return v, nil
	}
	if h, ok := src.(NoVocabularyHinter); ok {
		v.hinter = h
	}
	for _, rel := range src.Relations() {
		s, ok := src.Schema(rel)
		if !ok {
			return nil, fmt.Errorf("query: source lists relation %q but serves no schema for it", rel)
		}
		if err := v.AddRelation(rel, s); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// MustVocabulary is NewVocabulary for a Source known to be well formed, such as a test fixture. It
// panics on the error NewVocabulary would return.
func MustVocabulary(src Source) *Vocabulary {
	v, err := NewVocabulary(src)
	if err != nil {
		panic(err)
	}
	return v
}

// AddRelation registers a base relation at path with the given schema. Its tuples come from
// whichever Source a query is run over, asked for by the same path.
func (v *Vocabulary) AddRelation(path string, s Schema) error {
	if err := v.add(path, member{kind: kindBase, schema: s}); err != nil {
		return err
	}
	v.baseOrder = append(v.baseOrder, path)
	return nil
}

// AddPredicate registers a computed predicate at path: a filter (Holds set) or a generator (Gen
// set), exactly one. It fails on a builtin that is neither or both, on a filter with arity below
// one, on a generator without Modes or with a mode of the wrong length, and on a path the tree's
// rules refuse (see Vocabulary).
func (v *Vocabulary) AddPredicate(path string, b Builtin) error {
	if (b.Holds == nil) == (b.Gen == nil) {
		return fmt.Errorf("query: predicate %q needs exactly one of Holds and Gen", path)
	}
	if b.Holds != nil && b.Arity < 1 {
		return fmt.Errorf("query: filter %q needs arity >= 1", path)
	}
	if b.Gen != nil {
		if len(b.Modes) == 0 {
			return fmt.Errorf("query: generator %q declares no Modes; list the binding patterns it accepts (an all-false mode if it may enumerate with nothing bound)", path)
		}
		width := max(b.Arity, b.MaxArity)
		for _, m := range b.Modes {
			if len(m) != width {
				return fmt.Errorf("query: generator %q has a mode of %d positions, want %d", path, len(m), width)
			}
		}
	}
	return v.add(path, member{kind: kindPredicate, pred: b})
}

// add places m at path after checking the path's shape and the tree's two rules.
func (v *Vocabulary) add(path string, m member) error {
	if err := v.admits(path, m); err != nil {
		return err
	}
	v.put(path, m)
	return nil
}

// admits reports why m cannot be placed at path, or nil when it can.
func (v *Vocabulary) admits(path string, m member) error {
	if err := checkPath(path); err != nil {
		return err
	}
	if prev, ok := v.members[path]; ok {
		return fmt.Errorf("query: %q is defined twice, as a %s and as a %s", path, prev.kind, m.kind)
	}
	if n := v.modules[path]; n > 0 {
		return fmt.Errorf("query: %q cannot be a %s: it is already a module holding %s", path, m.kind, strings.Join(v.children(path), ", "))
	}
	segs := strings.Split(path, ".")
	for i := 1; i < len(segs); i++ {
		mod := strings.Join(segs[:i], ".")
		if prev, ok := v.members[mod]; ok {
			return fmt.Errorf("query: %q needs %q to be a module, but it is a %s", path, mod, prev.kind)
		}
	}
	return nil
}

// put places m at path, which admits has accepted, and drops every cached result.
func (v *Vocabulary) put(path string, m member) {
	v.members[path] = m
	segs := strings.Split(path, ".")
	for i := 1; i < len(segs); i++ {
		v.modules[strings.Join(segs[:i], ".")]++
	}
	v.cache = newMemo()
}

// checkPath refuses a path with an empty segment or a character a query cannot spell, since a member
// registered there could never be called.
func checkPath(path string) error {
	if path == "" {
		return fmt.Errorf("query: empty path")
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return fmt.Errorf("query: path %q has an empty segment", path)
		}
	}
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("query: path %q holds a character a query cannot spell", path)
		}
	}
	return nil
}

// Clone returns an independent copy, so a caller can register more without changing the vocabulary
// it copied. A test adding a predicate uses it to leave a shared vocabulary as it found it.
func (v *Vocabulary) Clone() *Vocabulary {
	out := &Vocabulary{
		members: make(map[string]member, len(v.members)), modules: make(map[string]int, len(v.modules)),
		langs: make(map[string]Language, len(v.langs)), hinter: v.hinter, cache: newMemo(),
		units: v.units.clone(), foldSource: v.foldSource,
	}
	for k, m := range v.members {
		out.members[k] = m
	}
	for k, n := range v.modules {
		out.modules[k] = n
	}
	for k, l := range v.langs {
		out.langs[k] = l
	}
	out.baseOrder = append([]string(nil), v.baseOrder...)
	out.mods = append([]Module(nil), v.mods...) // a Module is never changed after AddModule
	return out
}

// Schema returns the schema of the base relation at path.
func (v *Vocabulary) Schema(path string) (Schema, bool) {
	if v == nil {
		return Schema{}, false
	}
	m, ok := v.members[path]
	if !ok || m.kind != kindBase {
		return Schema{}, false
	}
	return m.schema, true
}

// Predicate returns the builtin registered at path.
func (v *Vocabulary) Predicate(path string) (Builtin, bool) {
	if v == nil {
		return Builtin{}, false
	}
	m, ok := v.members[path]
	if !ok || m.kind != kindPredicate {
		return Builtin{}, false
	}
	return m.pred, true
}

// DefiningModule returns the index, into Modules, of the module defining the derived relation at
// path.
func (v *Vocabulary) DefiningModule(path string) (int, bool) {
	if v == nil {
		return 0, false
	}
	m, ok := v.members[path]
	if !ok || m.kind != kindDerived {
		return 0, false
	}
	return m.module, true
}

// Has reports whether a member of any kind is registered at path.
func (v *Vocabulary) Has(path string) bool {
	if v == nil {
		return false
	}
	_, ok := v.members[path]
	return ok
}

// IsModule reports whether anything is registered beneath path.
func (v *Vocabulary) IsModule(path string) bool { return v != nil && v.modules[path] > 0 }

// BaseRelations returns the base relation paths in registration order. A vocabulary with none
// cannot call a name unknown, because it cannot tell a typo from a vocabulary nobody installed yet,
// and an engine stands its unknown-name checks down until there are some.
func (v *Vocabulary) BaseRelations() []string {
	if v == nil {
		return nil
	}
	return append([]string(nil), v.baseOrder...)
}

func (v *Vocabulary) hasBase() bool { return v != nil && len(v.baseOrder) > 0 }

// children lists the direct children of a module, sorted, as their last segment.
func (v *Vocabulary) children(module string) []string {
	seen := map[string]bool{}
	for p := range v.members {
		if leaf, ok := childOf(module, p); ok {
			seen[leaf] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// childOf reports the segment of path directly under module, if path lies beneath it.
func childOf(module, path string) (string, bool) {
	rest := path
	if module != "" {
		if !strings.HasPrefix(path, module+".") {
			return "", false
		}
		rest = path[len(module)+1:]
	}
	seg, _, _ := strings.Cut(rest, ".")
	return seg, true
}

// namesOf returns every path of the given kind, sorted.
func (v *Vocabulary) namesOf(k memberKind) []string {
	if v == nil {
		return nil
	}
	var out []string
	for p, m := range v.members {
		if m.kind == k {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// memo is a per-key, compute-once cache. Each entry has its own once, so a computation may consult
// another key (Check asks a language, which asks its engine's own memo) without deadlocking.
type memo struct {
	mu      sync.Mutex
	entries map[any]*memoEntry
}

type memoEntry struct {
	once sync.Once
	val  any
	err  error
}

func newMemo() *memo { return &memo{entries: map[any]*memoEntry{}} }

// Memo returns build's result for key, computing it once per state of the vocabulary: any
// registration discards every memoized result. It is how an engine keeps work that depends only on
// the vocabulary (resolved modules, say) shared by every query over it. Concurrent callers of one key
// wait for a single computation. Use a key of an unexported type, as with context values, so engines
// cannot collide.
func (v *Vocabulary) Memo(key any, build func() (any, error)) (any, error) {
	if v == nil {
		return build()
	}
	c := v.cache
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &memoEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.val, e.err = build() })
	return e.val, e.err
}
