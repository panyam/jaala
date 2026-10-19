package ns

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// hardValues are the values a lossy encoding would change: a kind, a spelling, a unit, a sign or a
// number JSON can't hold.
func hardValues() []Value {
	num := func(f float64, s, unit string) Value { return Value{S: s, Num: &f, BaseUnit: unit} }
	return []Value{
		S("C1"), S(""), S("3"), S("null"), S(`a "quoted" <tag> & \ slash`), Absent(),
		N(3), N(math.Copysign(0, -1)), N(1e21), N(0.1),
		num(1.5, "1.50", ""), num(3.3, "3.3V", "V"), NU(4.7, "kohm"),
		N(math.NaN()), N(math.Inf(1)), N(math.Inf(-1)), num(math.NaN(), "nan?", "V"),
		{S: "5", BaseUnit: "V"},
	}
}

// sameValue compares two values field by field, NaN equal to NaN and -0 apart from 0.
func sameValue(a, b Value) bool {
	if a.S != b.S || a.Absent != b.Absent || a.BaseUnit != b.BaseUnit || (a.Num == nil) != (b.Num == nil) {
		return false
	}
	if a.Num == nil {
		return true
	}
	x, y := *a.Num, *b.Num
	return math.Float64bits(x) == math.Float64bits(y) || math.IsNaN(x) && math.IsNaN(y)
}

func roundTrip(t *testing.T, c Changes) (Changes, []byte) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Changes
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("encoded twice differently:\n%s\n%s", data, again)
	}
	return back, data
}

func TestEveryValueComesBackAsItWent(t *testing.T) {
	var c Changes
	for i, v := range hardValues() {
		c.Add("attr", Tuple{Vals: []Value{S(fmt.Sprint("p", i)), v}, ID: fmt.Sprint("id", i), Cites: []string{"edit"}})
	}
	c.Remove("attr", Tuple{Vals: []Value{S("gone"), N(2)}})
	back, data := roundTrip(t, c)
	got, want := back.Added("attr"), c.Added("attr")
	if len(got) != len(want) {
		t.Fatalf("%d facts back, want %d: %s", len(got), len(want), data)
	}
	for i := range want {
		if !sameValue(got[i].Vals[1], want[i].Vals[1]) || got[i].ID != want[i].ID || !slices.Equal(got[i].Cites, want[i].Cites) {
			t.Errorf("fact %d: %#v came back as %#v", i, want[i], got[i])
		}
	}
	if r := back.Removed("attr"); len(r) != 1 || r[0].Vals[0].S != "gone" {
		t.Errorf("removed: %v", r)
	}
	// control: the plain cases are short, and only the lossy ones take the object form.
	for _, frag := range []string{`["p0","C1"]`, `["p6",3]`, `{"num":1.5,"text":"1.50"}`, `{"num":"NaN"}`, `{"text":"5","unit":"V"}`} {
		if !strings.Contains(string(data), frag) {
			t.Errorf("%s isn't in %s", frag, data)
		}
	}
}

func TestARandomChangeSetReadsBackTheSame(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	vals := hardValues()
	hard := 0
	for trial := range 500 {
		var c Changes
		for range rnd.Intn(8) {
			rel := []string{"edge", "attr", "pin"}[rnd.Intn(3)]
			tu := Tuple{Vals: []Value{vals[rnd.Intn(len(vals))], vals[rnd.Intn(len(vals))]}}
			if rnd.Intn(2) == 0 {
				tu.ID = fmt.Sprint("e", rnd.Intn(4))
			}
			if rnd.Intn(2) == 0 {
				tu.Cites = []string{fmt.Sprint("c", rnd.Intn(3))}
			}
			if rnd.Intn(2) == 0 {
				c.Add(rel, tu)
			} else {
				c.Remove(rel, tu)
			}
		}
		back, data := roundTrip(t, c)
		if strings.Contains(string(data), `"num"`) {
			hard++
		}
		if !slices.Equal(back.Relations(), c.Relations()) {
			t.Fatalf("trial %d: relations %v, want %v", trial, back.Relations(), c.Relations())
		}
		base := NewMemSource().Declare("edge", "a", "b").Declare("attr", "a", "b").Declare("pin", "a", "b")
		base.Add("edge", Tuple{Vals: []Value{S("C1"), N(3)}})
		if a, b := Overlay(base, c).Version(), Overlay(base, back).Version(); a != b {
			t.Fatalf("trial %d: overlay versions %s and %s for %s", trial, a, b, data)
		}
		for _, rel := range c.Relations() {
			if g, w := factTexts(back.Apply(rel, base.Tuples(rel))), factTexts(c.Apply(rel, base.Tuples(rel))); !slices.Equal(g, w) {
				t.Fatalf("trial %d: %s applied %v, want %v", trial, rel, g, w)
			}
		}
	}
	if hard < 100 {
		t.Fatalf("control: only %d change sets held a value in the object form", hard)
	}
}

