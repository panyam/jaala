package ns

import (
	"context"
	"iter"
	"slices"
	"testing"
)

// storeStub is a MemSource that implements every store interface, answering a lookup with the
// relation whole, and records what reached it.
type storeStub struct {
	*MemSource
	version          string
	lookups, batches int
	streamed         int
}

func (s *storeStub) Version() string { return s.version }

func (s *storeStub) TuplesSeq(_ context.Context, rel string) iter.Seq2[Tuple, error] {
	s.streamed++
	return func(yield func(Tuple, error) bool) {
		for _, t := range s.Tuples(rel) {
			if !yield(t, nil) {
				return
			}
		}
	}
}

func (s *storeStub) Lookup(_ context.Context, rel string, _ map[int]Value) ([]Tuple, error) {
	s.lookups++
	return s.Tuples(rel), nil
}

func (s *storeStub) LookupBatch(_ context.Context, rel string, _ []int, values [][]Value) ([][]Tuple, error) {
	s.batches++
	out := make([][]Tuple, len(values))
	for i := range out {
		out[i] = s.Tuples(rel)
	}
	return out, nil
}

func (s *storeStub) LooksUp(rel string) bool                     { return rel != "weight" }
func (s *storeStub) Indexed(rel string, bound []int) bool        { return slices.Contains(bound, 0) }
func (s *storeStub) Pushes(Conjunction) bool                     { return true }
func (s *storeStub) PushesFilter(name string) bool               { return name == "str.glob" }
func (s *storeStub) Summarizes(Conjunction, Summary) bool        { return true }
func (s *storeStub) Walks(string, int, int, []int) bool          { return true }
func (s *storeStub) WalksConjunction(Conjunction, int, int) bool { return true }

func (s *storeStub) Push(context.Context, Conjunction) iter.Seq2[[]Tuple, error] { return nil }
func (s *storeStub) Summarize(context.Context, Conjunction, Summary) iter.Seq2[SummaryRow, error] {
	return nil
}
func (s *storeStub) Walk(context.Context, Walk) iter.Seq2[Tuple, error] { return nil }
func (s *storeStub) WalkConjunction(context.Context, Conjunction, int, int, []Value) iter.Seq2[[]Tuple, error] {
	return nil
}

func overlayBase() *MemSource {
	m := NewMemSource().Declare("edge", "from", "to").
		DeclareSchema("weight", Schema{Arity: 2, Labels: []string{"node", "w"}, Types: []ArgType{{}, {Type: TypeNumber}}})
	m.Add("edge", Tuple{Vals: []Value{S("a"), S("b")}, Cites: []string{"e:ab"}})
	m.Add("edge", Tuple{Vals: []Value{S("b"), S("c")}, Cites: []string{"e:bc"}})
	m.Add("weight", Tuple{Vals: []Value{S("a"), S("1.0")}})
	m.Add("weight", Tuple{Vals: []Value{S("b"), N(2)}})
	return m
}

func session() Changes {
	var c Changes
	c.Remove("edge", Tuple{Vals: []Value{S("b"), S("c")}}).
		Add("edge", Tuple{Vals: []Value{S("c"), S("d")}, Cites: []string{"edit:cd"}}).
		Remove("weight", Tuple{Vals: []Value{S("a"), N(1)}})
	return c
}

