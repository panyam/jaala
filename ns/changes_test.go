package ns

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func factTexts(ts []Tuple) []string {
	var out []string
	for _, t := range ts {
		var vals []string
		for _, v := range t.Vals {
			switch {
			case v.Absent:
				vals = append(vals, "<absent>")
			case v.Num != nil:
				vals = append(vals, "n:"+ftoa(*v.Num))
			default:
				vals = append(vals, fmt.Sprintf("%q", v.S))
			}
		}
		out = append(out, fmt.Sprintf("%s id=%s %v", strings.Join(vals, " "), t.ID, t.Cites))
	}
	return out
}

func sorted(ss []string) []string {
	ss = slices.Clone(ss)
	slices.Sort(ss)
	return ss
}

func TestTheLastOperationOnAFactWins(t *testing.T) {
	var c Changes
	c.Add("edge", Tuple{Vals: []Value{S("a"), S("b")}, ID: "e1"}).
		Remove("edge", Tuple{Vals: []Value{S("a"), S("b")}}).
		Remove("edge", Tuple{Vals: []Value{S("b"), S("c")}}).
		Add("edge", Tuple{Vals: []Value{S("b"), S("c")}, ID: "e2", Cites: []string{"edit:2"}})
	if got := factTexts(c.Removed("edge")); !slices.Equal(got, []string{`"a" "b" id= []`}) {
		t.Errorf("removed %v", got)
	}
	if got := factTexts(c.Added("edge")); !slices.Equal(got, []string{`"b" "c" id=e2 [edit:2]`}) {
		t.Errorf("added %v", got)
	}
	if got := c.Relations(); !slices.Equal(got, []string{"edge"}) {
		t.Errorf("relations %v", got)
	}
	if c.Empty() || !(Changes{}).Empty() {
		t.Error("Empty")
	}
}

func TestApplyKeysAFactByItsValues(t *testing.T) {
	negZero := math.Copysign(0, -1)
	base := []Tuple{
		{Vals: []Value{S("a"), N(1)}, ID: "first"},
		{Vals: []Value{S("a"), S("1")}},
		{Vals: []Value{S("a"), N(1)}, ID: "copy"},
		{Vals: []Value{S("b"), Absent()}},
		{Vals: []Value{S("b"), S("")}},
		{Vals: []Value{S("c"), N(negZero)}},
		{Vals: []Value{S("d"), NU(5, "V")}},
	}
	var c Changes
	c.Remove("w", Tuple{Vals: []Value{S("a"), N(1)}}).
		Remove("w", Tuple{Vals: []Value{S("b"), Absent()}}).
		Remove("w", Tuple{Vals: []Value{S("c"), N(0)}}).
		Add("w", Tuple{Vals: []Value{S("d"), N(5)}, ID: "new"})
	got := factTexts(c.Apply("w", base))
	want := []string{`"a" "1" id= []`, `"b" "" id= []`, `"d" n:5 id=new []`}
	if !slices.Equal(got, want) {
		t.Errorf("applied:\n got %v\nwant %v", got, want)
	}
	// control: a relation c doesn't name is base as it is.
	if got := c.Apply("edge", base); len(got) != len(base) {
		t.Errorf("edge: %v", factTexts(got))
	}
}

// TestThenIsOneSetAfterTheOther composes random change sets over a few facts and requires the
// composition applied once to hold what applying each in turn does.
func TestThenIsOneSetAfterTheOther(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	facts := []Tuple{
		{Vals: []Value{S("a"), S("b")}},
		{Vals: []Value{S("b"), S("c")}},
		{Vals: []Value{S("c"), N(1)}},
		{Vals: []Value{S("c"), S("1")}},
	}
	random := func() Changes {
		var c Changes
		for range rnd.Intn(5) {
			f := facts[rnd.Intn(len(facts))]
			f.ID = fmt.Sprintf("id%d", rnd.Intn(3))
			if rnd.Intn(2) == 0 {
				c.Add("edge", f)
			} else {
				c.Remove("edge", f)
			}
		}
		return c
	}
	orderMatters := 0
	for trial := range 500 {
		var base []Tuple
		for _, f := range facts {
			if rnd.Intn(2) == 0 {
				base = append(base, f)
			}
		}
		first, second := random(), random()
		before := factTexts(first.Added("edge"))
		got := sorted(factTexts(first.Then(second).Apply("edge", base)))
		want := sorted(factTexts(second.Apply("edge", first.Apply("edge", base))))
		if !slices.Equal(got, want) {
			t.Fatalf("trial %d: composed %v, in turn %v", trial, got, want)
		}
		if !slices.Equal(factTexts(first.Added("edge")), before) {
			t.Fatalf("trial %d: Then changed its receiver", trial)
		}
		if !slices.Equal(got, sorted(factTexts(second.Then(first).Apply("edge", base)))) {
			orderMatters++
		}
	}
	if orderMatters == 0 {
		t.Fatal("control: no trial's answer depended on the order of the two sets")
	}
}

func TestCheckRefusesWhatTheVocabularyCannotHold(t *testing.T) {
	v, _ := newTestVocabulary(t)
	if err := v.AddModule("net", "stub", "has_tp/1", ""); err != nil {
		t.Fatal(err)
	}
	ok := func() *Changes {
		var c Changes
		return c.Add("edge", Tuple{Vals: []Value{S("a"), S("b")}}).Remove("net.pin_count", Tuple{Vals: []Value{S("gnd"), N(2)}})
	}
	if err := ok().Check(v); err != nil {
		t.Fatalf("control: %v", err)
	}
	wantErr(t, ok().Add("edgee", Tuple{Vals: []Value{S("a"), S("b")}}).Check(v), `query: change set: unknown relation "edgee"; did you mean "edge"?`)
	wantErr(t, ok().Add("net.has_tp", Tuple{Vals: []Value{S("gnd")}}).Check(v), `query: change set: net.has_tp is a derived relation, and only a base relation's facts change`)
	wantErr(t, ok().Add("edge", Tuple{Vals: []Value{S("a")}}).Check(v), `query: change set: edge takes 2 args, and a fact to add has 1`)
	wantErr(t, ok().Remove("edge", Tuple{Vals: []Value{S("a"), S("b"), S("c")}}).Check(v), `query: change set: edge takes 2 args, and a fact to remove has 3`)
}