func TestEveryListValueReadsAsTheSameWireValue(t *testing.T) {
	for _, v := range hardValues() {
		list := ListText([]Value{v})
		inList, err := ParseList(list)
		if err != nil {
			t.Fatal(err)
		}
		var c Changes
		data := fmt.Sprintf(`{"format":1,"ops":[{"rel":"r","op":"add","vals":%s}]}`, list)
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if got := c.Added("r")[0].Vals[0]; !sameValue(got, inList[0]) {
			t.Errorf("%s reads as %#v in a change set, %#v in a list", list, got, inList[0])
		}
	}
	// A value a list holds exactly is itself in a change set, kind and all.
	for _, v := range []Value{S("C1"), S(""), S("3"), N(3), N(math.Copysign(0, -1)), NU(4.7, "kohm"), Absent()} {
		var c Changes
		data := fmt.Sprintf(`{"format":1,"ops":[{"rel":"r","op":"add","vals":%s}]}`, ListText([]Value{v}))
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatal(err)
		}
		if got := c.Added("r")[0].Vals[0]; !sameValue(got, v) {
			t.Errorf("%s reads as %#v, want %#v", data, got, v)
		}
	}
}

func TestASessionCarriesItsBase(t *testing.T) {
	var c Changes
	c.Add("pin", Tuple{Vals: []Value{S("C1"), S("3V3")}})
	data, err := json.Marshal(Session{Base: "rev-41", Changes: c})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"base":"rev-41","changes":{"format":1,"ops":[{"rel":"pin","op":"add","vals":["C1","3V3"]}]}}`; string(data) != want {
		t.Errorf("got %s\nwant %s", data, want)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil || s.Base != "rev-41" || len(s.Changes.Added("pin")) != 1 {
		t.Errorf("read back %+v, %v", s, err)
	}
}

func TestAChangeSetFromAnotherWriterIsRefused(t *testing.T) {
	ops := func(op string) string { return `{"format":1,"ops":[` + op + `]}` }
	for _, tc := range []struct{ data, err string }{
		{`[1]`, "query: change set: not a change set's JSON"},
		{`{"format":2,"ops":[]}`, `query: change set: format "2" isn't one this version reads (it reads 1)`},
		{`{"ops":[]}`, `query: change set: format "" isn't one this version reads`},
		{ops(`{"rel":"pin","op":"add","vals":[],"when":3}`), "query: change set: not a change set's JSON"},
		{ops(`{"op":"add","vals":["a"]}`), "query: change set: op 0 names no relation"},
		{ops(`{"rel":"pin","op":"set","vals":["a"]}`), `query: change set: op 0 is "set", not "add" or "remove"`},
		{ops(`{"rel":"pin","op":"add","vals":["a",true]}`), "query: change set: op 0's value 1 isn't one a change set writes: true"},
		{ops(`{"rel":"pin","op":"add","vals":[{"num":"inf"}]}`), "query: change set: op 0's value 0 isn't one a change set writes"},
		{ops(`{"rel":"pin","op":"add","vals":[{"num":1,"why":2}]}`), "query: change set: op 0's value 0 isn't one a change set writes"},
		{ops(`{"rel":"pin","op":"add","vals":[{"unit":"V"}]}`), "query: change set: op 0's value 0 isn't one a change set writes"},
		{ops(`{"rel":"pin","op":"add","vals":[{"num":1,"unit":""}]}`), "query: change set: op 0's value 0 isn't one a change set writes"},
		{ops(`{"rel":"pin","op":"add","vals":[[1]]}`), "query: change set: op 0's value 0 isn't one a change set writes"},
	} {
		var c Changes
		wantErr(t, json.Unmarshal([]byte(tc.data), &c), tc.err)
	}
	// control: the same shape with valid parts reads.
	var c Changes
	if err := json.Unmarshal([]byte(ops(`{"rel":"pin","op":"add","vals":["a",{"num":1,"unit":"V"},{"text":"x","unit":"V"},null]}`)), &c); err != nil {
		t.Fatal(err)
	}
}
