package datalog

import (
	"errors"
	"strings"
	"testing"
)

// TestParse (WS3-029): the surface syntax parses atoms, comparisons, term kinds, and the
// projection into the IR the evaluator runs.
func TestParse(t *testing.T) {
	q, err := Parse(`component.mpn(?ref,"REG-24"), net.max_voltage(?net,?v), ?v < 30 => ?ref, ?net`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Goal.Literals) != 3 {
		t.Fatalf("literals = %d, want 3", len(q.Goal.Literals))
	}
	// atom with a variable and a string constant
	a0 := q.Goal.Literals[0].Pos
	if a0 == nil || a0.Relation != "component.mpn" || len(a0.Args) != 2 {
		t.Fatalf("literal 0 = %+v, want component.mpn/2", a0)
	}
	if a0.Args[0].Var != "ref" || a0.Args[1].Const == nil || a0.Args[1].Const.S != "REG-24" {
		t.Errorf("component.mpn args = %+v, want ?ref, \"REG-24\"", a0.Args)
	}
	// comparison against a numeric literal (Num set, so it compares numerically)
	c := q.Goal.Literals[2].Compare
	if c == nil || c.Op != "<" || c.Right.Const == nil || c.Right.Const.Num == nil || *c.Right.Const.Num != 30 {
		t.Errorf("comparison = %+v, want ?v < 30 (numeric)", c)
	}
	if len(q.Select) != 2 || q.Select[0].Var != "ref" || q.Select[1].Var != "net" {
		t.Errorf("select = %v, want [?ref ?net]", q.Select)
	}
}

// TestParseNegationAndAggregate (WS3-029 fast-follow): the surface parses a `not R(...)` literal
// and a func(?x) aggregate projection column.
func TestParseNegationAndAggregate(t *testing.T) {
	q, err := Parse(`component.mpn(?r,?m), not param(?m,"VIN",?v) => ?m`)
	if err != nil {
		t.Fatalf("Parse negation: %v", err)
	}
	if q.Goal.Literals[1].Neg == nil || q.Goal.Literals[1].Neg.Relation != "param" {
		t.Errorf("literal 1 = %+v, want a negated param atom", q.Goal.Literals[1])
	}

	q2, err := Parse(`component-on-net(?r,?n) => ?n, count(?r)`)
	if err != nil {
		t.Fatalf("Parse aggregate: %v", err)
	}
	if len(q2.Select) != 2 || q2.Select[0].Var != "n" || q2.Select[1].Agg == nil ||
		q2.Select[1].Agg.Func != "count" || q2.Select[1].Agg.Var != "r" {
		t.Errorf("select = %+v, want [?n count(?r)]", q2.Select)
	}
}

// TestParseErrors (WS3-029): malformed queries are rejected with an error, not silently mis-parsed.
func TestParseErrors(t *testing.T) {
	for name, text := range map[string]string{
		"empty":            ``,
		"bad term":         `component.mpn(bareword) => ?x`,
		"unterminated str": `component.mpn(?r,"REG) => ?r`,
		"bad projection":   `component.mpn(?r,?m) => notavar`,
		"double arrow":     `component.mpn(?r,?m) => ?r => ?m`,
		"junk literal":     `this is not a query`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); err == nil {
				t.Errorf("Parse(%q) succeeded; want an error", text)
			}
		})
	}
}

