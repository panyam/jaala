package ns

import (
	"context"
	"iter"
	"sort"
)

// A Tuple is one fact of a relation: its values in argument order, plus the citations that justify
// it. Base facts and derived facts share this shape, so a derived answer stays as verifiable as a
// looked-up one.
type Tuple struct {
	Vals  []Value
	Cites []string
}

// A Schema describes one relation a Source serves.
type Schema struct {
	// Arity is the number of arguments every tuple of the relation carries.
	Arity int
	// Labels names the arguments for error messages ("ref_des", "net"). Optional, and may be shorter
	// than Arity; an unlabelled argument is simply never named.
	Labels []string
	// Types says what each argument denotes: an entity kind, a scalar type with its unit, and
	// optionally a closed vocabulary (see ArgType). Optional, and may be shorter than Arity; a missing
	// entry is an argument the relation says nothing about. Kinds and owners name other arguments by
	// their Labels, so a relation declaring them labels its arguments.
	//
	// A Domain here is enforced: a CONSTANT outside it is rejected before evaluation rather than
	// silently matching nothing, because an empty answer to a question that was never valid reads as
	// a fact about the data. It is only enforced on a labelled argument, since the error has to name
	// what the argument is. A Type is enforced too: a constant is read as the argument's type, or
	// refused when it cannot be (see ArgType.Type).
	Types []ArgType
	// Doc is a one-line description a host shows when the relation is listed (see Vocabulary.Lookup).
	Doc string
}

// A Source is where the base (extensional) relations come from. The engine asks it for a relation's
// schema when it meets an atom, and for the relation's tuples the first time it needs them; it
// indexes and caches from there, so a Source need not.
//
// Tuples are treated as immutable for the life of a Base. Each relation is read once per Base, by one
// goroutine, even when several Evals on the Base first need it at the same moment; different
// relations may be read concurrently.
type Source interface {
	// Schema reports whether the source serves rel, and its shape.
	Schema(rel string) (Schema, bool)
	// Tuples returns every fact of rel. Called at most once per relation per Base, unless the Source
	// is Versioned and its version changes, or the host calls the Base's Forget.
	Tuples(rel string) []Tuple
	// Relations lists every relation name the source serves, in the order a did-you-mean suggestion
	// should prefer on a tie. An EMPTY list means no vocabulary is installed at all, which the engine
	// treats differently from a vocabulary that lacks a name: it cannot call a relation unknown when
	// it knows of none, so it says so instead of guessing at a typo.
	Relations() []string
}

// ContextSource is optionally implemented by a Source whose reads take long enough to cancel, such as
// one that runs a query against a database. When a Source implements it, the engine reads a relation
// through TuplesContext with the query's context instead of through Tuples, and a cancelled or
// failed read is not cached: the next query needing the relation reads it again under its own
// context. A read error stops the query that asked, wrapped with the relation's name.
type ContextSource interface {
	TuplesContext(ctx context.Context, rel string) ([]Tuple, error)
}

// StreamSource is optionally implemented by a Source that can hand over a relation's facts one at a
// time, such as one reading a database cursor (#233). When a Source implements it, the engine reads
// a relation whole through TuplesSeq rather than TuplesContext or Tuples, and charges each tuple to
// the Eval's Budget as it arrives, so a query over a relation larger than its budget stops having
// held at most that many tuples rather than after the whole relation is in memory. A Source that
// returns a slice is read as before, and its read is not charged.
//
// The engine stops ranging when the budget is spent or the query's context ends, so the sequence
// should release what it holds (close its cursor) when yield returns false. An error yielded stops
// the read and fails the query, wrapped with the relation's name; a read that stops for any reason
// is not kept, and the next query needing the relation reads it again. A relation read in full is
// kept, as Tuples is, except one only the goal reads, which the engine solves as it arrives and doesn't
// keep (#264), so a limit stops the read; such a relation may be read once per query.
type StreamSource interface {
	TuplesSeq(ctx context.Context, rel string) iter.Seq2[Tuple, error]
}

