package ns

import (
	"math/rand"
	"slices"
	"testing"
)

// kinds are the VersionSet implementations every conformance test runs over, each built from ranges.
var kinds = map[string]func(...VersionRange) VersionSet{
	"intervals": func(r ...VersionRange) VersionSet { return Intervals(r...) },
	"bits":      func(r ...VersionRange) VersionSet { return BitsOf(Intervals(r...)) },
}

// refSet is a VersionSet's meaning over the versions below probeTo, plus whether it holds from there
// on (a range up to Open).
type refSet struct {
	in   map[Version]bool
	tail bool
}

const probeTo = 400

func refOf(rs ...VersionRange) refSet {
	ref := refSet{in: map[Version]bool{}}
	for _, r := range rs {
		for v := r.From; v < r.To && v < probeTo; v++ {
			ref.in[v] = true
		}
		if r.To == Open && r.From < Open {
			ref.tail = true
		}
	}
	return ref
}

// randomRanges draws a few ranges below probeTo, sometimes long enough to be a run of whole
// words and sometimes up to Open.
func randomRanges(rnd *rand.Rand) []VersionRange {
	var rs []VersionRange
	for range rnd.Intn(5) {
		from := Version(rnd.Intn(300))
		switch rnd.Intn(4) {
		case 0:
			rs = append(rs, VersionRange{from, from + 1})
		case 1:
			rs = append(rs, VersionRange{from, from + Version(rnd.Intn(100))})
		case 2:
			rs = append(rs, VersionRange{from, Open})
		default:
			rs = append(rs, VersionRange{from, from + Version(rnd.Intn(10))})
		}
	}
	return rs
}

// check requires s to mean ref: membership below probeTo, its tail, and ranges sorted, non-empty and
// coalesced.
func check(t *testing.T, what string, s VersionSet, ref refSet) {
	t.Helper()
	for v := Version(0); v < probeTo; v++ {
		if s.Contains(v) != ref.in[v] {
			t.Fatalf("%s: Contains(%d) = %v, want %v (ranges %v)", what, v, s.Contains(v), ref.in[v], slices.Collect(s.Ranges()))
		}
	}
	for _, far := range []Version{probeTo, 1 << 20, Open - 1} {
		if s.Contains(far) != ref.tail {
			t.Fatalf("%s: Contains(%d) = %v, want %v", what, far, s.Contains(far), ref.tail)
		}
	}
	if s.Contains(Open) {
		t.Fatalf("%s: Contains(Open)", what)
	}
	if s.Empty() != (len(ref.in) == 0 && !ref.tail) {
		t.Fatalf("%s: Empty() = %v", what, s.Empty())
	}
	var prev *VersionRange
	for r := range s.Ranges() {
		if r.From >= r.To {
			t.Fatalf("%s: empty range %v", what, r)
		}
		if prev != nil && r.From <= prev.To {
			t.Fatalf("%s: ranges %v and %v overlap or touch", what, *prev, r)
		}
		prev = &r
	}
}

