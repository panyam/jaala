package ns

import (
	"context"
	"sync"
)

// A DerivedKey names one derived relation as one set of rules derives it from one version of a
// Source's facts (#220).
type DerivedKey struct {
	// Relation is the relation's name as a program calls it ("reach", "path.reach"). A host reading
	// the relation as facts, such as a walk over derived edges, finds it by Relation and Version.
	Relation string
	// Rules is the text of the relation's rules and of every derived relation they read, sorted and
	// led by Relation's own line, so two keys with the same Rules derive the same tuples from the same
	// facts and a change to any rule below the relation is a new key. It is text rather than a digest,
	// which would bring a hashing package into every host's build; a store wanting a short key hashes
	// it itself.
	Rules string
	// Version is the Source's Version (see Versioned) when the relation was derived.
	Version string
}

// DerivedStore is optionally given to a Base (Base.KeepDerivedIn) to keep the derived relations it
// evaluates in full beyond its own memory, such as in the host's database, so a relation is derived
// once per version of the facts however many Bases and processes ask for it (#220). The Base consults
// it only for a Source that is Versioned, since an unversioned Source's facts have no name a stored
// relation could be filed under, and only where it would use its own cache: the planned SemiNaive,
// with no Witnesses or CanonicalCites. Forget doesn't reach the store; a Versioned Source doesn't
// need it.
//
// Derived returns the tuples kept under key, and whether it holds them. They must come back as
// KeepDerived was given them: the same values (text, number, unit and absent alike) and citations,
// in the same order. A tuple of the wrong arity is taken as a miss. An error is taken as a miss too:
// the Base derives the relation, so a failing store costs work, never answers.
//
// KeepDerived stores a relation an Eval evaluated in full. It may be called again for a key already
// held, as when two Bases derived it at once, and should then keep either copy. An Eval ignores its
// error, which the store can log; Materialize returns it. Both may be called from concurrent Evals,
// so a store guards its own state, and neither is called with the Base's locks held.
type DerivedStore interface {
	Derived(ctx context.Context, key DerivedKey) ([]Tuple, bool, error)
	KeepDerived(ctx context.Context, key DerivedKey, tuples []Tuple) error
}

// MemDerivedStore is an in-memory DerivedStore, for tests and for hosts sharing derived relations
// between Bases in one process. Its zero value is ready to use.
type MemDerivedStore struct {
	mu   sync.Mutex
	kept map[DerivedKey][]Tuple
}

// Derived returns the tuples kept under key.
func (s *MemDerivedStore) Derived(_ context.Context, key DerivedKey) ([]Tuple, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.kept[key]
	return t, ok, nil
}

// KeepDerived keeps tuples under key, replacing what was there.
func (s *MemDerivedStore) KeepDerived(_ context.Context, key DerivedKey, tuples []Tuple) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kept == nil {
		s.kept = map[DerivedKey][]Tuple{}
	}
	s.kept[key] = tuples
	return nil
}

// Relation returns the tuples of the relation named rel kept at version under any rules, and whether
// exactly one set of rules derived it there. It is how a host reads a derived relation as facts.
func (s *MemDerivedStore) Relation(rel, version string) ([]Tuple, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Tuple
	n := 0
	for k, t := range s.kept {
		if k.Relation == rel && k.Version == version {
			out, n = t, n+1
		}
	}
	return out, n == 1
}
