package ns

import (
	"context"
	"reflect"
	"testing"
)

// Two stores serving one value in different units: A's TP1 reads 0.004 kV, B's VCC rail is 4 V. The
// engine reads both as 4 V (#205), so a partitioned Federation carrying TP1's value from A to B has to
// carry it folded, as the engine sends values, or B, comparing what it stores, never meets it.
func unitStores(t *testing.T) (*Units, []Member) {
	t.Helper()
	v := electrical(t)
	a := &joinMem{MemSource: NewMemSource().Declare("level", "probe", "v")}
	a.Add("level", Tuple{Vals: []Value{S("TP1"), NU(0.004, "kV")}, Cites: []string{"TP1"}})
	b := &joinMem{MemSource: NewMemSource().Declare("rail", "v", "name")}
	b.Add("rail", Tuple{Vals: []Value{NU(4, "V"), S("VCC")}, Cites: []string{"VCC"}})
	return v.Units(), []Member{{Name: "A", Source: a}, {Name: "B", Source: b}}
}

func TestAPartitionedFederationCarriesValuesFolded(t *testing.T) {
	units, members := unitStores(t)
	f, err := NewPartitionedFederation(members...)
	if err != nil {
		t.Fatal(err)
	}
	c := Conjunction{Atoms: []PushAtom{
		{Relation: "level", Args: []PushArg{{Var: 1}, {Var: 2}}},
		{Relation: "rail", Args: []PushArg{{Var: 2}, {Var: 3}}},
	}}
	if got := pushedRows(t, f, c); len(got) != 0 {
		t.Fatalf("control: with no unit table, 0.004 kV met 4 V: %q", got)
	}
	c.Units = units
	if got, want := pushedRows(t, f, c), []string{"A:TP1 B:VCC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pushed %q, want %q", got, want)
	}
}

// A walk crossing members carries the value it reached folded too.
func TestAFederationWalksAcrossMembersFolded(t *testing.T) {
	v := electrical(t)
	a := &walkMem{MemSource: NewMemSource().Declare("step", "from", "to")}
	a.Add("step", Tuple{Vals: []Value{S("x"), NU(0.004, "kV")}})
	b := &walkMem{MemSource: NewMemSource().Declare("step", "from", "to")}
	b.Add("step", Tuple{Vals: []Value{NU(4, "V"), S("y")}})
	f, err := NewFederation(Member{Name: "A", Source: a}, Member{Name: "B", Source: b})
	if err != nil {
		t.Fatal(err)
	}
	walk := func(units *Units) []string {
		var out []string
		for tu, err := range f.Walk(context.Background(), Walk{Relation: "step", From: 0, To: 1, Start: []Value{S("x")}, Units: units}) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tu.Vals[0].S+">"+tu.Vals[1].S)
		}
		return out
	}
	if got := walk(nil); len(got) != 1 {
		t.Fatalf("control: with no unit table, the walk went past 0.004 kV: %q", got)
	}
	if got, want := walk(v.Units()), []string{"x>0.004", "4>y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("walked %q, want %q", got, want)
	}
}