func TestAnOverlayServesTheBaseWithTheSessionsEdits(t *testing.T) {
	edges := []string{`"a" "b" id= [e:ab]`, `"c" "d" id= [edit:cd]`}
	weights := []string{`"b" n:2 id= []`}
	for _, tc := range []struct {
		name string
		base Source
	}{{"plain", overlayBase()}, {"store", &storeStub{MemSource: overlayBase()}}} {
		o := Overlay(tc.base, session())
		read := map[string]func(string) []Tuple{
			"Tuples": o.Tuples,
			"TuplesContext": func(rel string) []Tuple {
				ts, err := o.TuplesContext(context.Background(), rel)
				if err != nil {
					t.Fatal(err)
				}
				return ts
			},
			"TuplesSeq": func(rel string) []Tuple {
				var out []Tuple
				for t2, err := range o.TuplesSeq(context.Background(), rel) {
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, t2)
				}
				return out
			},
			"Lookup": func(rel string) []Tuple {
				ts, err := o.Lookup(context.Background(), rel, map[int]Value{0: S("a")})
				if err != nil {
					t.Fatal(err)
				}
				return ts
			},
			"LookupBatch": func(rel string) []Tuple {
				res, err := o.LookupBatch(context.Background(), rel, []int{0}, [][]Value{{S("a")}, {S("c")}})
				if err != nil || len(res) != 2 {
					t.Fatalf("%v %v", res, err)
				}
				if !slices.Equal(factTexts(res[0]), factTexts(res[1])) {
					t.Errorf("%s: a batch's results differ: %v", tc.name, res)
				}
				return res[0]
			},
		}
		for how, f := range read {
			if got := factTexts(f("edge")); !slices.Equal(got, edges) {
				t.Errorf("%s %s(edge) = %v, want %v", tc.name, how, got, edges)
			}
			if got := factTexts(f("weight")); !slices.Equal(got, weights) {
				t.Errorf("%s %s(weight) = %v, want %v", tc.name, how, got, weights)
			}
		}
		// The base never changes.
		if got := len(tc.base.Tuples("edge")); got != 2 {
			t.Errorf("%s: the base holds %d edges", tc.name, got)
		}
	}
	// control: the store's reads went through its own interfaces.
	st := &storeStub{MemSource: overlayBase()}
	o := Overlay(st, session())
	for range o.TuplesSeq(context.Background(), "edge") {
	}
	o.Lookup(context.Background(), "edge", map[int]Value{0: S("a")})
	o.LookupBatch(context.Background(), "edge", []int{0}, [][]Value{{S("a")}})
	if st.streamed != 1 || st.lookups != 1 || st.batches != 1 {
		t.Errorf("control: streamed %d, looked up %d, batched %d", st.streamed, st.lookups, st.batches)
	}
	// weight isn't looked up by the store, so neither does the overlay, and a batch asks per value.
	if o.LooksUp("weight") || !o.LooksUp("edge") || Overlay(overlayBase(), session()).LooksUp("edge") {
		t.Error("LooksUp")
	}
	o.LookupBatch(context.Background(), "weight", []int{0}, [][]Value{{S("a")}})
	if st.batches != 1 || st.lookups != 1 {
		t.Errorf("weight's batch reached the store's batch or lookup: batched %d, looked up %d", st.batches, st.lookups)
	}
}

func TestAnOverlayPassesOnlyUntouchedWorkToTheBase(t *testing.T) {
	o := Overlay(&storeStub{MemSource: overlayBase().Declare("attr", "id", "v")}, session())
	plain := Overlay(overlayBase().Declare("attr", "id", "v"), session())
	over := func(rels ...string) Conjunction {
		var c Conjunction
		for _, r := range rels {
			c.Atoms = append(c.Atoms, PushAtom{Relation: r, Args: []PushArg{{Var: 1}, {Var: 2}}})
		}
		return c
	}
	for _, tc := range []struct {
		what      string
		got, want bool
	}{
		{"push untouched", o.Pushes(over("attr")), true},
		{"push touched", o.Pushes(over("attr", "edge")), false},
		{"push, base doesn't", plain.Pushes(over("attr")), false},
		{"summary untouched", o.Summarizes(over("attr"), Summary{}), true},
		{"summary touched", o.Summarizes(over("weight"), Summary{}), false},
		{"summary, base doesn't", plain.Summarizes(over("attr"), Summary{}), false},
		{"walk untouched", o.Walks("attr", 0, 1, nil), true},
		{"walk touched", o.Walks("edge", 0, 1, nil), false},
		{"walk, base doesn't", plain.Walks("attr", 0, 1, nil), false},
		{"step untouched", o.WalksConjunction(over("attr", "attr"), 1, 2), true},
		{"step touched", o.WalksConjunction(over("attr", "edge"), 1, 2), false},
		{"step, base doesn't", plain.WalksConjunction(over("attr"), 1, 2), false},
		{"filter", o.PushesFilter("str.glob"), true},
		{"filter, base doesn't", plain.PushesFilter("str.glob"), false},
		{"indexed", o.Indexed("edge", []int{1}), false},
		{"indexed, base doesn't say", plain.Indexed("edge", []int{1}), true},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %t, want %t", tc.what, tc.got, tc.want)
		}
	}
}

func TestAnOverlaysVersionMovesWithItsEdits(t *testing.T) {
	st := &storeStub{MemSource: overlayBase(), version: "v7"}
	if got := Overlay(st, Changes{}).Version(); got != "v7" {
		t.Errorf("no edits: %q", got)
	}
	one := Overlay(st, session()).Version()
	if one == "v7" || one[:3] != "v7+" || one != Overlay(st, session()).Version() {
		t.Errorf("one session: %q", one)
	}
	recited := session()
	recited.Add("edge", Tuple{Vals: []Value{S("c"), S("d")}, Cites: []string{"edit:other"}})
	more := session()
	more.Remove("edge", Tuple{Vals: []Value{S("a"), S("b")}})
	for name, c := range map[string]Changes{"recited": recited, "more": more} {
		if v := Overlay(st, c).Version(); v == one {
			t.Errorf("%s: version unmoved, %q", name, v)
		}
	}
	// The overlay keeps its own copy of the change set.
	c := session()
	o := Overlay(st, c)
	c.Remove("edge", Tuple{Vals: []Value{S("a"), S("b")}})
	if o.Version() != one || len(o.Changes().Removed("edge")) != 1 {
		t.Errorf("the overlay followed an edit to its change set: %q", o.Version())
	}
}

