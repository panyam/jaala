package datalog

import (
	"math"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// spelled is a number with text of its own, as a host writes one: 3.3V, or 1.50.
func spelled(text string, f float64, unit string) ns.Value {
	return ns.Value{S: text, Num: &f, BaseUnit: unit}
}

// volts is agni's shape: a rail's voltage stored as 3.3V carrying 3.3 in volts, the same number
// spelled 3300mV in another relation, a part's limit stored as its range text carrying its maximum and
// no unit, and a plain 3.3 in volts.
func volts() *ns.MemSource {
	src := ns.NewMemSource().Declare("rail", "net", "v").Declare("probe", "net", "v").Declare("node", "name").
		Declare("limit", "part", "max").Declare("level", "net", "v")
	src.Add("limit", ns.Tuple{Vals: []ns.Value{ns.S("U1"), spelled("1.8V..3.3V", 3.3, "")}})
	src.Add("level", ns.Tuple{Vals: []ns.Value{ns.S("VCC"), ns.NU(3.3, "V")}})
	src.Add("rail", ns.Tuple{Vals: []ns.Value{ns.S("VCC"), spelled("3.3V", 3.3, "V")}})
	src.Add("rail", ns.Tuple{Vals: []ns.Value{ns.S("GND"), spelled("0V", 0, "V")}})
	src.Add("probe", ns.Tuple{Vals: []ns.Value{ns.S("VCC"), spelled("3300mV", 3.3, "V")}})
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("VCC")}})
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("GND")}})
	return src
}

// #148's repro: the goal spells 1 as 01, and the planned SemiNaive binds ?x7 from its demand before
// reading weight's 1. Every evaluator now answers 1.
func TestANumberSpelledTwoWaysAnswersOneWay(t *testing.T) {
	q := mustParse(t, `r0(0, ?x7, ?x7) :- edge(?01, ?00), weight(?0, ?x7); r0(0, 01, ?0)`)
	rows, diff, err := agree(q, baseFor(fuzzVocabulary(t)))
	if err != nil || diff != "" {
		t.Fatalf("evaluators disagree: %v\n%s", err, diff)
	}
	if got := col(rows, "0"); got != "1" {
		t.Errorf("?0 = %q, want 1", got)
	}
}

// A query's 3.3, pushed by demand through a rule head into another column, meets the data's spelling of
// the number, and every evaluator answers the data's: text saying more than the number (3.3V, or a
// range with no unit), else the value carrying a unit.
func TestTheDataSpellingOfANumberWinsOverTheQuerys(t *testing.T) {
	for _, c := range []struct{ rel, want, unit string }{{"rail", "3.3V", "V"}, {"limit", "1.8V..3.3V", ""}, {"level", "3.3", "V"}} {
		text := `at(?n, ?v, ?v) :- ` + c.rel + `(?n, ?v); at(?n, ?v, ?v) :- ` + c.rel + `(?n, ?v), ?n != "none"; at(?n, 3.3, ?w) => ?n, ?w`
		rows := eval(t, volts(), text)
		if len(rows) != 1 || rows[0].Bind["w"].S != c.want || rows[0].Bind["w"].BaseUnit != c.unit {
			t.Errorf("%s: ?w = %v, want %q in %q", c.rel, rows, c.want, c.unit)
		}
	}
}

// The query's 3.3 can also reach the data one relation further down: demand carries it through a
// variable into e (demand:e/fb(?v) :- at:from1(?v)), and e meets rail's 3.3V there. control: the
// planned evaluator ran that rule, so the value took that path.
func TestTheDataSpellingWinsOverAQuerysPassedOnByDemand(t *testing.T) {
	text := `e(?n, ?v) :- rail(?n, ?v); e(?n, ?v) :- rail(?n, ?v), ?n != "none";
		at(?n, ?v, ?v) :- e(?n, ?v); at(?n, ?v, ?v) :- e(?n, ?v), ?n != "none"; at(?n, 3.3, ?w) => ?n, ?w`
	rows := eval(t, volts(), text)
	if len(rows) != 1 || rows[0].Bind["w"].S != "3.3V" {
		t.Errorf("?w = %v, want 3.3V as rail spells it", rows)
	}
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, text), baseFor(std(volts())), Explain(&r)); err != nil || !strings.Contains(r.String(), "demand:e/fb(?v) :- at:from1(?v)") {
		t.Errorf("control: the planned evaluator didn't pass demand into e (%v):\n%s", err, r.String())
	}
}

// Two spellings of one number in the data answer the same text whichever the body reads first: the
// shorter, 3.3V. control: written in both orders.
func TestTwoDataSpellingsAnswerOneWayInEitherOrder(t *testing.T) {
	for _, body := range []string{`rail(?n, ?v), probe(?n, ?v)`, `probe(?n, ?v), rail(?n, ?v)`} {
		rows := eval(t, volts(), body+` => ?v`)
		if got := col(rows, "v"); got != "3.3V" {
			t.Errorf("%s: ?v = %q, want 3.3V", body, got)
		}
	}
}

// An answer writes a plain number canonically: 1.50 as 1.5, and 1.0 and 1 as one row, in its
// bindings, a list, and its witnesses. Text saying more, such as 3.3V, is left alone, and a value the
// host bound keeps the text it was bound as.
func TestAnAnswerWritesAPlainNumberCanonically(t *testing.T) {
	src := ns.NewMemSource().Declare("m", "k", "v")
	src.Add("m", ns.Tuple{Vals: []ns.Value{ns.S("a"), spelled("1.50", 1.5, "")}})
	src.Add("m", ns.Tuple{Vals: []ns.Value{ns.S("b"), spelled("1.0", 1, "")}})
	src.Add("m", ns.Tuple{Vals: []ns.Value{ns.S("c"), ns.N(1)}})
	src.Add("m", ns.Tuple{Vals: []ns.Value{ns.S("d"), spelled("3.3V", 3.3, "V")}})
	src.Add("m", ns.Tuple{Vals: []ns.Value{ns.S("e"), spelled("-0", math.Copysign(0, -1), "")}})
	if got := col(eval(t, src, `m(_, ?v) => ?v`), "v"); got != "0,1,1.5,3.3V" {
		t.Errorf("values: %q, want 0,1,1.5,3.3V (1.0 and 1 one row)", got)
	}
	if got := col(eval(t, src, `m(?k, ?v), ?k = "a" => list(?v)`), "list(v)"); got != "[1.5]" {
		t.Errorf("list: %q, want [1.5]", got)
	}
	v := std(src)
	rows, err := both(mustParse(t, `m("a", ?v) => ?v`), baseFor(v), Witnesses())
	if err != nil || len(rows) != 1 || len(rows[0].Witness) != 1 || rows[0].Witness[0].Values[1].S != "1.5" {
		t.Errorf("witness: %v %v; want its value written 1.5", rows, err)
	}
	if src.Tuples("m")[0].Vals[1].S != "1.50" {
		t.Errorf("the source's tuple was rewritten: %q", src.Tuples("m")[0].Vals[1].S)
	}
	rows, err = both(mustParse(t, `m(?k, ?v) => ?k, ?v`), baseFor(v), Bind(map[Var][]ns.Value{"v": {spelled("1.50", 1.5, "")}}))
	if err != nil || col(rows, "v") != "1.50" {
		t.Errorf("bound: %v %v; want the column as bound, 1.50", col(rows, "v"), err)
	}
}
