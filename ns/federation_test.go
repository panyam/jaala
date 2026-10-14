package ns

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type fixedVersion struct {
	*MemSource
	v string
}

func (s *fixedVersion) Version() string { return s.v }

// Relations lists every member's relations once, in member order, and a relation two members serve
// with different schemas is refused, naming both.
func TestNewFederationUnionsRelationsAndRefusesAConflict(t *testing.T) {
	a := NewMemSource().Declare("node", "id").Declare("edge", "src", "dst")
	b := NewMemSource().Declare("attr", "id", "key").Declare("node", "id")
	f, err := NewFederation(Member{Source: a}, Member{Source: b})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Relations(); !reflect.DeepEqual(got, []string{"edge", "node", "attr"}) {
		t.Errorf("relations %v", got)
	}
	for _, c := range []*MemSource{
		NewMemSource().Declare("node", "id", "kind"),
		NewMemSource().Declare("node", "name"),
		NewMemSource().DeclareSchema("node", Schema{Arity: 1, Labels: []string{"id"}, Types: []ArgType{{Type: "number"}}}),
	} {
		_, err := NewFederation(Member{Name: "repo", Source: a}, Member{Source: c})
		if err == nil || err.Error() != `query: federation: node has a different schema in member #1 than in member "repo"` {
			t.Errorf("err %v", err)
		}
	}
	inner, _ := NewFederation(Member{Source: a})
	outer, _ := NewFederation(Member{Source: inner})
	if outer.LooksUp("node") {
		t.Error("a federation of MemSources looks node up")
	}
	if _, err := NewFederation(Member{Source: a}, Member{Source: NewMemSource().DeclareSchema("node", Schema{Arity: 1, Labels: []string{"id"}, Doc: "other"})}); err != nil {
		t.Errorf("a different Doc is refused: %v", err)
	}
}

// The version changes when any member's does, and two different splits of versions never read alike.
func TestAFederationsVersionComposesItsMembers(t *testing.T) {
	x := &fixedVersion{MemSource: NewMemSource(), v: "a"}
	y := &fixedVersion{MemSource: NewMemSource(), v: "bc"}
	f, _ := NewFederation(Member{Source: x}, Member{Source: NewMemSource()}, Member{Source: y})
	before := f.Version()
	y.v = "bd"
	if f.Version() == before {
		t.Error("a member's version moved and the federation's didn't")
	}
	x.v, y.v = "ab", "c"
	g, _ := NewFederation(Member{Source: x}, Member{Source: NewMemSource()}, Member{Source: y})
	if g.Version() == before {
		t.Errorf("%q and %q split differently but read alike", "a|bc", "ab|c")
	}
}

// HoldsPrefix refuses a call binding a named position to a value without the prefix, and holds
// anything else.
func TestHoldsPrefix(t *testing.T) {
	h := HoldsPrefix("repo1/", map[string][]int{"edge": {0}})
	for _, c := range []struct {
		rel   string
		bound map[int]Value
		want  bool
	}{
		{"edge", map[int]Value{0: S("repo1/x")}, true},
		{"edge", map[int]Value{0: S("repo2/x")}, false},
		{"edge", map[int]Value{1: S("repo2/x")}, true},
		{"node", map[int]Value{0: S("repo2/x")}, true},
	} {
		if got := h(c.rel, c.bound); got != c.want {
			t.Errorf("%s %v: %v", c.rel, c.bound, got)
		}
	}
}

// A named member's tuples cite it, whole and looked up; an unnamed member's tuples are its own.
func TestAFederationNamesItsMembersInCitations(t *testing.T) {
	a := NewMemSource().Declare("node", "id")
	a.Add("node", Tuple{Vals: []Value{S("x")}, Cites: []string{"f.go:1"}})
	b := NewMemSource().Declare("node", "id")
	b.Add("node", Tuple{Vals: []Value{S("y")}, Cites: []string{"g.go:2"}})
	f, _ := NewFederation(Member{Name: "left", Source: a}, Member{Source: b})
	var cites []string
	for _, t := range f.Tuples("node") {
		cites = append(cites, t.Cites...)
	}
	if got := strings.Join(cites, " "); got != "left:f.go:1 g.go:2" {
		t.Errorf("cites %q", got)
	}
	if a.Tuples("node")[0].Cites[0] != "f.go:1" {
		t.Error("naming a member rewrote its Source's own tuples")
	}
}

type catalog map[string][]int

func (c catalog) Indexed(rel string, bound []int) bool {
	for _, p := range bound {
		if slices.Contains(c[rel], p) {
			return true
		}
	}
	return false
}

type cataloguedMem struct {
	*MemSource
	catalog
}

