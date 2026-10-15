package datalog

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// splitInto deals src's tuples round-robin into n MemSources declaring every relation src serves, and
// wraps member i in looking when look(i), so a relation's facts are spread over stores that differ in
// whether they answer lookups.
func splitInto(src ns.Source, n int, look func(i int) bool) []ns.Member {
	mems := make([]*ns.MemSource, n)
	for i := range mems {
		mems[i] = ns.NewMemSource()
		for _, rel := range src.Relations() {
			s, _ := src.Schema(rel)
			mems[i].DeclareSchema(rel, s)
		}
	}
	for _, rel := range src.Relations() {
		for k, t := range src.Tuples(rel) {
			mems[k%n].Add(rel, t)
		}
	}
	out := make([]ns.Member, n)
	for i, m := range mems {
		out[i].Source = m
		if look(i) {
			out[i].Source = lookingAt(m)
		}
	}
	return out
}

func federate(t *testing.T, members ...ns.Member) *ns.Federation {
	t.Helper()
	f, err := ns.NewFederation(members...)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A federation of the graph's facts answers every lookup query as the graph does, whether its members
// all look up, none do, or some do, in all three evaluators and under CanonicalCites (#211). Each
// query gets its own Base, since one reading a relation whole would answer the next from its index.
func TestAFederationAnswersAsOneSourceOfItsFacts(t *testing.T) {
	v := std(graph())
	for name, look := range map[string]func(int) bool{
		"all look up": func(int) bool { return true },
		"none do":     func(int) bool { return false },
		"some do":     func(i int) bool { return i == 0 },
	} {
		for _, text := range lookupQueries {
			b := MustBase(v, federate(t, splitInto(graph(), 3, look)...))
			q := mustParse(t, text)
			want, werr := both(q, baseFor(v))
			got, gerr := both(q, b)
			if !reflect.DeepEqual(binds(got), binds(want)) || errText(gerr) != errText(werr) {
				t.Errorf("%s: %s\n federated: %v %v\n one:       %v %v", name, text, binds(got), gerr, binds(want), werr)
			}
			w, _ := Naive{}.Eval(bg, q, baseFor(v), CanonicalCites(), Witnesses())
			g, _ := Naive{}.Eval(bg, q, b, CanonicalCites(), Witnesses())
			if !reflect.DeepEqual(w, g) {
				t.Errorf("%s: %s cites differently\n federated: %v\n one:       %v", name, text, g, w)
			}
		}
	}
}

// fiftyRepos is one node relation over fifty stores, store i holding repo i's ids, each with its own
// looking wrapper so a test can see which were asked.
func fiftyRepos(holds bool) ([]*looking, *ns.Federation, *ns.Vocabulary) {
	var lookers []*looking
	var members []ns.Member
	for i := range 50 {
		m := ns.NewMemSource().Declare("node", "id", "kind")
		for k := range 3 {
			m.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("repo%d/n%d", i, k)), ns.S("func")}})
		}
		l := lookingAt(m)
		lookers = append(lookers, l)
		mem := ns.Member{Source: l}
		if holds {
			mem.Holds = ns.HoldsPrefix(fmt.Sprintf("repo%d/", i), map[string][]int{"node": {0}})
		}
		members = append(members, mem)
	}
	f, err := ns.NewFederation(members...)
	if err != nil {
		panic(err)
	}
	return lookers, f, ns.MustVocabulary(f)
}

// A lookup by an id goes only to the store that can hold it. control: without Holds, all fifty are
// asked.
func TestALookupGoesOnlyToTheMembersThatCanHoldAMatch(t *testing.T) {
	for _, holds := range []bool{true, false} {
		lookers, f, v := fiftyRepos(holds)
		rows, err := both(mustParse(t, `node("repo7/n1", ?k) => ?k`), MustBase(v, f))
		if err != nil || col(rows, "k") != "func" {
			t.Fatalf("holds %v: %v %v", holds, binds(rows), err)
		}
		asked := 0
		for i, l := range lookers {
			if l.whole("node") > 0 {
				t.Fatalf("holds %v: member %d read whole", holds, i)
			}
			if l.looked("node") > 0 {
				asked++
			}
		}
		want := 1
		if !holds {
			want = 50
		}
		if asked != want {
			t.Errorf("holds %v: %d members asked, want %d", holds, asked, want)
		}
	}
}

