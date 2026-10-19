package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// looker writes the answer right only once a result in its conversation shows "MIT"; blind, it
// guesses the license's spelling. It asks for the values first when the prompt says it may.
type looker struct {
	ask, right, wrong string
	asked             []nl.Prompt
}

func (m *looker) Name() string { return "small" }

func (m *looker) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	m.asked = append(m.asked, p)
	for _, t := range p.Turns[1:] {
		if t.Role == nl.RoleUser && strings.Contains(t.Text, `"MIT"`) {
			return nl.Completion{Text: m.right, TokensIn: 10, TokensOut: 5}, nil
		}
	}
	if strings.Contains(p.Prefix, "values TARGET") && len(p.Turns) == 1 {
		return nl.Completion{Text: m.ask, TokensIn: 10, TokensOut: 3}, nil
	}
	return nl.Completion{Text: m.wrong, TokensIn: 10, TokensOut: 5}, nil
}

// With Actions a model looks at the values before answering, and the answer is its first try; the
// step's tokens count in the report. control: the same model without Actions only ever guesses, and
// every guess answers nothing.
func TestActionsLookAtTheDataBeforeAnswering(t *testing.T) {
	v, b := guide.Deps(t)
	m := &looker{ask: "values pkg.license", right: `pkg(?p, "MIT") => ?p`, wrong: `pkg(?p, "mit") => ?p`}
	p, tr, err := nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != m.right || len(p.Rows) != 1 {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if len(tr.Tries) != 1 || len(tr.Steps) != 1 || tr.Steps[0].Action != nl.ActionValues || tr.Steps[0].After != 0 {
		t.Fatalf("tries %d, steps %+v", len(tr.Tries), tr.Steps)
	}
	if res := tr.Steps[0].Result; !strings.Contains(res, `"MIT"`) || !strings.Contains(res, `"Apache-2.0"`) || !strings.Contains(res, "absent") {
		t.Errorf("values sent back %q", res)
	}
	if g := m.asked[0].Grammar; !strings.HasPrefix(g, `root ::= "look " target | "values " target`) || !strings.Contains(g, "program ::=") {
		t.Errorf("the grammar doesn't take actions:\n%.200s", g)
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "mit", Question: "which packages are MIT licensed?", Want: [][]string{{"app"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, &looker{ask: m.ask, right: m.right, wrong: m.wrong}, nl.Options{Base: b, Actions: &nl.Actions{}})
		})
	if rep.Passed != 1 || rep.PassedFirstTry != 1 || rep.Steps != 1 || rep.TokensOut != 8 || !strings.Contains(rep.String(), "1 tries and 1 steps") {
		t.Errorf("report: %+v\n%s", rep, rep.String())
	}

	blind := &looker{ask: m.ask, right: m.right, wrong: m.wrong}
	_, tr, err = nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, blind, nl.Options{Base: b})
	if err == nil || len(tr.Steps) != 0 || tr.Tries[0].Kind != nl.KindEmpty {
		t.Errorf("control: without Actions, %v, steps %d, %+v", err, len(tr.Steps), tr.Tries)
	}
}

// A try runs a draft on a few rows and sends back its rows, or why it answered none, without
// spending a try; look describes a member, and a mistaken look or values says what's there.
func TestEachActionSendsBackWhatItFound(t *testing.T) {
	v, b := guide.Deps(t)
	m := model("small",
		"try\npkg(?p, \"mit\") => ?p",
		"try pkg(?p, _) => ?p",
		"look pkg",
		"look pkgs",
		"values pkg.nope",
		"values pkg.1",
		`pkg(?p, "MIT") => ?p`)
	p, tr, err := nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{Steps: 6, Sample: 2}})
	if err != nil || p.Text != `pkg(?p, "MIT") => ?p` || len(tr.Tries) != 1 || len(tr.Steps) != 6 {
		t.Fatalf("Translate = %+v, %v, %d tries, %d steps", p, err, len(tr.Tries), len(tr.Steps))
	}
	for i, want := range []string{
		`pkg(?p, "mit") matched nothing, even alone`,
		"it answered its first 2 rows:\n  \"api\"\n  \"app\"",
		"pkg(name, license)",
		`did you mean "pkg"?`,
		"pkg's arguments are name, license",
		"values of pkg.1 answered its first 2 rows:",
	} {
		if res := tr.Steps[i].Result; !strings.Contains(res, want) {
			t.Errorf("step %d (%s): sent back %q, want %q in it", i+1, tr.Steps[i].Text, res, want)
		}
	}
	if !strings.Contains(tr.Steps[5].Result, "That was the last look") {
		t.Errorf("the last step didn't say so: %q", tr.Steps[5].Result)
	}
}

// Steps run out: once a model has asked Steps actions it must answer, under the draft's grammar, and
// an action then is a draft that doesn't parse. A repeated action says so.
func TestStepsRunOut(t *testing.T) {
	v, b := guide.Deps(t)
	m := model("small", "look pkg", "look pkg", "look pkg", "look pkg")
	_, tr, err := nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, MaxTries: 2, Actions: &nl.Actions{Steps: 2}})
	if err == nil || len(tr.Steps) != 2 || len(tr.Tries) != 2 || tr.Tries[0].Kind != nl.KindParse {
		t.Fatalf("%v, steps %d, tries %+v", err, len(tr.Steps), tr.Tries)
	}
	if !strings.HasPrefix(tr.Steps[1].Result, "You asked that already.") {
		t.Errorf("a repeated look: %q", tr.Steps[1].Result)
	}
	if g := m.asked[2].Grammar; strings.Contains(g, `"look "`) || !strings.HasPrefix(g, "root ::=") {
		t.Errorf("the grammar after the last step still takes actions:\n%.200s", g)
	}
	if !strings.Contains(m.asked[0].Prefix, "values TARGET") || strings.Contains(model("x").Name(), "?") {
		t.Errorf("the prefix doesn't list the actions")
	}
}
