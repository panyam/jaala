package datalog

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// parts is thirteen parts on four nets, counted 9 (GND), 2 (VBUS), 1 (SDA) and 1 (SCL), and four pins
// whose numbers sort differently by value (1 2 3 10) and by text (1 10 2 3). Facts go in out of
// order, so no answer is sorted by insertion.
func parts() *ns.MemSource {
	src := ns.NewMemSource().Declare("part", "ref", "net").Declare("pin", "ref", "n")
	for i, n := range []string{"SDA", "GND", "VBUS", "GND", "SCL", "VBUS", "GND", "GND", "GND", "GND", "GND", "GND", "GND"} {
		src.Add("part", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("R%d", i)), ns.S(n)}})
	}
	for i, n := range []float64{2, 10, 1, 3} {
		src.Add("pin", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("U%d", i)), ns.N(n)}})
	}
	return src
}

// cols is each row's values of the given columns, joined with spaces.
func cols(rows []Row, names ...string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		vals := make([]string, len(names))
		for j, n := range names {
			vals[j] = r.Bind[Var(n)].S
		}
		out[i] = strings.Join(vals, " ")
	}
	return out
}

func TestTheDefaultOrderSortsNumbersByValue(t *testing.T) {
	got := cols(eval(t, parts(), `pin(?r, ?n) => ?n, ?r`), "n")
	if want := []string{"1", "2", "3", "10"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
	if text := []string{"1", "2", "3", "10"}; sort.StringsAreSorted(text) {
		t.Errorf("control: %v is already in text order, so the fixture can't tell value order from text order", text)
	}
}

func TestAListAggregateNamesItsMembersInTheDefaultOrder(t *testing.T) {
	if got := col(eval(t, parts(), `pin(_, ?n) => list(?n)`), "list(n)"); got != "[1,2,3,10]" {
		t.Errorf("list %q, want [1,2,3,10]", got)
	}
}

// One column holding every kind of value: absent first, then numbers by value, then text.
func TestTheDefaultOrderRanksAbsentThenNumbersThenText(t *testing.T) {
	src := ns.NewMemSource().Declare("v", "x")
	for _, val := range []ns.Value{ns.S("b"), ns.N(10), ns.S("1a"), ns.Absent(), ns.N(2), ns.S("10x")} {
		src.Add("v", ns.Tuple{Vals: []ns.Value{val}})
	}
	rows := eval(t, src, `v(?x) => ?x`)
	if !rows[0].Bind["x"].Absent {
		t.Errorf("first row %+v, want the absent value", rows[0].Bind["x"])
	}
	if got, want := cols(rows[1:], "x"), []string{"2", "10", "10x", "1a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order after absent %v, want %v", got, want)
	}
	desc := Query{Goal: mustParse(t, `v(?x)`).Goal, Select: []Term{V("x")}, OrderBy: []Order{{Term: V("x"), Desc: true}}}
	got, err := both(desc, baseFor(std(src)))
	if err != nil || !got[len(got)-1].Bind["x"].Absent {
		t.Errorf("order by ?x desc: %v, %v; want the absent value last", cols(got, "x"), err)
	}
}

// An absent value and the empty string are two values to valueEq, so they are two rows and two groups;
// a number and its text are one value to a join, and stay one row (#62).
func TestAnAbsentValueIsNotTheEmptyString(t *testing.T) {
	src := ns.NewMemSource().Declare("v", "x").Declare("u", "x")
	src.Add("v", ns.Tuple{Vals: []ns.Value{ns.S("")}})
	src.Add("v", ns.Tuple{Vals: []ns.Value{ns.Absent()}})
	src.Add("u", ns.Tuple{Vals: []ns.Value{ns.S("")}})
	src.Add("u", ns.Tuple{Vals: []ns.Value{ns.Absent()}})
	for _, q := range []string{`v(?x) => ?x`, `w(?x) :- v(?x); w(?x) :- u(?x); w(?x) => ?x`} {
		rows := eval(t, src, q)
		if len(rows) != 2 || !rows[0].Bind["x"].Absent || rows[1].Bind["x"].Absent || rows[1].Bind["x"].S != "" {
			t.Errorf("%s: %v, want two rows, the absent value then the empty string", q, binds(rows))
		}
	}
	rows := eval(t, src, `v(?x) => ?x, count(?x)`)
	if got := cols(rows, "count(x)"); len(rows) != 2 || !rows[0].Bind["x"].Absent || !reflect.DeepEqual(got, []string{"1", "1"}) {
		t.Errorf("grouped: %v, want two groups of one, the absent value first", binds(rows))
	}
	same := ns.NewMemSource().Declare("v", "x")
	same.Add("v", ns.Tuple{Vals: []ns.Value{ns.N(1)}})
	same.Add("v", ns.Tuple{Vals: []ns.Value{ns.S("1")}})
	// N(1) and S("1") are different values (#162), so they answer as two rows, the number first.
	if rows := eval(t, same, `v(?x) => ?x`); len(rows) != 2 || rows[0].Bind["x"].Num == nil || rows[1].Bind["x"].Num != nil {
		t.Errorf("control: N(1) and S(\"1\") answer %v, want two rows, the number first", binds(rows))
	}
}

