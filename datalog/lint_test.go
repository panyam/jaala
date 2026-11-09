package datalog

import (
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// agni's crystal load-capacitor rule (stdlib/rules/datalog/crystal_load_caps.go in panyam/agni),
// with three's last disequality left as a hole.
const crystal = `two(?y) :- term(?y, ?a), term(?y, ?b), ?a != ?b;
three(?y) :- term(?y, ?a), term(?y, ?b), term(?y, ?c), ?a != ?b, ?a != ?c%s;
term(?y, _), not three(?y) => ?y`

func TestLintNamesTheCrystalRulesMissingDisequality(t *testing.T) {
	got := Lint(mustParse(t, strings.Replace(crystal, "%s", "", 1)))
	if len(got) != 1 {
		t.Fatalf("Lint = %+v, want one warning", got)
	}
	w := got[0]
	if w.Vars != [2]Var{"b", "c"} {
		t.Errorf("Vars = %v, want b and c", w.Vars)
	}
	if w.Atoms != [2]string{"term(?y, ?b)", "term(?y, ?c)"} {
		t.Errorf("Atoms = %q", w.Atoms)
	}
	if !strings.HasPrefix(w.Rule, "three(?y) :-") {
		t.Errorf("Rule = %q, want three's", w.Rule)
	}
	for _, want := range []string{"?b and ?c may bind the same value", "term(?y, ?b) and term(?y, ?c)", "?b != ?c"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("Message %q lacks %q", w.Message, want)
		}
	}
}

func TestLintIsQuietOnTheCrystalRuleAsWritten(t *testing.T) {
	if got := Lint(mustParse(t, strings.Replace(crystal, "%s", ", ?b != ?c", 1))); len(got) != 0 {
		t.Fatalf("Lint = %+v, want none", got)
	}
}

func TestAnyComparisonBetweenThePairQuietsLint(t *testing.T) {
	for _, op := range []string{"!=", "<", "<=", "=", ">", ">="} {
		text := `edge(?x, ?a), edge(?x, ?b), ?a ` + op + ` ?b => ?x`
		if got := Lint(mustParse(t, text)); len(got) != 0 {
			t.Errorf("%s: Lint = %+v, want none", op, got)
		}
	}
	// control: the same goal without the comparison draws the warning, so the goal is linted too
	if got := Lint(mustParse(t, `edge(?x, ?a), edge(?x, ?b) => ?x`)); len(got) != 1 || got[0].Rule != "" {
		t.Fatalf("control: Lint = %+v, want one warning on the goal", got)
	}
}

func TestLintLeavesAtomsThatDifferElsewhereAlone(t *testing.T) {
	for _, text := range []string{
		`edge(?x, ?a), edge(?y, ?b) => ?x`,             // two positions differ
		`edge(?x, ?a), edge(?x, _) => ?x`,              // a wildcard names no variable
		`edge(?x, ?a), edge(?x, "n") => ?x`,            // a constant isn't a variable
		`edge(?x, ?a) as ?r, edge(?x, ?b) as ?s => ?x`, // facts bound by id
		`tt(?r, ?a, ?b), has_tp(?a), has_tp(?b) => ?r`, // tt relates the pair
		`edge(?x, ?a), edge(?x, ?b), not tt(?x, ?a, ?b) => ?x`,
	} {
		if got := Lint(mustParse(t, text)); len(got) != 0 {
			t.Errorf("%s: Lint = %+v, want none", text, got)
		}
	}
}

func TestExplainReportsLintsWarnings(t *testing.T) {
	src := ns.NewMemSource().Declare("term", "part", "net").
		Add("term", ns.Tuple{Vals: []ns.Value{ns.S("X1"), ns.S("n1")}}).
		Add("term", ns.Tuple{Vals: []ns.Value{ns.S("X1"), ns.S("n2")}})
	_, r := reported(t, src, strings.Replace(crystal, "%s", "", 1))
	if len(r.Warnings) != 1 || r.Warnings[0].Vars != [2]Var{"b", "c"} {
		t.Fatalf("Report.Warnings = %+v, want three's ?b and ?c", r.Warnings)
	}
	if s := r.String(); !strings.Contains(s, "warning: in rule \"three(?y) :-") {
		t.Errorf("Report.String lacks the warning:\n%s", s)
	}
}
