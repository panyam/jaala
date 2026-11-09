package translator_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nlmodels/internal/modeltest"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// looker writes the answer right only once a result in its conversation shows "MIT"; blind, it
// guesses the license's spelling. It asks for the values first when it is offered them as a tool.
type looker struct {
	ask, right, wrong string
	asked             []modeltest.Prompt
	offered           [][]string
}

func (m *looker) Offer(names []string, _ map[string]string) { m.offered = append(m.offered, names) }

func (m *looker) Name() string { return "small" }

// With Actions a model looks at the values before answering, and the answer is its first try; the
// step's tokens count in the report. control: the same model without Actions only ever guesses, and
// every guess answers nothing.
func TestActionsLookAtTheDataBeforeAnswering(t *testing.T) {
	v, b := guide.Deps(t)
	m := &looker{ask: "values pkg.license", right: `pkg(?p, "MIT") => ?p`, wrong: `pkg(?p, "mit") => ?p`}
	p, tr, err := translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != m.right || len(p.Rows) != 1 {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if len(tr.Tries) != 1 || len(tr.Steps) != 1 || tr.Steps[0].Action != nl.ActionValues || tr.Steps[0].After != 0 {
		t.Fatalf("tries %d, steps %+v", len(tr.Tries), tr.Steps)
	}
	if res := tr.Steps[0].Result; !strings.Contains(res, `"MIT"`) || !strings.Contains(res, `"Apache-2.0"`) || !strings.Contains(res, "absent") {
		t.Errorf("values sent back %q", res)
	}
	if got := m.offered[0]; !slices.Equal(got, []string{nl.ActionLook, nl.ActionValues, nl.ActionTry}) || m.asked[0].Grammar != "" {
		t.Errorf("the first request offered %v, with a grammar %v", got, m.asked[0].Grammar != "")
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "mit", Question: "which packages are MIT licensed?", Want: [][]string{{"app"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return translate(ctx, q, v, nil, &looker{ask: m.ask, right: m.right, wrong: m.wrong}, nl.Options{Base: b, Actions: &nl.Actions{}})
		})
	if rep.Passed != 1 || rep.PassedFirstTry != 1 || rep.Steps != 1 || rep.TokensOut != 8 || !strings.Contains(rep.String(), "1 tries and 1 steps") {
		t.Errorf("report: %+v\n%s", rep, rep.String())
	}

	blind := &looker{ask: m.ask, right: m.right, wrong: m.wrong}
	_, tr, err = translate(context.Background(), "which packages are MIT licensed?", v, nil, blind, nl.Options{Base: b})
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
	p, tr, err := translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{Steps: 6, Sample: 2}})
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

// Steps run out: once a model has asked Steps actions it is offered none and must answer, under the
// draft's grammar, and an action then is a draft that doesn't parse. A repeated action says so, and
// takes no step.
func TestStepsRunOut(t *testing.T) {
	v, b := guide.Deps(t)
	m := model("small", "look pkg", "look pkg", "values pkg.license", "look pkg", "look pkg")
	_, tr, err := translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, MaxTries: 2, Actions: &nl.Actions{Steps: 2}})
	if err == nil || len(tr.Steps) != 3 || len(tr.Tries) != 2 || tr.Tries[0].Kind != nl.KindParse {
		t.Fatalf("%v, steps %d, tries %+v", err, len(tr.Steps), tr.Tries)
	}
	if !strings.HasPrefix(tr.Steps[1].Result, "You asked that already.") {
		t.Errorf("a repeated look: %q", tr.Steps[1].Result)
	}
	if m.offered[2] == nil {
		t.Errorf("the repeat used up a step: the request after it offered no tools")
	}
	if g := m.asked[3].Grammar; strings.Contains(g, `"look "`) || !strings.HasPrefix(g, "root ::=") || m.offered[3] != nil {
		t.Errorf("after the last step, tools %v and the grammar:\n%.200s", m.offered[3], g)
	}
	if len(m.offered[0]) != 3 || strings.Contains(m.asked[0].Prefix, "values TARGET") {
		t.Errorf("the first request offered %v; the prefix lists the text actions: %v", m.offered[0], strings.Contains(m.asked[0].Prefix, "values TARGET"))
	}
}

func (m *looker) Complete(_ context.Context, p modeltest.Prompt) (modeltest.Completion, error) {
	m.asked = append(m.asked, p)
	for _, t := range p.Turns[1:] {
		if t.Role == nl.RoleUser && strings.Contains(t.Text, `"MIT"`) {
			return modeltest.Completion{Text: m.right, TokensIn: 10, TokensOut: 5}, nil
		}
	}
	if n := len(m.offered); n > 0 && slices.Contains(m.offered[n-1], nl.ActionValues) && len(p.Turns) == 1 {
		return modeltest.Completion{Text: m.ask, TokensIn: 10, TokensOut: 3}, nil
	}
	return modeltest.Completion{Text: m.wrong, TokensIn: 10, TokensOut: 5}, nil
}