func TestEffectiveKeepsOnlyEditsThatChangeTheBase(t *testing.T) {
	c := session()
	c.Remove("edge", Tuple{Vals: []Value{S("x"), S("y")}}). // the base doesn't hold it
								Add("edge", Tuple{Vals: []Value{S("a"), S("b")}, Cites: []string{"again"}}). // it does
								Add("weight", Tuple{Vals: []Value{S("b"), S("2")}})                          // it does, read as a number
	want := []string{`edge -"b" "c" id= []`, `edge +"c" "d" id= [edit:cd]`, `weight -"a" n:1 id= []`}
	for name, base := range map[string]Source{"plain": overlayBase(), "store": &storeStub{MemSource: overlayBase()}} {
		e, err := Overlay(base, c).Effective(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, rel := range e.Relations() {
			for _, f := range factTexts(e.Removed(rel)) {
				got = append(got, rel+" -"+f)
			}
			for _, f := range factTexts(e.Added(rel)) {
				got = append(got, rel+" +"+f)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: effective\n got %v\nwant %v", name, got, want)
		}
	}
}

func TestACommitIsTheVersionBeforeItWithTheChangesApplied(t *testing.T) {
	src := NewTimedMemSource(Span(0, 2))
	src.Declare("edge", "from", "to")
	src.Add("edge", Span(0, 2), []string{"e:ab"}, S("a"), S("b"))
	src.Add("edge", Span(1, 2), []string{"e:bc"}, S("b"), S("c"))
	src.Add("edge", Span(0, 1), []string{"e:cd@0"}, S("c"), S("d"))
	src.Add("edge", Since(0), []string{"e:xy"}, S("x"), S("y"))
	src.Add("edge", Since(0), []string{"e:yz"}, S("y"), S("z"))
	var edits Changes
	edits.Remove("edge", Tuple{Vals: []Value{S("b"), S("c")}}).
		Add("edge", Tuple{Vals: []Value{S("c"), S("d")}, Cites: []string{"edit:cd"}}).
		Add("edge", Tuple{Vals: []Value{S("x"), S("y")}, Cites: []string{"edit:xy"}}).
		Remove("edge", Tuple{Vals: []Value{S("y"), S("z")}}).
		Add("edge", Tuple{Vals: []Value{S("q"), S("r")}, Cites: []string{"edit:qr"}, ID: "qr"})
	before := src.Version()
	if err := src.Commit(edits, 1, 2); err != nil {
		t.Fatal(err)
	}
	if src.Version() == before || !src.Versions().Contains(2) {
		t.Errorf("version %q, versions %v", src.Version(), src.Versions())
	}
	at := func(v Version) []string {
		ts, _ := src.TimedTuples(context.Background(), "edge")
		var out []Tuple
		for _, tt := range ts {
			if tt.Versions.Contains(v) {
				out = append(out, Tuple{Vals: tt.Vals, Cites: tt.CitesAt(v), ID: tt.ID})
			}
		}
		return sorted(factTexts(out))
	}
	// A fact added with an open range holds in a new version only when the commit keeps it.
	if got, want := at(2), []string{`"a" "b" id= [e:ab]`, `"c" "d" id= [edit:cd]`, `"q" "r" id=qr [edit:qr]`, `"x" "y" id= [edit:xy]`}; !slices.Equal(got, want) {
		t.Errorf("at 2: %v, want %v", got, want)
	}
	if got, want := at(1), []string{`"a" "b" id= [e:ab]`, `"b" "c" id= [e:bc]`, `"x" "y" id= [e:xy]`, `"y" "z" id= [e:yz]`}; !slices.Equal(got, want) {
		t.Errorf("at 1: %v, want %v", got, want)
	}
	// A fact held in an earlier version is one TimedTuple, now holding in both.
	if ts, _ := src.TimedTuples(context.Background(), "edge"); len(ts) != 6 {
		t.Errorf("%d timed tuples", len(ts))
	}
	wantErr(t, src.Commit(edits, 5, 6), "query: commit: version 5 is not one the source serves")
	wantErr(t, src.Commit(edits, 2, 1), "query: commit: version 1 is one the source serves already")
	var unknown Changes
	wantErr(t, src.Commit(*unknown.Add("edgy", Tuple{Vals: []Value{S("a")}}), 2, 3), `query: commit: the source declares no relation "edgy"`)
}