// A pattern is indexed in a federation when every member serving its relation indexes it; a member
// that doesn't say counts as indexing it.
func TestAFederationIndexesAPatternOnlyWhenEveryMemberDoes(t *testing.T) {
	mem := func() *MemSource { return NewMemSource().Declare("edge", "src", "dst") }
	bySrc := cataloguedMem{mem(), catalog{"edge": {0}}}
	byBoth := cataloguedMem{mem(), catalog{"edge": {0, 1}}}
	f, _ := NewFederation(Member{Source: bySrc}, Member{Source: byBoth}, Member{Source: mem()})
	if !f.Indexed("edge", []int{0}) {
		t.Error("edge by src is indexed in every member that says")
	}
	if f.Indexed("edge", []int{1}) {
		t.Error("edge by dst is indexed in one member and scanned in another, so it's a scan")
	}
	g, _ := NewFederation(Member{Source: byBoth}, Member{Source: mem()})
	if !g.Indexed("edge", []int{1}) {
		t.Error("control: edge by dst, indexed in the only member that says")
	}
}

// lookupMem answers Lookup by scanning, counting its calls.
type lookupMem struct {
	*MemSource
	calls int
}

func (m *lookupMem) Lookup(_ context.Context, rel string, bound map[int]Value) ([]Tuple, error) {
	m.calls++
	var out []Tuple
	for _, t := range m.Tuples(rel) {
		ok := true
		for i, v := range bound {
			ok = ok && t.Vals[i].S == v.S
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// batchMem is a lookupMem that also answers a batch in one call, recording what each batch asked.
type batchMem struct {
	*lookupMem
	asked [][]string
}

func (m *batchMem) LookupBatch(ctx context.Context, rel string, positions []int, values [][]Value) ([][]Tuple, error) {
	var asked []string
	out := make([][]Tuple, len(values))
	for k, vals := range values {
		asked = append(asked, vals[0].S)
		out[k], _ = m.lookupMem.Lookup(ctx, rel, map[int]Value{positions[0]: vals[0]})
	}
	m.lookupMem.calls -= len(values)
	m.asked = append(m.asked, asked)
	return out, nil
}

// A batch goes to each member serving the relation once, carrying only the values its Holds allows: in
// one call to a member that batches, one Lookup per value to one that only looks up. Each value's
// result is its members' answers in member order, cited by member. A batch to a member asked for
// none of its values is never made.
func TestAFederationBatchesPerMember(t *testing.T) {
	mem := func(ids ...string) *MemSource {
		m := NewMemSource().Declare("node", "id")
		for _, id := range ids {
			m.Add("node", Tuple{Vals: []Value{S(id)}, Cites: []string{"f"}})
		}
		return m
	}
	x := &batchMem{lookupMem: &lookupMem{MemSource: mem("x/1", "x/2")}}
	y := &lookupMem{MemSource: mem("y/1", "y/2")}
	z := &batchMem{lookupMem: &lookupMem{MemSource: mem("z/1")}}
	f, _ := NewFederation(
		Member{Name: "x", Source: x, Holds: HoldsPrefix("x/", map[string][]int{"node": {0}})},
		Member{Name: "y", Source: y, Holds: HoldsPrefix("y/", map[string][]int{"node": {0}})},
		Member{Name: "z", Source: z, Holds: HoldsPrefix("z/", map[string][]int{"node": {0}})},
	)
	got, err := f.LookupBatch(context.Background(), "node", []int{0}, [][]Value{{S("x/1")}, {S("y/2")}, {S("x/3")}, {S("y/1")}})
	if err != nil {
		t.Fatal(err)
	}
	var cites []string
	for _, ts := range got {
		var c []string
		for _, t := range ts {
			c = append(c, t.Vals[0].S+"@"+strings.Join(t.Cites, ","))
		}
		cites = append(cites, strings.Join(c, " "))
	}
	if want := []string{"x/1@x:f", "y/2@y:f", "", "y/1@y:f"}; !reflect.DeepEqual(cites, want) {
		t.Errorf("results %q, want %q", cites, want)
	}
	if !reflect.DeepEqual(x.asked, [][]string{{"x/1", "x/3"}}) || x.calls != 0 || y.calls != 2 || len(z.asked) != 0 {
		t.Errorf("x batched %v with %d lookups, y looked up %d times, z batched %v", x.asked, x.calls, y.calls, z.asked)
	}
}

// streamingMem streams its MemSource's tuples, counting those yielded, and fails a read when failing.
type streamingMem struct {
	*MemSource
	yielded int
	failing bool
}

func (s *streamingMem) TuplesSeq(_ context.Context, rel string) iter.Seq2[Tuple, error] {
	return func(yield func(Tuple, error) bool) {
		if s.failing {
			yield(Tuple{}, errors.New("disk gone"))
			return
		}
		for _, t := range s.Tuples(rel) {
			s.yielded++
			if !yield(t, nil) {
				return
			}
		}
	}
}

// countingMem counts its whole reads.
type countingMem struct {
	*MemSource
	reads int
}

func (s *countingMem) Tuples(rel string) []Tuple {
	s.reads++
	return s.MemSource.Tuples(rel)
}

// A Federation streams its members in order, a streaming member one tuple at a time and any other
// read whole, each tuple cited to its member, and stops reading once its consumer stops (#233).
func TestAFederationStreamsItsMembers(t *testing.T) {
	a := &streamingMem{MemSource: NewMemSource().Declare("node", "id")}
	for _, id := range []string{"a1", "a2", "a3"} {
		a.Add("node", Tuple{Vals: []Value{S(id)}, Cites: []string{id + ".go"}})
	}
	b := &countingMem{MemSource: NewMemSource().Declare("node", "id")}
	b.Add("node", Tuple{Vals: []Value{S("b1")}, Cites: []string{"b1.go"}})
	f, _ := NewFederation(Member{Name: "a", Source: a}, Member{Name: "b", Source: b})

	var got []string
	for tu, err := range f.TuplesSeq(context.Background(), "node") {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, tu.Vals[0].S+"@"+strings.Join(tu.Cites, ","))
	}
	if want := []string{"a1@a:a1.go", "a2@a:a2.go", "a3@a:a3.go", "b1@b:b1.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("streamed %v, want %v", got, want)
	}

	a.yielded, b.reads = 0, 0
	for range f.TuplesSeq(context.Background(), "node") {
		break
	}
	if a.yielded != 1 || b.reads != 0 {
		t.Errorf("stopping after one tuple yielded %d from a and read b %d times, want 1 and 0", a.yielded, b.reads)
	}

	a.failing = true
	var failed error
	for _, err := range f.TuplesSeq(context.Background(), "node") {
		failed = err
	}
	if failed == nil || !strings.Contains(failed.Error(), `member "a": disk gone`) {
		t.Errorf("a failing member streamed %v, want its error named", failed)
	}
}

// pushMem is a MemSource that pushes every conjunction, answering each with every tuple of its first
// atom's relation as a one-tuple row, or failing.
type pushMem struct {
	*MemSource
	fail error
}

func (m *pushMem) Pushes(Conjunction) bool { return true }

func (m *pushMem) Push(_ context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	return func(yield func([]Tuple, error) bool) {
		if m.fail != nil {
			yield(nil, m.fail)
			return
		}
		for _, t := range m.Tuples(c.Atoms[0].Relation) {
			if !yield([]Tuple{t}, nil) {
				return
			}
		}
	}
}

// A Federation pushes a conjunction only to a lone member that pushes, citing each tuple to it, and
// wraps the member's failure with its name.
func TestAFederationPushesToALoneMember(t *testing.T) {
	mem := NewMemSource().Declare("edge", "from", "to")
	mem.Add("edge", Tuple{Vals: []Value{S("a"), S("b")}, Cites: []string{"e1"}})
	c := Conjunction{Atoms: []PushAtom{{Relation: "edge", Args: []PushArg{{Var: 1}, {Var: 2}}}}}
	pm := &pushMem{MemSource: mem}
	lone, err := NewFederation(Member{Name: "repo0", Source: pm})
	if err != nil {
		t.Fatal(err)
	}
	if !lone.Pushes(c) {
		t.Fatalf("a federation of one pushing member refuses to push")
	}
	var rows [][]Tuple
	for row, err := range lone.Push(context.Background(), c) {
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0][0].Cites, []string{"repo0:e1"}) {
		t.Errorf("pushed %v, want the edge cited to repo0", rows)
	}
	pm.fail = errors.New("locked")
	for _, err := range lone.Push(context.Background(), c) {
		if err == nil || !errors.Is(err, pm.fail) || !strings.Contains(err.Error(), `member "repo0": `) {
			t.Errorf("a failing member's push gave %v, want its error wrapped with its name", err)
		}
	}
	two, err := NewFederation(Member{Name: "repo0", Source: pm}, Member{Name: "repo1", Source: &pushMem{MemSource: mem}})
	if err != nil {
		t.Fatal(err)
	}
	if two.Pushes(c) {
		t.Errorf("a federation of two members pushes a conjunction, whose join could span them (#238)")
	}
	plain, err := NewFederation(Member{Name: "repo0", Source: mem})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Pushes(c) {
		t.Errorf("a federation whose lone member doesn't push pushes")
	}
}
