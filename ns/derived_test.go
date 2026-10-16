package ns

import (
	"context"
	"testing"
)

// Relation reads a kept relation back as facts only when one set of rules derived it at that version,
// since two definitions of one name are two relations.
func TestMemDerivedStoreReadsARelationBackByNameAndVersion(t *testing.T) {
	var s MemDerivedStore
	ctx := context.Background()
	one := []Tuple{{Vals: []Value{S("a")}}}
	if err := s.KeepDerived(ctx, DerivedKey{Relation: "r", Rules: "r\nr(?a) :- n(?a)", Version: "1"}, one); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Relation("r", "1"); !ok || len(got) != 1 {
		t.Errorf("Relation(r, 1): %v, %v; want the one tuple kept", got, ok)
	}
	if _, ok := s.Relation("r", "2"); ok {
		t.Errorf("Relation(r, 2) found a relation kept only at version 1")
	}
	if _, ok, _ := s.Derived(ctx, DerivedKey{Relation: "r", Rules: "r\nr(?a) :- m(?a)", Version: "1"}); ok {
		t.Errorf("Derived found r under rules it wasn't kept under")
	}
	s.KeepDerived(ctx, DerivedKey{Relation: "r", Rules: "r\nr(?a) :- m(?a)", Version: "1"}, nil)
	if _, ok := s.Relation("r", "1"); ok {
		t.Errorf("Relation(r, 1) answered with two definitions of r kept at 1")
	}
}