func TestParseRejectsEmptyCommaPieces(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"argument between commas", `imports(?a,,?b) => ?a, ?b`, "query: empty term"},
		{"leading argument", `imports(,?b) => ?b`, "query: empty term"},
		{"trailing argument", `imports(?a,) => ?a`, "query: empty term"},
		{"whitespace argument", "imports(?a, \t\n ,?b) => ?a, ?b", "query: empty term"},
		{"only empty arguments", `flag(,)`, "query: empty term"},
		{"negated argument", `not imports(?a,,?b) => ?a`, "query: empty term"},
		{"rule head argument", `r(?a,,?b) :- imports(?a,?b); r(?a,?b)`, "query: empty term"},
		{"typed head argument", `r(?a: string,,?b) :- imports(?a,?b); r(?a,?b)`, "query: empty term"},
		{"leading typed head argument", `r(,?a: string,?b) :- imports(?a,?b); r(?a,?b)`, "query: empty term"},
		{"trailing typed head argument", `r(?a: string,?b,) :- imports(?a,?b); r(?a,?b)`, "query: empty term"},
		{"literal between commas", `imports(?a, ?b), , => ?a`, "query: empty literal"},
		{"leading literal", `, imports(?a, ?b) => ?a`, "query: empty literal"},
		{"trailing literal", `imports(?a, ?b), => ?a`, "query: empty literal"},
		{"whitespace literal", "imports(?a, ?b), \t\n , package(?b) => ?a", "query: empty literal"},
		{"only empty literals", ", \t ,", "query: empty literal"},
		{"rule body literal", `r(?a,?b) :- imports(?a,?b),,package(?b); r(?a,?b)`, "query: empty literal"},
		{"projection between commas", `imports(?a, ?b) => ?a, , ?b`, `query: the projection has an empty column, as in "=> ?a, ?b"`},
		{"leading projection", `imports(?a, ?b) => , ?a`, `query: the projection has an empty column, as in "=> ?a, ?b"`},
		{"trailing projection", `imports(?a, ?b) => ?a,`, `query: the projection has an empty column, as in "=> ?a, ?b"`},
		{"whitespace projection", "imports(?a, ?b) => ?a, \t\n , ?b", `query: the projection has an empty column, as in "=> ?a, ?b"`},
		{"only empty projection columns", `imports(?a, ?b) => ,`, `query: the projection has an empty column, as in "=> ?a, ?b"`},
		{"having between commas", `imports(?a, ?b) => ?a, count(?b) having count(?b) > 0, , count(?b) < 2`, `query: having needs a comparison, as in "having count(?n) > 1"`},
		{"leading having", `imports(?a, ?b) => ?a, count(?b) having , count(?b) > 0`, `query: having needs a comparison, as in "having count(?n) > 1"`},
		{"trailing having", `imports(?a, ?b) => ?a, count(?b) having count(?b) > 0,`, `query: having needs a comparison, as in "having count(?n) > 1"`},
		{"whitespace having", "imports(?a, ?b) => ?a, count(?b) having count(?b) > 0, \t\n , count(?b) < 2", `query: having needs a comparison, as in "having count(?n) > 1"`},
		{"only empty having comparisons", `imports(?a, ?b) => ?a, count(?b) having ,`, `query: having needs a comparison, as in "having count(?n) > 1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.text); err == nil || err.Error() != tc.want {
				t.Errorf("Parse(%q): err = %v, want %q", tc.text, err, tc.want)
			}
		})
	}
}

func TestParseRulesRejectsEmptyCommaPieces(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"head argument", `r(?a,,?b) :- imports(?a,?b);`, "query: empty term"},
		{"typed head argument", `r(?a: string,,?b) :- imports(?a,?b);`, "query: empty term"},
		{"body argument", `r(?a,?b) :- imports(?a,,?b);`, "query: empty term"},
		{"body literal", `r(?a,?b) :- imports(?a,?b),,package(?b);`, "query: empty literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRules(tc.text); err == nil || err.Error() != tc.want {
				t.Errorf("ParseRules(%q): err = %v, want %q", tc.text, err, tc.want)
			}
		})
	}
}

func TestParseCommaControls(t *testing.T) {
	q := mustParse(t, `imports(?a,?b) => ?a, ?b`)
	if len(q.Goal.Literals) != 1 || len(q.Goal.Literals[0].Pos.Args) != 2 || len(q.Select) != 2 {
		t.Fatalf("control: valid imports query = %+v, want one literal, two arguments and two columns", q)
	}
	for _, text := range []string{`imports(?a, ?b) =>`, "imports(?a, ?b) => \t\n"} {
		q := mustParse(t, text)
		if len(q.Goal.Literals) != 1 || len(q.Select) != 0 || len(q.Having) != 0 {
			t.Errorf("control: Parse(%q) = %+v, want default projection and no having", text, q)
		}
	}
	for _, text := range []string{`imports(?a, ?b) => ?a, count(?b)`, "imports(?a, ?b) => ?a, count(?b) having \t\n"} {
		q := mustParse(t, text)
		if len(q.Select) != 2 || q.Select[1].Agg == nil || q.Select[1].Agg.Func != "count" || len(q.Having) != 0 {
			t.Errorf("control: Parse(%q) = %+v, want two columns and no having comparisons", text, q)
		}
	}
	q = mustParse(t, `imports(?a, ?b) => ?a, list(?b) having list(?b) != "a,,b", count(?b) > 0`)
	if len(q.Select) != 2 || len(q.Having) != 2 || q.Having[0].Left.Agg == nil || q.Having[0].Left.Agg.Var != "b" ||
		q.Having[0].Right.Const == nil || q.Having[0].Right.Const.S != "a,,b" || q.Having[1].Left.Agg == nil || q.Having[1].Left.Agg.Func != "count" {
		t.Errorf("control: quoted and nested commas changed the projection or having: %+v", q)
	}
	for _, text := range []string{`flag()`, "flag( \t\n ) =>", `;;; flag(); ;;`} {
		q := mustParse(t, text)
		if len(q.Goal.Literals) != 1 || len(q.Goal.Literals[0].Pos.Args) != 0 || len(q.Select) != 0 {
			t.Errorf("control: Parse(%q) = %+v, want one zero-argument literal and no columns", text, q)
		}
	}
	q = mustParse(t, `imports("a,,b", ?b), package(?b) => ?b`)
	if len(q.Goal.Literals) != 2 || len(q.Goal.Literals[0].Pos.Args) != 2 || q.Goal.Literals[0].Pos.Args[0].Const.S != "a,,b" {
		t.Errorf("control: quoted and nested commas changed the literals or arguments: %+v", q)
	}
	q = mustParse(t, `r(?a: {"api", "cli"}, ?b) :- imports(?a,?b); r(?a,?b)`)
	if len(q.Rules[0].Head.Args) != 2 || len(q.Rules[0].HeadTypes[0].Domain) != 2 {
		t.Errorf("control: nested vocabulary commas changed the typed head: %+v", q.Rules[0])
	}
	q = mustParse(t, `degree(?a, count(?b)) :- imports(?a,?b); degree(?a,?n)`)
	if len(q.Rules[0].Head.Args) != 2 || q.Rules[0].Head.Args[1].Agg == nil {
		t.Errorf("control: nested aggregate changed the rule head: %+v", q.Rules[0])
	}
	for _, text := range []string{"", " \t\n ", ";; # no rules\n;"} {
		if rules, err := ParseRules(text); err != nil || len(rules) != 0 {
			t.Errorf("control: ParseRules(%q) = %v, %v; want no rules and no error", text, rules, err)
		}
	}
	if _, err := Parse(" \t =>"); err == nil || err.Error() != "query: empty query" {
		t.Errorf("control: empty goal err = %v, want query: empty query", err)
	}
}

// TestParseHaving: the group filter parses into Having rather than into the goal, so it is applied
// after the reduce.
func TestParseHaving(t *testing.T) {
	q, err := Parse(`component-on-net(?r,?n) => ?n, count(?r) having count(?r) >= 2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Goal.Literals) != 1 {
		t.Errorf("goal literals = %d, want 1 (the having must not land in the goal)", len(q.Goal.Literals))
	}
	if len(q.Having) != 1 {
		t.Fatalf("Having = %+v, want one filter", q.Having)
	}
	h := q.Having[0]
	if h.Left.Agg == nil || h.Left.Agg.Func != "count" || h.Left.Agg.Var != "r" || h.Op != ">=" || h.Right.Const == nil || h.Right.Const.S != "2" {
		t.Errorf("Having[0] = %+v, want count(?r) >= 2", h)
	}
}

func TestParseHavingErrorContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		having string
		want   string
		cause  string
	}{
		{
			name:   "aggregate on right",
			having: `count(?b) >= count(?a)`,
			want:   `query: having "count(?b) >= count(?a)": bare identifier "count(?a)" — a term must be a ?variable, a "string", or a number`,
			cause:  `query: bare identifier "count(?a)" — a term must be a ?variable, a "string", or a number`,
		},
		{
			name:   "invalid aggregate on left",
			having: `count(0) > 0`,
			want:   `query: having "count(0) > 0": aggregate count(...) expects a ?variable, got "0"`,
			cause:  `query: aggregate count(...) expects a ?variable, got "0"`,
		},
		{
			name:   "invalid variable on left",
			having: `?bad-name > 0`,
			want:   `query: having "?bad-name > 0": variable ?bad-name: a variable's name is letters, digits and _`,
			cause:  `query: variable ?bad-name: a variable's name is letters, digits and _`,
		},
		{
			name:   "empty term on right",
			having: `count(?b) >=`,
			want:   `query: having "count(?b) >=": empty term`,
			cause:  `query: empty term`,
		},
		{
			name:   "query prefix in user text",
			having: `count(?b) >= query: value`,
			want:   `query: having "count(?b) >= query: value": bare identifier "query: value" — a term must be a ?variable, a "string", or a number`,
			cause:  `query: bare identifier "query: value" — a term must be a ?variable, a "string", or a number`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(`imports(?a, ?b) => ?a, count(?b) having ` + tc.having)
			if err == nil {
				t.Fatal("Parse succeeded; want a having error")
			}
			t.Run("message", func(t *testing.T) {
				if err.Error() != tc.want {
					t.Errorf("err = %q, want %q", err, tc.want)
				}
			})
			t.Run("cause", func(t *testing.T) {
				cause := errors.Unwrap(err)
				if cause == nil {
					t.Fatal("having error lost its underlying parse error")
				}
				if cause.Error() != tc.cause {
					t.Errorf("cause = %q, want %q", cause, tc.cause)
				}
				if errors.Unwrap(err) != cause || !errors.Is(err, cause) {
					t.Error("having error does not preserve its cause's identity")
				}
			})
		})
	}
}