// A relation one of whose members can't look up is read whole from every member and kept, rather than
// read whole on every probe. control: when both members look up, it's looked up.
func TestARelationWithAMemberThatCantLookUpIsReadWhole(t *testing.T) {
	q := mustParse(t, `edge("a", ?y) => ?y`)
	for _, bothLook := range []bool{false, true} {
		members := splitInto(graph(), 2, func(i int) bool { return i == 0 || bothLook })
		lk := members[0].Source.(*looking)
		f := federate(t, members...)
		b := MustBase(std(f), f)
		for range 2 {
			if rows, err := both(q, b); err != nil || col(rows, "y") != "b" {
				t.Fatalf("both look up %v: %v %v", bothLook, binds(rows), err)
			}
		}
		looked, whole := lk.looked("edge"), lk.whole("edge")
		if bothLook && (looked == 0 || whole != 0) {
			t.Errorf("control: both look up, edge looked up %d times and read whole %d", looked, whole)
		}
		if !bothLook && (looked != 0 || whole != 1) {
			t.Errorf("edge looked up %d times and read whole %d, want 0 and 1", looked, whole)
		}
	}
}

// A relation the federation never looks up, held whole and indexed from an earlier query, doesn't make
// Explain's Eval warm, so Report.Cold doesn't rerun it and read the relation again. control: the
// relation is big enough to index (indexMinFacts), which is what reads as warm.
func TestAHeldRelationAFederationNeverLooksUpIsNotWarm(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	for i := range 2 * indexMinFacts {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i%3)), ns.S(fmt.Sprintf("n%d", i))}})
	}
	members := splitInto(src, 2, func(i int) bool { return i == 0 })
	counted := &counting{Source: members[1].Source, wholes: map[string]int{}}
	members[1].Source = counted
	f := federate(t, members...)
	b := MustBase(std(f), f)
	// A rule reads edge first: a goal alone would stream it and keep nothing (#264).
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `r(?y) :- edge(?x, ?y); r(?y) :- edge(?y, ?x); r(?y) => ?y`), b); err != nil {
		t.Fatal(err)
	}
	q := mustParse(t, `edge("n1", ?y) => ?y`)
	var r Report
	for range 2 {
		r = Report{}
		if _, err := (SemiNaive{}).Eval(bg, q, b, Explain(&r)); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.Sources) != 1 || !r.Sources[0].Cached || len(r.Sources[0].Indexes) == 0 {
		t.Fatalf("control: the second Eval didn't probe a held index: %+v", r.Sources)
	}
	if n := counted.whole("edge"); n != 1 {
		t.Errorf("edge read whole %d times, want 1: Report.Cold reran the Eval as warm", n)
	}
}

// A named member's citations carry its name, and an unnamed one's are as its Source wrote them.
func TestAFederationNamesTheMemberAFactCameFrom(t *testing.T) {
	members := splitInto(graph(), 2, func(i int) bool { return i == 1 })
	members[0].Name = "left"
	f := federate(t, members...)
	rows, err := both(mustParse(t, `edge(?x, ?y) => ?x, ?y`), MustBase(std(f), f), Witnesses())
	if err != nil {
		t.Fatal(err)
	}
	var cites []string
	for _, r := range rows {
		cites = append(cites, r.Cites...)
	}
	if got := strings.Join(cites, " "); got != "left:edge:ab edge:bc left:edge:cd" {
		t.Errorf("cites %q", got)
	}
}

// A member's version moving drops what the Base read, so the next query sees its new facts.
// control: without the version moving, the Base answers from what it kept.
func TestAFederationsVersionMovesWithAMembers(t *testing.T) {
	for _, bump := range []bool{true, false} {
		vs := &versioned{MemSource: ns.NewMemSource().Declare("node", "name"), v: "1"}
		vs.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("a")}})
		other := ns.NewMemSource().Declare("node", "name")
		f := federate(t, ns.Member{Source: other}, ns.Member{Source: vs})
		b := MustBase(std(f), f)
		q := mustParse(t, `node(?n) => count(?n)`)
		if rows, _ := both(q, b); col(rows, "count(n)") != "1" {
			t.Fatalf("first: %v", binds(rows))
		}
		vs.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("b")}})
		want := "1"
		if bump {
			vs.v, want = "2", "2"
		}
		if rows, _ := both(q, b); col(rows, "count(n)") != want {
			t.Errorf("bump %v: count %v, want %s", bump, binds(rows), want)
		}
	}
}

// A member's failed lookup fails the query with the member named, and isn't kept: the next query asks
// again.
func TestAFailedMemberLookupFailsTheQueryAndIsNotKept(t *testing.T) {
	members := splitInto(graph(), 2, func(int) bool { return true })
	members[1].Name = "flaky"
	lk := members[1].Source.(*looking)
	lk.fail = errors.New("store unreachable")
	f := federate(t, members...)
	b := MustBase(std(f), f)
	q := mustParse(t, `edge("a", ?y) => ?y`)
	_, err := (SemiNaive{}).Eval(bg, q, b)
	if err == nil || err.Error() != `query: looking up edge: member "flaky": store unreachable` {
		t.Fatalf("err %v", err)
	}
	lk.fail = nil
	if rows, err := both(q, b); err != nil || col(rows, "y") != "b" {
		t.Errorf("after recovery: %v %v", binds(rows), err)
	}
}
