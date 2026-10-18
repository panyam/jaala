package datalog

import (
	"github.com/panyam/jaala/ns"
	"strings"
	"testing"
)

// tree is a source whose relations sit at the root, one module deep and two deep.
func tree() *ns.MemSource {
	src := ns.NewMemSource().
		Declare("edge", "from", "to").
		Declare("net.pin_count", "net", "n").
		Declare("bus.pin_cont", "bus", "n").
		Declare("acme.power.rail_budget", "rail", "watts")
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}})
	src.Add("net.pin_count", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.N(4)}})
	src.Add("acme.power.rail_budget", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.N(10)}})
	return src
}

func TestPathsResolveAtEveryDepth(t *testing.T) {
	rows := eval(t, tree(), `edge(?a, ?b), net.pin_count(?n, ?c), acme.power.rail_budget(?n, ?w) => ?a, ?n, ?w`)
	if len(rows) != 1 || rows[0].Bind["a"].S != "a" || rows[0].Bind["w"].S != "10" {
		t.Errorf("rows = %v, want a root, a one-deep and a two-deep relation joined", rows)
	}
}

func TestASegmentCannotBeBothModuleAndMember(t *testing.T) {
	_, err := ns.NewVocabulary(ns.NewMemSource().Declare("pin", "ref", "pin").Declare("pin.net", "pin", "net"))
	if err == nil || !strings.Contains(err.Error(), `needs "pin" to be a module`) {
		t.Errorf("member then module: err = %v, want pin refused as a module", err)
	}
	r := ns.MustVocabulary(tree())
	err = r.AddPredicate("acme.power", ns.Filter(1, func([]ns.Value) (bool, error) { return true, nil }))
	if err == nil || !strings.Contains(err.Error(), "already a module holding rail_budget") {
		t.Errorf("module then member: err = %v, want acme.power refused as a member", err)
	}
}

func TestOnePathHasOneDefiner(t *testing.T) {
	r := ns.MustVocabulary(tree())
	err := r.AddPredicate("net.pin_count", ns.Filter(2, func([]ns.Value) (bool, error) { return true, nil }))
	if err == nil || !strings.Contains(err.Error(), `"net.pin_count" is defined twice, as a base relation and as a predicate`) {
		t.Errorf("err = %v, want both definitions named", err)
	}
}

func TestAPathMustBeSpellable(t *testing.T) {
	r := ns.MustVocabulary(nil)
	for _, p := range []string{"", "a..b", ".a", "a b"} {
		if err := r.AddPredicate(p, ns.Filter(1, func([]ns.Value) (bool, error) { return true, nil })); err == nil {
			t.Errorf("AddPredicate(%q) was accepted", p)
		}
	}
}

func TestStringFiltersLiveUnderStrAndAbsentAtTheRoot(t *testing.T) {
	src := ns.NewMemSource().Declare("row", "name", "min")
	src.Add("row", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.Absent()}}).Add("row", ns.Tuple{Vals: []ns.Value{ns.S("GND"), ns.N(0)}})
	for q, want := range map[string]string{
		`row(?n, _), str.contains(?n, "BU") => ?n`: "VBUS",
		`row(?n, _), str.prefix(?n, "G") => ?n`:    "GND",
		`row(?n, _), str.suffix(?n, "D") => ?n`:    "GND",
		`row(?n, _), str.glob(?n, "V*") => ?n`:     "VBUS",
		`row(?n, _), str.match(?n, "^G") => ?n`:    "GND",
		`row(?n, ?m), absent(?m) => ?n`:            "VBUS",
	} {
		if got := col(eval(t, src, q), "n"); got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
	err := evalErr(src, `row(?n, _), contains(?n, "BU")`)
	if err == nil || !strings.Contains(err.Error(), `unknown relation "contains"; did you mean "str.contains"?`) {
		t.Errorf("bare contains: err = %v, want it unknown with str.contains suggested", err)
	}
}

// A typo in a member is repaired inside its module, even when another module holds a closer spelling.
func TestUnknownMemberSuggestsFromItsOwnModule(t *testing.T) {
	err := evalErr(tree(), `net.pin_cont(?n, ?c)`)
	if err == nil || !strings.Contains(err.Error(), `unknown relation "net.pin_cont"; did you mean "net.pin_count"?`) {
		t.Errorf("err = %v, want net.pin_count and not bus.pin_cont", err)
	}
}

func TestAMissingModuleIsNamedAsOne(t *testing.T) {
	err := evalErr(tree(), `nett.pin_count(?n, ?c)`)
	if err == nil || !strings.Contains(err.Error(), `unknown module "nett" in "nett.pin_count"; did you mean "net.pin_count"?`) {
		t.Errorf("err = %v, want the module named and the repaired path suggested", err)
	}
	err = evalErr(tree(), `edge(?a, ?c), acme.powr.x(?a)`)
	if err == nil || !strings.Contains(err.Error(), `unknown module "acme.powr"`) || !strings.Contains(err.Error(), `did you mean "acme.power"?`) {
		t.Errorf("err = %v, want the nested module named and its nearest suggested", err)
	}
}

func TestAModuleIsNotARelation(t *testing.T) {
	err := evalErr(tree(), `edge(?a, ?b), str(?a)`)
	if err == nil || !strings.Contains(err.Error(), `"str" is a module, not a relation; it holds capitalize, concat, contains, distance, glob, lower, match, prefix, replace, split, suffix, upper`) {
		t.Errorf("err = %v, want str named as a module with its members", err)
	}
}

func TestDidYouMeanUsesTheTree(t *testing.T) {
	if got := ns.DidYouMean(std(tree()), "net.pin_cont"); got != `; did you mean "net.pin_count"?` {
		t.Errorf("DidYouMean = %q", got)
	}
}

// A hint is only a hint: a name typed without its module still does not resolve (#10).
func TestAHintedNameStillFails(t *testing.T) {
	src := ns.NewMemSource().Declare("component.pin", "ref", "pin").Declare("pin.net", "ref", "pin", "net")
	src.Add("component.pin", ns.Tuple{Vals: []ns.Value{ns.S("R1"), ns.S("1")}})
	rows, err := Naive{}.Eval(bg, mustParse(t, `pin(?r, ?p) => ?r`), baseFor(std(src)))
	if err == nil || rows != nil || !strings.Contains(err.Error(), `"pin" is a module, not a relation; it holds net; did you mean "component.pin"?`) {
		t.Errorf("rows = %v, err = %v; want no rows and the module error with its hint", rows, err)
	}
}