// TestParseHavingRejectsAPlainComparison: a filter over a group key is a goal comparison written in
// the wrong place, and the error says so rather than silently accepting a filter that never fires.
func TestParseHavingRejectsAPlainComparison(t *testing.T) {
	_, err := Parse(`component-on-net(?r,?n) => ?n having ?n < 2`)
	if err == nil || !strings.Contains(err.Error(), "=>") {
		t.Errorf("err = %v, want a complaint pointing at the goal", err)
	}
}

// TestParseHavingKeywordNeedsAWordBoundary: "having" splits the projection only as a bare word, so a
// name that merely contains those letters is left alone.
func TestParseHavingKeywordNeedsAWordBoundary(t *testing.T) {
	q, err := Parse(`shaving(?r,?n) => ?n, ?r`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Having) != 0 {
		t.Errorf("Having = %+v, want none: the relation is named shaving", q.Having)
	}
}

// TestParseDistinctAggregate: distinct is a modifier inside the parens, and it labels its own column
// so a projection may carry both spellings of one aggregate.
func TestParseDistinctAggregate(t *testing.T) {
	q, err := Parse(`component-on-net(?r,?n) => ?n, count(?r), count(distinct ?r), list(distinct ?r)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Select) != 4 {
		t.Fatalf("Select = %+v, want four columns", q.Select)
	}
	if q.Select[1].Agg.Distinct {
		t.Error("count(?r) parsed as distinct")
	}
	if !q.Select[2].Agg.Distinct || q.Select[2].Agg.Var != "r" {
		t.Errorf("count(distinct ?r) = %+v", q.Select[2].Agg)
	}
	if !q.Select[3].Agg.Distinct || q.Select[3].Agg.Func != "list" {
		t.Errorf("list(distinct ?r) = %+v", q.Select[3].Agg)
	}
	cols := q.Columns()
	if len(cols) != 4 || cols[1] != "count(r)" || cols[2] != "count(distinct r)" {
		t.Errorf("Columns() = %v, want the two count spellings to be different columns", cols)
	}
}

// TestParseDistinctNeedsWhitespace: a variable whose name starts with the keyword is not a modifier.
func TestParseDistinctNeedsWhitespace(t *testing.T) {
	q, err := Parse(`component-on-net(?r,?distinctive) => ?r, count(?distinctive)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a := q.Select[1].Agg; a.Distinct || a.Var != "distinctive" {
		t.Errorf("agg = %+v, want a plain count over ?distinctive", a)
	}
}

func TestCommentsRunToTheEndOfTheLineOutsideStrings(t *testing.T) {
	q := mustParse(t, "# nodes named with a hash\nnode(?n), ?n = \"a#b\" # trailing\n=> ?n")
	if len(q.Goal.Literals) != 2 || q.Goal.Literals[1].Compare.Right.Const.S != "a#b" || len(q.Select) != 1 {
		t.Errorf("parsed %+v, want two literals keeping \"a#b\" and one column", q)
	}
}
