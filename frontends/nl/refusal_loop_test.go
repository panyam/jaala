package nl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/internal/guide"
)

// A turn started from a refused draft answers a try of that draft from its refusal, taking no step
// (#567). control: a draft the pack's Check refused still runs when tried, since a try isn't judged
// by Check.
func TestATryOfTheDraftATurnStartsFromIsARepeat(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 3}})
	nope := `pkg(?p, "nope") => ?p`
	draft, _, err := s.Draw(ctx, reply(nope))
	if err == nil {
		t.Fatal("the draft was accepted")
	}
	c := s.NewConversation(nil, true)
	c.From(draft)
	if !c.Repeats(nl.ActionTry, nope) {
		t.Error("a try of the draft isn't a repeat")
	}
	got := c.Act(ctx, nl.ActionTry, nope)
	if !strings.HasPrefix(got, d.TriedAgain+"\n") || !strings.Contains(got, `"nope"`) || c.Steps() != 0 {
		t.Errorf("the try answered %q, steps %d", got, c.Steps())
	}

	checked := s.NewConversation(nil, true)
	checked.From(nl.Try{Model: "small", Text: mit, Kind: nl.KindAnswer, Err: errors.New("far too many rows")})
	if checked.Repeats(nl.ActionTry, mit) {
		t.Error("control: a try of a draft Check refused is a repeat")
	}
	if got := checked.Act(ctx, nl.ActionTry, mit); strings.HasPrefix(got, d.TriedAgain) || checked.Steps() != 1 {
		t.Errorf("control: the try answered %q, steps %d", got, checked.Steps())
	}
}

// A try that answers nothing carries the pack's Hint, as an answer's refusal does (#567). control:
// the pack's Check, which refuses everything here, still doesn't judge a try.
func TestATryKeepsThePacksHint(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	const hint = "the licenses are MIT and Apache-2.0"
	pack := &nl.Pack{
		Hint:  func(context.Context, *datalog.EmptyReport) string { return hint },
		Check: func(context.Context, nl.Program) error { return errors.New("the pack refuses every answer") },
	}
	s, err := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Pack: pack, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	c := s.NewConversation(nil, true)
	if got := c.Act(ctx, nl.ActionTry, `pkg(?p, "nope") => ?p`); !strings.Contains(got, hint) {
		t.Errorf("the empty try answered %q, without the hint", got)
	}
	if got := c.Act(ctx, nl.ActionTry, mit); strings.Contains(got, "refuses") || !strings.Contains(got, "answered") {
		t.Errorf("control: the try answered %q", got)
	}
}

// A note is a thought on one line: the action grammar takes it, it takes a step and no try, and
// the Ledger leaves it out (#567). The grammar no longer takes prose run into one name. control:
// the program still parses under it.
func TestANoteIsAThoughtThatTakesAStep(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 2}})
	_, text := s.TextActions()
	g, err := gbnf.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	thought := "The draft ran past its budget, so bind ?p first."
	for _, c := range []struct {
		reply string
		in    bool
	}{
		{"note " + thought, true},
		{"Thequerywasrefusedbecauseitranse(1000001)", false},
		{"note \n" + mit, false},
		{mit, true},
	} {
		if got := g.Accepts(c.reply); got != c.in {
			t.Errorf("the action grammar accepts %q: %v, want %v", c.reply, got, c.in)
		}
	}
	for _, r := range []string{"note", "note " + thought + "\n" + mit} {
		if _, _, ok := nl.ReadAction(r); ok {
			t.Errorf("ReadAction read %q as an action", r)
		}
	}
	action, arg, ok := nl.ReadAction("note " + thought)
	if !ok || action != nl.ActionNote || arg != thought {
		t.Fatalf("ReadAction: %q %q %v", action, arg, ok)
	}
	c := s.NewConversation(nil, true)
	if got := c.Act(ctx, action, arg); got != d.Noted || c.Steps() != 1 || c.Ledger() != "" || c.Repeats(action, arg) {
		t.Errorf("the note answered %q, steps %d, ledger %q, repeat %v", got, c.Steps(), c.Ledger(), c.Repeats(action, arg))
	}
	if got := c.Act(ctx, action, arg); got != d.Noted+"\n\n"+d.LastLook || c.Steps() != 2 {
		t.Errorf("the last note answered %q, steps %d", got, c.Steps())
	}
}

// A draft the work budget stopped is told the literal that did the most work and what to change
// (#544), and errors.As still finds the budget's error. control: the same draft under a budget it
// fits answers.
func TestABudgetRefusalNamesWhereTheWorkWent(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	cross := `pkg(?a, ?l), pkg(?b, ?m), pkg(?c, ?n) => ?a`
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Eval: []datalog.Option{datalog.Budget(40)}})
	try, _, err := s.Draw(ctx, reply(cross))
	var be *datalog.BudgetExceeded
	var bs *nl.BudgetStopped
	if !errors.As(err, &be) || !errors.As(err, &bs) {
		t.Fatalf("the draft's error %v", err)
	}
	if bs.Literal != "pkg(?c, ?n)" || bs.Rule != "" {
		t.Errorf("the hot literal %q in rule %q", bs.Literal, bs.Rule)
	}
	told := nl.Explain(cross, err)
	d := nl.DefaultPrompts()
	if !strings.HasPrefix(told, be.Error()+"\nMost of the work went to pkg(?c, ?n): ") || !strings.Contains(told, "(read by scan)") || !strings.HasSuffix(told, "\n"+d.BudgetFix) {
		t.Errorf("the draft was told %q", told)
	}
	if try.Class() != nl.ClassBudget {
		t.Errorf("class %q", try.Class())
	}
	roomy, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Eval: []datalog.Option{datalog.Budget(1_000_000)}})
	if _, _, err := roomy.Draw(ctx, reply(cross)); err != nil {
		t.Errorf("control: under a roomy budget %v", err)
	}
}

// A literal in a rule's body is named with its rule (Prompts.BudgetInRule).
func TestABudgetRefusalNamesTheRuleTheLiteralIsIn(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	// two rules, so pair isn't inlined into the goal
	draft := `pair(?a, ?b) :- pkg(?a, ?l), pkg(?b, ?m), pkg(?c, ?n); pair(?a, ?b) :- pkg(?a, "MIT"), pkg(?b, "MIT"); pair(?x, ?y) => ?x`
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Eval: []datalog.Option{datalog.Budget(40)}})
	_, _, err := s.Draw(ctx, reply(draft))
	var bs *nl.BudgetStopped
	if !errors.As(err, &bs) {
		t.Fatalf("the draft's error %v", err)
	}
	if bs.Rule != `pair(?a, ?b) :- pkg(?a, ?l), pkg(?b, ?m), pkg(?c, ?n)` || !strings.HasPrefix(bs.Where, "Most of the work went to "+bs.Literal+" in the rule "+bs.Rule+": ") {
		t.Errorf("literal %q, rule %q, told %q", bs.Literal, bs.Rule, bs.Where)
	}
}