// orderValues has to be an order (antisymmetric and transitive) for sorting by it to mean anything.
// The values mix numbers, numeric-looking text and units; the control shows that comparing numbers
// by value only when both are numbers, as the issue first sketched, makes a cycle on this set.
func TestTheDefaultOrderIsATotalOrder(t *testing.T) {
	vals := []ns.Value{ns.Absent(), ns.N(2), ns.N(10), ns.N(-1), ns.NU(5, "V"), ns.NU(5, "A"), ns.S("10"), ns.S("2"), ns.S("1a"), ns.S("a"), ns.S("")}
	check := func(order func(a, b ns.Value) int) error {
		for _, a := range vals {
			for _, b := range vals {
				if order(a, b) != -order(b, a) {
					return fmt.Errorf("%+v and %+v are not antisymmetric", a, b)
				}
				for _, c := range vals {
					if order(a, b) <= 0 && order(b, c) <= 0 && order(a, c) > 0 {
						return fmt.Errorf("%q <= %q <= %q but %q > %q", a.S, b.S, c.S, a.S, c.S)
					}
				}
			}
		}
		return nil
	}
	if err := check(orderValues); err != nil {
		t.Error(err)
	}
	pairwise := func(a, b ns.Value) int {
		if a.Num != nil && b.Num != nil {
			return orderValues(a, b)
		}
		return strings.Compare(a.S, b.S)
	}
	if check(pairwise) == nil {
		t.Error("control: comparing by value only when both are numbers found no cycle, so these values can't tell an order from a non-order")
	}
}

func TestOrderByACountDescendingAndLimit(t *testing.T) {
	q := `part(?r, ?n) => ?n, count(?r) order by count(?r) desc, ?n limit 2`
	if got, want := cols(eval(t, parts(), q), "n", "count(r)"), []string{"GND 9", "VBUS 2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s: %v, want %v", q, got, want)
	}
	if first := cols(eval(t, parts(), `part(?r, ?n) => ?n, count(?r) limit 2`), "n"); reflect.DeepEqual(first, []string{"GND", "VBUS"}) {
		t.Errorf("control: the default order's first two are already %v, so the order by isn't what picked them", first)
	}
}