// LookupSource is optionally implemented by a Source that answers a relation's facts matching some
// bound arguments from its own index, such as a database table indexed on a column (#126). When a
// Source implements it, the engine asks Lookup for the facts a call with at least one argument bound
// needs, instead of reading the relation whole and indexing it in memory. bound maps argument
// positions, from 0, to the values the call binds them to.
//
// Lookup may return facts that don't match, since the engine checks every one, but it must not leave
// out one that does, or the answer silently loses it. A bound value is already read as its argument's
// declared type, and a stored value matches when, read as that type too, it is the same value: two
// numbers when the numbers are equal (a bound N(1) matches a stored 1.0), two texts when the text is,
// and a number never text. Read as its type means what the engine does with what Lookup returns: in a
// number argument text is read as its number, and in a text or entity argument a number as its text,
// so a pin stored as N(3) matches a bound "3". When the vocabulary folds a Source's units
// (Vocabulary.FoldSourceUnits, #205, #303), a number's unit is folded as well (Units.Fold): a bound
// NU(1, "V") matches a stored NU(1000, "mV"), so a source serving prefixed values compares them
// folded, or returns more. A source whose stored kinds match its declared types, in base units,
// compares as stored. A source unsure how its index compares can return more.
//
// What Lookup returns is kept for the rest of the Eval that asked, and by the Base for later Evals, up
// to the Base's LimitLookupCache in tuples (#212), so a repeated question doesn't ask again and a
// relation the queries only ever probe is held only as far as it was probed. A Versioned Source's
// version moving drops what was kept, as Forget does. A call with nothing bound still reads the
// relation whole, through Tuples or TuplesContext, and the Base keeps that read and answers later
// calls from its own index.
// Unindexed never calls Lookup. Each call counts one unit of work against the Eval's Budget, and a
// failed call is wrapped with the relation's name and not kept.
type LookupSource interface {
	Lookup(ctx context.Context, rel string, bound map[int]Value) ([]Tuple, error)
}

// BatchLookupSource is optionally implemented by a LookupSource that answers many lookups of one
// pattern in one call, such as a database asked `WHERE (a, b) IN (...)` across a worker boundary or a
// network, where each call is a round trip (#153). positions are the bound argument positions, from 0
// and sorted, and each of values is one lookup's values at those positions, in that order. LookupBatch
// returns one result per value, in values' order, each what Lookup would return for it and under
// Lookup's contract: it may hold facts that don't match, and must not leave out one that does. A
// result count other than len(values) fails the query.
//
// The planned SemiNaive asks for a batch as it solves a body, for the values the literals before a
// looked-up call bind it to, or for a negated call those the whole positive body binds it to (#230),
// and keeps each result as Lookup's is, so the calls it answers never reach Lookup. Each value asked counts one unit of work, as a Lookup does. A
// Source that doesn't implement it is asked one value at a time through Lookup.
type BatchLookupSource interface {
	LookupBatch(ctx context.Context, rel string, positions []int, values [][]Value) ([][]Tuple, error)
}

// LookupRelations is optionally implemented by a LookupSource that answers lookups for some of its
// relations and not others, such as a Federation whose members differ (#211). The engine looks up a
// relation only when LooksUp reports true for it, and reads any other relation whole, once per Base,
// as it would from a Source without Lookup. A LookupSource that doesn't implement it looks up every
// relation. The answer for a relation must not change for the life of a Base.
type LookupRelations interface {
	LooksUp(rel string) bool
}

