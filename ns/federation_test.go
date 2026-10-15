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

// joinMem is a MemSource that evaluates conjunctions by a nested-loop join comparing values by their
// text, returning every prefix of its matches when asked (Partial) unless noPartial, and counting its
// pushes.
type joinMem struct {
	*MemSource
	noPartial bool
	noInputs  bool
	pushes    int
	pushed    []Conjunction
}

func (m *joinMem) Pushes(c Conjunction) bool {
	return (!c.Partial || !m.noPartial) && (len(c.Inputs) == 0 || !m.noInputs)
}

func (m *joinMem) Push(_ context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	m.pushes++
	m.pushed = append(m.pushed, c)
	var rows [][]Tuple
	vals := map[int]string{}
	row := make([]Tuple, len(c.Atoms))
	var walk func(k int)
	walk = func(k int) {
		if c.Partial && k > 0 && k < len(c.Atoms) {
			rows = append(rows, slices.Clone(row[:k]))
		}
		if k == len(c.Atoms) {
			if !c.PrefixesOnly {
				rows = append(rows, slices.Clone(row))
			}
			return
		}
		for _, t := range m.Tuples(c.Atoms[k].Relation) {
			ok, set := true, []int{}
			for i, x := range c.Atoms[k].Args {
				v := t.Vals[i].S
				switch w, has := vals[x.Var]; {
				case x.Const != nil:
					ok = ok && x.Const.S == v
				case x.Var > 0 && has:
					ok = ok && w == v
				case x.Var > 0:
					vals[x.Var] = v
					set = append(set, x.Var)
				}
			}
			if ok {
				row[k] = t
				walk(k + 1)
			}
			for _, n := range set {
				delete(vals, n)
			}
		}
	}
	walk(0)
	rows = slices.DeleteFunc(rows, func(r []Tuple) bool { return !agrees(c, r) })
	return func(yield func([]Tuple, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

// agrees reports whether row, a match of c or a prefix of one, agrees with one of c's Bindings on the
// Inputs its atoms bind, or c has none.
func agrees(c Conjunction, row []Tuple) bool {
	if len(c.Bindings) == 0 {
		return true
	}
	return slices.ContainsFunc(c.Bindings, func(b []Value) bool {
		for j, n := range c.Inputs {
		find:
			for k, a := range c.Atoms[:len(row)] {
				for pos, x := range a.Args {
					if x.Var == n {
						if row[k].Vals[pos].S != b[j].S {
							return false
						}
						break find
					}
				}
			}
		}
		return true
	})
}

// Two stores partitioned by an edge's source, A holding a1's edges and a2's, B holding b9's: two
// hops from a1 reach a3 within A and b10 through B.
func partitionedEdges(holds bool) (*joinMem, *joinMem, []Member) {
	edge := func(m *MemSource, from, to string) {
		m.Add("edge", Tuple{Vals: []Value{S(from), S(to)}, Cites: []string{from + "-" + to}})
	}
	a, b := &joinMem{MemSource: NewMemSource().Declare("edge", "from", "to")}, &joinMem{MemSource: NewMemSource().Declare("edge", "from", "to")}
	edge(a.MemSource, "a1", "a2")
	edge(a.MemSource, "a2", "a3")
	edge(a.MemSource, "a1", "b9")
	edge(b.MemSource, "b9", "b10")
	members := []Member{{Name: "A", Source: a}, {Name: "B", Source: b}}
	if holds {
		members[0].Holds = HoldsPrefix("a", map[string][]int{"edge": {0}})
		members[1].Holds = HoldsPrefix("b", map[string][]int{"edge": {0}})
	}
	return a, b, members
}

func pushedRows(t *testing.T, f *Federation, c Conjunction) []string {
	t.Helper()
	var out []string
	for row, err := range f.Push(context.Background(), c) {
		if err != nil {
			t.Fatal(err)
		}
		var cites []string
		for _, tu := range row {
			cites = append(cites, tu.Cites...)
		}
		out = append(out, strings.Join(cites, " "))
	}
	slices.Sort(out)
	return out
}

// A partitioned federation pushes a conjunction across its members: each answers the matches it
// holds whole, and a prefix leaving it continues in the member Holds routes the next atom to, so a
// join spanning members is answered whole, in a call per member asked. A loose Holds costs calls,
// never matches.
func TestAPartitionedFederationPushesAcrossMembers(t *testing.T) {
	a1 := S("a1")
	c := Conjunction{Atoms: []PushAtom{
		{Relation: "edge", Args: []PushArg{{Const: &a1}, {Var: 1}}},
		{Relation: "edge", Args: []PushArg{{Var: 1}, {Var: 2}}},
	}}
	want := []string{"A:a1-a2 A:a2-a3", "A:a1-b9 B:b9-b10"}
	for _, holds := range []bool{true, false} {
		a, b, members := partitionedEdges(holds)
		f, err := NewPartitionedFederation(members...)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Pushes(c) {
			t.Fatalf("holds %v: a partitioned federation refuses to push", holds)
		}
		if got := pushedRows(t, f, c); !reflect.DeepEqual(got, want) {
			t.Errorf("holds %v: pushed %q, want %q", holds, got, want)
		}
		if holds && (a.pushes != 1 || b.pushes != 1) {
			t.Errorf("A was pushed %d times and B %d, want once each", a.pushes, b.pushes)
		}
		if !holds && a.pushes+b.pushes <= 2 {
			t.Errorf("control: with no Holds, A was pushed %d times and B %d, want more than once each", a.pushes, b.pushes)
		}
	}
	_, _, members := partitionedEdges(true)
	plain, _ := NewFederation(members...)
	if plain.Pushes(c) {
		t.Errorf("a federation not declared partitioned pushes a conjunction across members")
	}
	a, _, members := partitionedEdges(true)
	a.noPartial = true
	if f, _ := NewPartitionedFederation(members...); f.Pushes(c) {
		t.Errorf("a partitioned federation pushes though a member can't return prefixes")
	}
	_, b, members := partitionedEdges(true)
	members[1].Source = b.MemSource
	if f, _ := NewPartitionedFederation(members...); f.Pushes(c) {
		t.Errorf("a partitioned federation pushes though a member doesn't evaluate conjunctions")
	}
	_, _, members = partitionedEdges(true)
	members = append(members, Member{Name: "C", Source: &joinMem{MemSource: NewMemSource().Declare("node", "id")}})
	if f, _ := NewPartitionedFederation(members...); f.Pushes(c) {
		t.Errorf("a partitioned federation pushes though a member doesn't serve edge")
	}
}

// A partitioned federation pushes a conjunction with Inputs (#268): each binding goes to the members
// whose Holds allows the first atom under its values, each asked once for all of its bindings, and a
// prefix leaving a member continues as before. The answer is each binding's matches, whatever Holds
// routes; a federation not declared partitioned, or with a member refusing Inputs, declines.
func TestAPartitionedFederationPushesBindingsAcrossMembers(t *testing.T) {
	c := Conjunction{
		Atoms: []PushAtom{
			{Relation: "edge", Args: []PushArg{{Var: 1}, {Var: 2}}},
			{Relation: "edge", Args: []PushArg{{Var: 2}, {Var: 3}}},
		},
		Inputs:   []int{1},
		Bindings: [][]Value{{S("a1")}, {S("b9")}},
	}
	offered := c
	offered.Bindings = nil
	want := []string{"A:a1-a2 A:a2-a3", "A:a1-b9 B:b9-a2", "A:a1-b9 B:b9-b10", "B:b9-a2 A:a2-a3"}
	for _, holds := range []bool{true, false} {
		a, b, members := partitionedEdges(holds)
		b.Add("edge", Tuple{Vals: []Value{S("b9"), S("a2")}, Cites: []string{"b9-a2"}})
		f, err := NewPartitionedFederation(members...)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Pushes(offered) {
			t.Fatalf("holds %v: a partitioned federation refuses a conjunction with Inputs", holds)
		}
		if got := pushedRows(t, f, c); !reflect.DeepEqual(got, want) {
			t.Errorf("holds %v: pushed %q, want %q", holds, got, want)
		}
		firsts := func(m *joinMem) (n int, bound [][]Value) {
			for _, p := range m.pushed {
				if len(p.Bindings) > 0 {
					n++
					bound = append(bound, p.Bindings...)
				}
			}
			return n, bound
		}
		an, ab := firsts(a)
		bn, bb := firsts(b)
		switch {
		case holds && (an != 1 || bn != 1 || !reflect.DeepEqual(ab, [][]Value{{S("a1")}}) || !reflect.DeepEqual(bb, [][]Value{{S("b9")}})):
			t.Errorf("A was asked %d times for %v and B %d for %v, want once each for its own binding", an, ab, bn, bb)
		case !holds && (an != 1 || bn != 1 || len(ab) != 2 || len(bb) != 2):
			t.Errorf("control: with no Holds, A was asked %d times for %v and B %d for %v, want once each for both", an, ab, bn, bb)
		}
	}
	_, _, members := partitionedEdges(true)
	if plain, _ := NewFederation(members...); plain.Pushes(offered) {
		t.Errorf("a federation not declared partitioned pushes a conjunction with Inputs across members")
	}
	a, _, members := partitionedEdges(true)
	a.noInputs = true
	if f, _ := NewPartitionedFederation(members...); f.Pushes(offered) {
		t.Errorf("a partitioned federation pushes Inputs though a member refuses them")
	}
	paged := offered
	paged.Page = &Page{Columns: []int{1}, Limit: 1}
	_, _, members = partitionedEdges(true)
	if f, _ := NewPartitionedFederation(members...); f.Pushes(paged) {
		t.Errorf("a partitioned federation pushes a page with Inputs")
	}
}

// walkMem is a MemSource that walks a relation by its values' text, counting its walks.
type walkMem struct {
	*MemSource
	walks  int
	refuse bool
}

func (m *walkMem) Walks(string, int, int, []int) bool { return !m.refuse }

func (m *walkMem) Walk(_ context.Context, w Walk) iter.Seq2[Tuple, error] {
	m.walks++
	var out []Tuple
	seen := map[string]bool{}
	queue := append([]Value(nil), w.Start...)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if seen[v.S] {
			continue
		}
		seen[v.S] = true
		for _, t := range m.Tuples(w.Relation) {
			if t.Vals[w.From].S == v.S {
				out = append(out, t)
				queue = append(queue, t.Vals[w.To])
			}
		}
	}
	return func(yield func(Tuple, error) bool) {
		for _, t := range out {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// A Federation walks across its members: a value leaving one goes on in the member Holds routes it
// to, and back, each member walking a value once, so every fact the walk reaches comes back once,
// cited to its member. A loose Holds costs walks, never facts.
func TestAFederationWalksAcrossMembers(t *testing.T) {
	next := func(m *MemSource, from, to string) {
		m.Add("next", Tuple{Vals: []Value{S(from), S(to)}, Cites: []string{from + "-" + to}})
	}
	want := []string{"A:a1-a2", "A:a2-b1", "A:a3-a4", "B:b1-b2", "B:b2-a2", "B:b2-a3"}
	for _, holds := range []bool{true, false} {
		a := &walkMem{MemSource: NewMemSource().Declare("next", "src", "dst")}
		b := &walkMem{MemSource: NewMemSource().Declare("next", "src", "dst")}
		next(a.MemSource, "a1", "a2")
		next(a.MemSource, "a2", "b1")
		next(a.MemSource, "a3", "a4")
		next(b.MemSource, "b1", "b2")
		next(b.MemSource, "b2", "a3")
		next(b.MemSource, "b2", "a2") // back to a value A already walked
		members := []Member{{Name: "A", Source: a}, {Name: "B", Source: b}}
		if holds {
			members[0].Holds = HoldsPrefix("a", map[string][]int{"next": {0}})
			members[1].Holds = HoldsPrefix("b", map[string][]int{"next": {0}})
		}
		f, err := NewFederation(members...)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Walks("next", 0, 1, nil) {
			t.Fatalf("a federation of walking members doesn't walk")
		}
		var got []string
		for tu, err := range f.Walk(context.Background(), Walk{Relation: "next", From: 0, To: 1, Start: []Value{S("a1")}}) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, strings.Join(tu.Cites, " "))
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("holds %v: walked %q, want %q", holds, got, want)
		}
		if holds && (a.walks != 2 || b.walks != 1) {
			t.Errorf("A walked %d times and B %d, want twice (out and back) and once", a.walks, b.walks)
		}
		if !holds && a.walks+b.walks <= 3 {
			t.Errorf("control: with no Holds, A walked %d times and B %d, want more", a.walks, b.walks)
		}
	}
	plain := NewMemSource().Declare("next", "src", "dst")
	f, _ := NewFederation(Member{Source: &walkMem{MemSource: plain}}, Member{Source: plain})
	if f.Walks("next", 0, 1, nil) {
		t.Errorf("a federation walks though a member doesn't")
	}
	f, _ = NewFederation(Member{Source: &walkMem{MemSource: plain}}, Member{Source: &walkMem{MemSource: plain, refuse: true}})
	if f.Walks("next", 0, 1, nil) {
		t.Errorf("a federation walks though a member refuses the walk")
	}
	// A leaf A reached itself, routed back from B, isn't walked again.
	a := &walkMem{MemSource: NewMemSource().Declare("next", "src", "dst")}
	b := &walkMem{MemSource: NewMemSource().Declare("next", "src", "dst")}
	next(a.MemSource, "a1", "a9")
	next(a.MemSource, "a1", "b1")
	next(b.MemSource, "b1", "a9")
	f, _ = NewFederation(Member{Source: a, Holds: HoldsPrefix("a", map[string][]int{"next": {0}})}, Member{Source: b, Holds: HoldsPrefix("b", map[string][]int{"next": {0}})})
	n := 0
	for _, err := range f.Walk(context.Background(), Walk{Relation: "next", From: 0, To: 1, Start: []Value{S("a1")}}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 3 || a.walks != 1 || b.walks != 1 {
		t.Errorf("walked %d facts, A %d times and B %d, want 3 facts and one walk each: A reached a9 already", n, a.walks, b.walks)
	}
}

// summaryMem is a joinMem that also summarizes, as a row per fact, counting its summaries.
type summaryMem struct {
	*joinMem
	summaries int
}

func (m *summaryMem) Summarizes(Conjunction, Summary) bool { return true }

func (m *summaryMem) Summarize(ctx context.Context, c Conjunction, _ Summary) iter.Seq2[SummaryRow, error] {
	m.summaries++
	return func(yield func(SummaryRow, error) bool) {
		for row := range m.joinMem.Push(ctx, c) {
			var cites []string
			for _, t := range row {
				cites = append(cites, t.Cites...)
			}
			if !yield(SummaryRow{Matches: 1, Cites: cites}, nil) {
				return
			}
		}
	}
}

// A page or a summary of one atom goes to every member whose Holds allows it, partitioned or not,
// each cited to its member; of a join, to none, since a join could pair facts across members.
func TestAFederationPagesAndSummarizesOneAtom(t *testing.T) {
	_, _, members := partitionedEdges(true)
	a := &summaryMem{joinMem: members[0].Source.(*joinMem)}
	b := &summaryMem{joinMem: members[1].Source.(*joinMem)}
	members[0].Source, members[1].Source = a, b
	f, err := NewFederation(members...)
	if err != nil {
		t.Fatal(err)
	}
	one := Conjunction{Atoms: []PushAtom{{Relation: "edge", Args: []PushArg{{Var: 1}, {Var: 2}}}}}
	if !f.Summarizes(one, Summary{}) {
		t.Fatalf("a federation won't summarize one atom")
	}
	var cites []string
	for row, err := range f.Summarize(context.Background(), one, Summary{}) {
		if err != nil {
			t.Fatal(err)
		}
		cites = append(cites, row.Cites...)
	}
	slices.Sort(cites)
	if want := []string{"A:a1-a2", "A:a1-b9", "A:a2-a3", "B:b9-b10"}; !reflect.DeepEqual(cites, want) || a.summaries != 1 || b.summaries != 1 {
		t.Errorf("summarized %q from %d and %d summaries, want %q from one each", cites, a.summaries, b.summaries, want)
	}
	a1 := S("a1")
	fromA1 := Conjunction{Atoms: []PushAtom{{Relation: "edge", Args: []PushArg{{Const: &a1}, {Var: 1}}}}}
	a.summaries, b.summaries = 0, 0
	for range f.Summarize(context.Background(), fromA1, Summary{}) {
	}
	if a.summaries != 1 || b.summaries != 0 {
		t.Errorf("summarizing a1's edges asked A %d times and B %d, want only A, whose Holds allows it", a.summaries, b.summaries)
	}
	paged := one
	paged.Page = &Page{Columns: []int{1}, Limit: 1}
	if !f.Pushes(paged) {
		t.Errorf("a federation won't page one atom")
	}
	join := Conjunction{Atoms: append(append([]PushAtom(nil), one.Atoms...), PushAtom{Relation: "edge", Args: []PushArg{{Var: 2}, {Var: 3}}})}
	if f.Summarizes(join, Summary{}) {
		t.Errorf("a federation summarizes a join across members")
	}
	join.Page = paged.Page
	if f.Pushes(join) {
		t.Errorf("a federation pages a join across members")
	}
	members[1].Source = b.joinMem
	if g, _ := NewFederation(members...); g.Summarizes(one, Summary{}) {
		t.Errorf("a federation summarizes though a member doesn't")
	}
}

// A partitioned federation summarizes a join across its members: each member's own matches, then the
// matches spanning members, continued from each member's prefixes, so two hops from a1 count both,
// the one through b9 cited to A and B (#253). It won't report extremes for a join, nor summarize one
// unpartitioned.
func TestAPartitionedFederationSummarizesAJoin(t *testing.T) {
	_, _, members := partitionedEdges(true)
	a := &summaryMem{joinMem: members[0].Source.(*joinMem)}
	b := &summaryMem{joinMem: members[1].Source.(*joinMem)}
	members[0].Source, members[1].Source = a, b
	f, err := NewPartitionedFederation(members...)
	if err != nil {
		t.Fatal(err)
	}
	a1 := S("a1")
	c := Conjunction{Atoms: []PushAtom{
		{Relation: "edge", Args: []PushArg{{Const: &a1}, {Var: 1}}},
		{Relation: "edge", Args: []PushArg{{Var: 1}, {Var: 2}}},
	}}
	if !f.Summarizes(c, Summary{}) {
		t.Fatalf("a partitioned federation won't summarize a join")
	}
	var got []string
	matches := 0
	for row, err := range f.Summarize(context.Background(), c, Summary{}) {
		if err != nil {
			t.Fatal(err)
		}
		matches += row.Matches
		got = append(got, strings.Join(row.Cites, " "))
	}
	slices.Sort(got)
	if want := []string{"A:a1-a2 A:a2-a3", "A:a1-b9 B:b9-b10"}; matches != 2 || !reflect.DeepEqual(got, want) {
		t.Errorf("summarized %d matches %q, want 2: %q", matches, got, want)
	}
	if f.Summarizes(c, Summary{Extremes: []int{2}}) {
		t.Errorf("a partitioned federation reports extremes for a join")
	}
	plain, _ := NewFederation(members...)
	if plain.Summarizes(c, Summary{}) {
		t.Errorf("control: a federation not declared partitioned summarizes a join")
	}
}