// Rows that tie on every order by column keep the default order: SCL and SDA both count 1.
func TestRowsThatTieKeepTheDefaultOrder(t *testing.T) {
	got := cols(eval(t, parts(), `part(?r, ?n) => ?n, count(?r) order by count(?r)`), "n")
	if want := []string{"SCL", "SDA", "VBUS", "GND"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

// Paging is the reason for offset (Declaire's interactive callers): the pages of any size, laid end to
// end, are the whole answer, and a page past the end is empty.
func TestPagesLaidEndToEndAreTheWholeAnswer(t *testing.T) {
	base := `part(?r, ?n) => ?r, ?n order by ?n desc`
	whole := cols(eval(t, parts(), base), "r", "n")
	if len(whole) != 13 {
		t.Fatalf("control: %d rows, want 13", len(whole))
	}
	for size := 1; size <= 14; size++ {
		var pages []string
		for off := 0; off <= len(whole); off += size {
			pages = append(pages, cols(eval(t, parts(), fmt.Sprintf("%s limit %d offset %d", base, size, off)), "r", "n")...)
		}
		if !reflect.DeepEqual(pages, whole) {
			t.Errorf("pages of %d: %v, want %v", size, pages, whole)
		}
	}
	if rows := eval(t, parts(), base+" offset 13"); len(rows) != 0 {
		t.Errorf("offset 13 of 13 rows: %v, want none", cols(rows, "r"))
	}
}

// A variable the host bound is dropped from the projection while evaluating, but it is still an answer
// column, so ordering by it is allowed.
func TestOrderByAColumnTheHostBound(t *testing.T) {
	q := mustParse(t, `part(?r, ?n) => ?r, ?n order by ?n, ?r desc limit 2`)
	rows, err := SemiNaive{}.Eval(bg, q, baseFor(std(parts())), Bind(map[Var][]ns.Value{"n": {ns.S("VBUS")}}))
	if got, want := cols(rows, "r", "n"), []string{"R5 VBUS", "R2 VBUS"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%v, %v; want %v", got, err, want)
	}
}

func TestOrderAndLimitRefusals(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{`pin(?r, ?n) => ?r order by ?n`, `order by ?n, which is not an answer column`},
		{`pin(?r, ?n) => ?r, count(?n) order by sum(?n)`, `order by sum(?n), which is not an answer column`},
		{`pin(?r, ?n) => ?r, count(?n) order by count(distinct ?n)`, `order by count(distinct ?n), which is not an answer column`},
		{`pin(?r, ?n) => ?r order ?r`, `order needs by`},
		{`pin(?r, ?n) => ?r order by`, `order by needs a column`},
		{`pin(?r, ?n) => ?r order by ?r,`, `order by needs a column`},
		{`pin(?r, ?n) => ?r order by "x"`, `order by: projection column`},
		{`pin(?r, ?n) => ?r limit 0`, `limit "0" is not a positive whole number`},
		{`pin(?r, ?n) => ?r limit -1`, `limit "-1" is not a positive whole number`},
		{`pin(?r, ?n) => ?r limit +3`, `limit "+3" is not a positive whole number`},
		{`pin(?r, ?n) => ?r limit x`, `limit "x" is not a positive whole number`},
		{`pin(?r, ?n) => ?r offset -1`, `offset "-1" is not a whole number`},
		{`pin(?r, ?n) => ?r limit 2 order by ?r`, `limit comes after order by, not before it`},
		{`pin(?r, ?n) => ?r, count(?n) limit 2 having count(?n) > 1`, `limit comes after having, not before it`},
		{`pin(?r, ?n) => ?r limit 1 limit 2`, `limit appears twice`},
	} {
		_, err := Parse(c.q)
		if err == nil {
			q := mustParse(t, c.q)
			err = evalErr(parts(), c.q)
			if verr := Validate(q, std(parts())); fmt.Sprint(verr) != fmt.Sprint(err) {
				t.Errorf("%s: Validate says %v, Eval says %v", c.q, verr, err)
			}
		}
		if err == nil || !strings.HasPrefix(err.Error(), "query: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one containing %q", c.q, err, c.want)
		}
	}
	q := mustParse(t, `pin(?r, ?n) => ?r`)
	q.Limit = -1
	if _, err := both(q, baseFor(std(parts()))); err == nil || !strings.Contains(err.Error(), "can't be negative") {
		t.Errorf("Limit -1 built in Go: error %v, want a refusal", err)
	}
}

// A clause keyword is a bare word: one inside a variable's name or a string constant is left alone.
func TestAKeywordInsideANameOrStringIsNotAClause(t *testing.T) {
	src := ns.NewMemSource().Declare("e", "a", "b")
	src.Add("e", ns.Tuple{Vals: []ns.Value{ns.S("order by"), ns.S("limit 1")}})
	src.Add("e", ns.Tuple{Vals: []ns.Value{ns.S("x"), ns.S("y")}})
	rows := eval(t, src, `e(?order, ?limit), ?limit != "offset 2 limit 1" => ?order, ?limit`)
	if got, want := cols(rows, "order", "limit"), []string{"order by limit 1", "x y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

// The default order holds whatever order the facts arrive in, and agrees across evaluators (eval runs
// all three through both()). N(1) and S("1") are two values (#162), each answered once, the number
// among the numbers and the text among the text.
func TestTheDefaultOrderDoesNotDependOnInsertionOrder(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	pool := []ns.Value{ns.Absent(), ns.N(0), ns.N(1), ns.N(2), ns.N(10), ns.N(-3), ns.N(2.5), ns.S("1"), ns.S("b"), ns.S("B"), ns.S("1a")}
	render := func(rows []Row) string {
		var b strings.Builder
		for _, row := range rows {
			switch x := row.Bind["x"]; {
			case x.Absent:
				b.WriteString("absent ")
			case x.Num != nil:
				fmt.Fprintf(&b, "n:%s ", x.S)
			default:
				fmt.Fprintf(&b, "s:%s ", x.S)
			}
		}
		return b.String()
	}
	const want = "absent n:-3 n:0 n:1 n:2 n:2.5 n:10 s:1 s:1a s:B s:b "
	for trial := 0; trial < 20; trial++ {
		src := ns.NewMemSource().Declare("v", "x")
		for _, i := range r.Perm(len(pool)) {
			src.Add("v", ns.Tuple{Vals: []ns.Value{pool[i]}})
		}
		if got := render(eval(t, src, `v(?x) => ?x`)); got != want {
			t.Fatalf("trial %d: %s, want %s", trial, got, want)
		}
	}
}