func TestVersionSetsConform(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	ops := map[string]struct {
		apply func(a, b VersionSet) VersionSet
		keep  func(a, b bool) bool
	}{
		"intersect": {func(a, b VersionSet) VersionSet { return a.Intersect(b) }, func(a, b bool) bool { return a && b }},
		"union":     {func(a, b VersionSet) VersionSet { return a.Union(b) }, func(a, b bool) bool { return a || b }},
		"minus":     {func(a, b VersionSet) VersionSet { return a.Minus(b) }, func(a, b bool) bool { return a && !b }},
	}
	checked := 0
	for range 2000 {
		ra, rb := randomRanges(rnd), randomRanges(rnd)
		refA, refB := refOf(ra...), refOf(rb...)
		for ka, mkA := range kinds {
			a := mkA(ra...)
			check(t, ka, a, refA)
			for kb, mkB := range kinds {
				b := mkB(rb...)
				for name, op := range ops {
					want := refSet{in: map[Version]bool{}, tail: op.keep(refA.tail, refB.tail)}
					for v := Version(0); v < probeTo; v++ {
						if op.keep(refA.in[v], refB.in[v]) {
							want.in[v] = true
						}
					}
					got := op.apply(a, b)
					check(t, ka+" "+name+" "+kb, got, want)
					if other := BitsOf(IntervalsOf(got)); !SameVersions(got, other) || !SameVersions(got, IntervalsOf(other)) {
						t.Fatalf("%s %s %s: %v and %v as the other kind differ", ka, name, kb, slices.Collect(got.Ranges()), slices.Collect(other.Ranges()))
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("control: no operation checked")
	}
}

func TestAnOperationKeepsItsReceiversKind(t *testing.T) {
	iv, bs := Span(0, 10), Bits(3, 4, 70)
	if _, ok := iv.Intersect(bs).(IntervalSet); !ok {
		t.Errorf("IntervalSet.Intersect(BitSet) is %T", iv.Intersect(bs))
	}
	if _, ok := bs.Union(iv).(BitSet); !ok {
		t.Errorf("BitSet.Union(IntervalSet) is %T", bs.Union(iv))
	}
}

func TestIntervalsJoinRangesThatMeet(t *testing.T) {
	for _, tc := range []struct {
		in   []VersionRange
		want []VersionRange
	}{
		{[]VersionRange{{0, 3}, {3, 5}}, []VersionRange{{0, 5}}},
		{[]VersionRange{{3, 5}, {0, 3}}, []VersionRange{{0, 5}}},
		{[]VersionRange{{0, 3}, {4, 5}}, []VersionRange{{0, 3}, {4, 5}}},
		{[]VersionRange{{2, 2}, {0, 1}}, []VersionRange{{0, 1}}},
		{[]VersionRange{{1, 4}, {2, Open}}, []VersionRange{{1, Open}}},
	} {
		if got := slices.Collect(Intervals(tc.in...).Ranges()); !slices.Equal(got, tc.want) {
			t.Errorf("Intervals(%v) = %v, want %v", tc.in, got, tc.want)
		}
		// A union of the ranges one at a time joins them the same way.
		var u VersionSet = IntervalSet{}
		for _, r := range tc.in {
			u = u.Union(Intervals(r))
		}
		if got := slices.Collect(u.Ranges()); !slices.Equal(got, tc.want) {
			t.Errorf("union of %v = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestABitSetKeepsWholeWordsAsARun(t *testing.T) {
	b := BitsOf(Span(64, 64*1000))
	if len(b.pieces) != 1 || b.pieces[0].bits != nil || b.pieces[0].n != 999 {
		t.Fatalf("pieces = %+v, want one run of 999 words", b.pieces)
	}
	if open := BitsOf(Since(5)); len(open.pieces) != 2 {
		t.Fatalf("Since(5) as bits is %d pieces, want a partial word and a run", len(open.pieces))
	}
	if got := slices.Collect(BitsOf(Since(5)).Ranges()); !slices.Equal(got, []VersionRange{{5, Open}}) {
		t.Fatalf("Since(5) as bits ranges %v", got)
	}
}

func TestTimedTupleCitesAtItsVersion(t *testing.T) {
	tt := TimedTuple{Vals: []Value{S("a")}, Versions: Span(0, 4), Cites: []TimedCites{
		{Versions: Span(0, 2), Cites: []string{"old"}},
		{Versions: Span(2, 4), Cites: []string{"new"}},
	}}
	if got := tt.CitesAt(1); !slices.Equal(got, []string{"old"}) {
		t.Errorf("CitesAt(1) = %v", got)
	}
	if got := tt.CitesAt(3); !slices.Equal(got, []string{"new"}) {
		t.Errorf("CitesAt(3) = %v", got)
	}
	if got := tt.CitesAt(9); got != nil {
		t.Errorf("CitesAt(9) = %v", got)
	}
}
