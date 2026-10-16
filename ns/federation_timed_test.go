package ns

import (
	"context"
	"slices"
	"testing"
)

// A timed federation's version keeps its members' names and Holds, and is partitioned when the
// federation is, so it pushes and routes at a version as it does as a plain Source (#312).
func TestATimedFederationsVersionIsTheSameKindOfFederation(t *testing.T) {
	timed := NewTimedMemSource(Since(0))
	timed.Declare("edge", "from", "to")
	holds := HoldsPrefix("a", map[string][]int{"edge": {0}})
	for _, build := range []func(...Member) (*Federation, error){NewFederation, NewPartitionedFederation} {
		f, err := build(Member{Name: "rev", Source: timed, Holds: holds}, Member{Name: "lib", Source: NewMemSource().Declare("label", "name", "text")})
		if err != nil {
			t.Fatal(err)
		}
		tf, err := f.Timed()
		if err != nil {
			t.Fatal(err)
		}
		src, err := tf.SourceAt(0)
		if err != nil {
			t.Fatal(err)
		}
		at := src.(*Federation)
		if at.partitioned != f.partitioned || at.members[0].Name != "rev" || at.members[0].Holds == nil || at.members[1].Source != f.members[1].Source {
			t.Errorf("partitioned %v: version 0 is partitioned %v with members %+v", f.partitioned, at.partitioned, at.members)
		}
		if _, ok := at.members[0].Source.(versionView); !ok {
			t.Errorf("a TimedSource member without SourceAt is %T at a version, want its facts at it", at.members[0].Source)
		}
	}
}

// timedLooker is a TimedMemSource answering LookupTimed by its first argument, recording what it was
// asked. looksUp, when set, names the relations it looks up.
type timedLooker struct {
	*TimedMemSource
	asked   []string
	looksUp map[string]bool
}

func (l *timedLooker) LookupTimed(_ context.Context, rel string, bound map[int]Value) ([]TimedTuple, error) {
	l.asked = append(l.asked, rel+":"+bound[0].S)
	var out []TimedTuple
	for _, t := range l.timed[rel] {
		if t.Vals[0].S == bound[0].S {
			out = append(out, t)
		}
	}
	return out, nil
}

func (l *timedLooker) LooksUpTimed(rel string) bool { return l.looksUp == nil || l.looksUp[rel] }

// lookingMem is a MemSource answering Lookup by its first argument, counting the calls.
type lookingMem struct {
	*MemSource
	asked int
}

func (m *lookingMem) Lookup(_ context.Context, rel string, bound map[int]Value) ([]Tuple, error) {
	m.asked++
	var out []Tuple
	for _, t := range m.Tuples(rel) {
		if t.Vals[0].S == bound[0].S {
			out = append(out, t)
		}
	}
	return out, nil
}

// A timed federation looks a call up across versions in the members whose Holds allow it (#317): a
// timed member through LookupTimed, its facts in their versions and its citations named, and a plain
// member through Lookup, its facts holding in every version the federation serves. It looks a relation
// up only where every member serving it can.
func TestATimedFederationLooksUpAcrossVersionsWhereHoldsAllows(t *testing.T) {
	rev := &timedLooker{TimedMemSource: NewTimedMemSource(Span(0, 3))}
	rev.Declare("edge", "from", "to")
	rev.Add("edge", Since(1), []string{"e1"}, S("a1"), S("b"))
	lib := &lookingMem{MemSource: NewMemSource().Declare("edge", "from", "to")}
	lib.Add("edge", Tuple{Vals: []Value{S("x1"), S("y")}, Cites: []string{"x"}})
	f, err := NewFederation(
		Member{Name: "rev", Source: rev, Holds: HoldsPrefix("a", map[string][]int{"edge": {0}})},
		Member{Name: "lib", Source: lib, Holds: HoldsPrefix("x", map[string][]int{"edge": {0}})},
	)
	if err != nil {
		t.Fatal(err)
	}
	tf, err := f.Timed()
	if err != nil {
		t.Fatal(err)
	}
	if !tf.LooksUpTimed("edge") {
		t.Fatalf("edge isn't looked up across versions")
	}
	got, err := tf.LookupTimed(context.Background(), "edge", map[int]Value{0: S("a1")})
	if err != nil || len(got) != 1 || !SameVersions(got[0].Versions, Since(1)) || !slices.Equal(got[0].CitesAt(2), []string{"rev:e1"}) {
		t.Errorf("a1 is %+v %v, want rev's fact from version 1, cited rev:e1", got, err)
	}
	if lib.asked != 0 || !slices.Equal(rev.asked, []string{"edge:a1"}) {
		t.Errorf("asked lib %d times and rev %v, want rev alone", lib.asked, rev.asked)
	}
	got, err = tf.LookupTimed(context.Background(), "edge", map[int]Value{0: S("x1")})
	if err != nil || len(got) != 1 || !SameVersions(got[0].Versions, Span(0, 3)) || !slices.Equal(got[0].CitesAt(0), []string{"lib:x"}) {
		t.Errorf("x1 is %+v %v, want lib's fact in every version", got, err)
	}
	if lib.asked != 1 || len(rev.asked) != 1 {
		t.Errorf("asked lib %d times and rev %v, want lib alone", lib.asked, rev.asked)
	}
	for _, c := range []struct {
		name    string
		members []Member
	}{
		{"a timed member without LookupTimed", []Member{{Source: rev}, {Source: NewTimedMemSource(Span(0, 3)).Declare("edge", "from", "to")}}},
		{"a timed member not looking edge up", []Member{{Source: &timedLooker{TimedMemSource: rev.TimedMemSource, looksUp: map[string]bool{}}}}},
		{"a plain member without Lookup", []Member{{Source: rev}, {Source: NewMemSource().Declare("edge", "from", "to")}}},
	} {
		f, _ := NewFederation(c.members...)
		tf, err := f.Timed()
		if err != nil || tf.LooksUpTimed("edge") {
			t.Errorf("%s: edge is looked up across versions: %v", c.name, err)
		}
	}
}