// IndexedLookups is optionally implemented by a LookupSource that knows which of its lookups it
// answers from an index, such as a database whose tables are indexed on some columns and not others
// (#154). Indexed reports whether a lookup of rel with the positions in bound bound (from 0, sorted)
// reads an index rather than scanning the relation. The planner ranks a call it would look up on an
// unindexed pattern as if nothing were bound, since the store scans either way, so a call it can
// answer from an index runs first and binds what the scan would otherwise be asked for. A
// LookupSource that doesn't implement it is taken as indexing every pattern. The answer for a
// pattern must not change for the life of a Base.
type IndexedLookups interface {
	Indexed(rel string, bound []int) bool
}

// Versioned is optionally implemented by a Source whose facts can change while a Base over it is
// kept. A Base caches what it reads, the base relations and their indexes and the derived relations a
// query evaluated in full, on the promise that a Source's facts are fixed for the Base's life. Version
// lets a Source withdraw that promise: each Eval reads it once before reading anything else, and when
// it differs from the version the caches were built at, they are dropped and the relations read again,
// so Tuples may then be called again for a relation. Any string works, as long as it changes whenever
// the facts do. A Source that doesn't implement it is taken as fixed, and a host that changes one
// anyway calls the Base's Forget.
type Versioned interface {
	Version() string
}

// NoVocabularyHinter is optionally implemented by a Source to explain an empty vocabulary in the
// host's terms, such as which import installs its relations. The text follows "; " in an unknown-
// relation error.
type NoVocabularyHinter interface {
	NoVocabularyHint() string
}

// MemSource is an in-memory Source, for tests and for hosts whose facts are already a handful of
// tables. Declare a relation, then Add its tuples.
type MemSource struct {
	schemas map[string]Schema
	tuples  map[string][]Tuple
	order   []string
}

// NewMemSource returns an empty MemSource.
func NewMemSource() *MemSource {
	return &MemSource{schemas: map[string]Schema{}, tuples: map[string][]Tuple{}}
}

// Declare adds a relation with the given argument labels; its arity is the number of labels.
// Declaring a name twice replaces its schema and keeps its tuples.
func (m *MemSource) Declare(rel string, labels ...string) *MemSource {
	if _, ok := m.schemas[rel]; !ok {
		m.order = append(m.order, rel)
	}
	m.schemas[rel] = Schema{Arity: len(labels), Labels: labels}
	return m
}

// DeclareSchema adds a relation with an explicit schema, for one that declares Types or a Doc.
func (m *MemSource) DeclareSchema(rel string, s Schema) *MemSource {
	if _, ok := m.schemas[rel]; !ok {
		m.order = append(m.order, rel)
	}
	m.schemas[rel] = s
	return m
}

// Add appends one tuple to a declared relation. It panics on an undeclared relation or a wrong
// arity, because either is a programming error in the host rather than a property of the data.
func (m *MemSource) Add(rel string, t Tuple) *MemSource {
	s, ok := m.schemas[rel]
	if !ok {
		panic("datalog: MemSource.Add to undeclared relation " + rel)
	}
	if len(t.Vals) != s.Arity {
		panic("datalog: MemSource.Add arity mismatch for " + rel)
	}
	m.tuples[rel] = append(m.tuples[rel], t)
	return m
}

// Schema implements Source.
func (m *MemSource) Schema(rel string) (Schema, bool) {
	s, ok := m.schemas[rel]
	return s, ok
}

// Tuples implements Source.
func (m *MemSource) Tuples(rel string) []Tuple { return m.tuples[rel] }

// Relations implements Source, listing names in sorted order.
func (m *MemSource) Relations() []string {
	out := append([]string(nil), m.order...)
	sort.Strings(out)
	return out
}

// S builds a string value.
func S(s string) Value { return Value{S: s} }

// N builds a numeric value; its string form is the canonical rendering of the number.
func N(f float64) Value { return Value{S: ftoa(f), Num: &f} }

// NU builds a numeric value carrying a base-unit tag.
func NU(f float64, unit string) Value { return Value{S: ftoa(f), Num: &f, BaseUnit: unit} }

// Absent is the value of an argument the source did not state at all.
func Absent() Value { return Value{Absent: true} }
