package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// A draft guessing a spelling of a value the conversation has seen is repaired by the harness and
// accepted, recorded as a try of its own after the model's. control: when the conversation saw no
// values, the same draft goes back to the model as before.
func TestARepairFixesASpellingTheConversationSaw(t *testing.T) {
	v, b := guide.Deps(t)
	guess := `pkg(?p, "mit") => ?p`
	o := nl.Options{Base: b, Actions: &nl.Actions{}}
	p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", "values pkg.license", guess), o)
	if err != nil || p.Text != mit || len(p.Rows) != 1 {
		t.Fatalf("Translate = %q %v", p.Text, err)
	}
	if len(tr.Tries) != 2 || tr.Tries[0].Kind != nl.KindEmpty || tr.Tries[1].Repair != `"mit" to "MIT"` || tr.Tries[1].Err != nil {
		t.Errorf("tries %+v", tr.Tries)
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "mit", Question: mitQ, Want: [][]string{{"app"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, model("small", "values pkg.license", guess), o)
		})
	if rep.Passed != 1 || rep.Repairs != 1 {
		t.Errorf("report: passed %d, repairs %d", rep.Passed, rep.Repairs)
	}
	_, tr, err = nl.Translate(context.Background(), mitQ, v, nil, model("small", guess), o)
	if err == nil || repaired(tr) {
		t.Errorf("control: nothing seen, %v %+v", err, tr.Tries)
	}
}

// repaired reports whether any try in tr is one the harness repaired.
func repaired(tr nl.Trace) bool {
	for _, try := range tr.Tries {
		if try.Repair != "" {
			return true
		}
	}
	return false
}

// A value a closed Domain refuses is repaired from the Domain, which the check names, though the
// conversation saw nothing.
func TestARepairFixesASpellingTheDomainNames(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{}, {Domain: []string{"func", "file"}}}})
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("f:main"), ns.S("func")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	p, tr, err := nl.Translate(context.Background(), "which functions are there?", v, nil, model("small", `node(?n, "Func") => ?n`), nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != `node(?n, "func") => ?n` || len(tr.Tries) != 2 || tr.Tries[1].Repair != `"Func" to "func"` {
		t.Errorf("%q %v, tries %+v", p.Text, err, tr.Tries)
	}
	if _, tr, err := nl.Translate(context.Background(), "which functions are there?", v, nil, model("small", `node(?n, "Func") => ?n`), nl.Options{Base: b}); err == nil || repaired(tr) {
		t.Errorf("control: without Actions, nothing is repaired: %v %+v", err, tr.Tries)
	}
}

// Compact sends one turn in place of the conversation: the question, each action and refusal as a
// fact, and the last refusal whole. control: without it, the conversation grows a turn pair per reply.
func TestCompactSendsWhatWasFoundInPlaceOfTheConversation(t *testing.T) {
	v, b := guide.Deps(t)
	replies := []string{"values pkg.license", "look pkg", `pkg(?p, "nope") => ?p`, mit}
	m := model("small", replies...)
	p, _, err := nl.Translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{Compact: true}})
	if err != nil || p.Text != mit {
		t.Fatalf("Translate = %q %v", p.Text, err)
	}
	for i, q := range m.asked {
		if len(q.Turns) != 1 {
			t.Errorf("ask %d sent %d turns", i, len(q.Turns))
		}
	}
	last := m.asked[len(m.asked)-1].Turns[0].Text
	for _, want := range []string{mitQ, "What you have found so far:", "- values pkg.license", `"Apache-2.0"`, "- look pkg", `- pkg(?p, "nope") => ?p`, `refused (empty): the program answered no rows: pkg(?p, "nope") matched nothing`, "Your last answer was refused:"} {
		if !strings.Contains(last, want) {
			t.Errorf("the last turn lacks %q:\n%s", want, last)
		}
	}
	if m.asked[0].Turns[0].Text != mitQ {
		t.Errorf("the first turn = %q, want the question alone", m.asked[0].Turns[0].Text)
	}
	plain := model("small", replies...)
	if _, _, err := nl.Translate(context.Background(), mitQ, v, nil, plain, nl.Options{Base: b, Actions: &nl.Actions{}}); err != nil || len(plain.asked[3].Turns) != 7 {
		t.Errorf("control: the conversation's last ask sent %d turns: %v", len(plain.asked[3].Turns), err)
	}
}

// A repair that still answers nothing isn't kept: the model's own draft is what was refused, and what
// goes back to it.
func TestAFailedRepairLeavesTheModelsDraft(t *testing.T) {
	v, b := guide.Deps(t)
	guess := `pkg(?p, "mit"), pkg(?p, "nope") => ?p`
	m := model("small", "values pkg.license", guess, mit)
	p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != mit || len(tr.Tries) != 2 || tr.Tries[0].Text != guess || repaired(tr) {
		t.Fatalf("%q %v, tries %+v", p.Text, err, tr.Tries)
	}
	if turns := m.asked[2].Turns; turns[len(turns)-2].Text != guess {
		t.Errorf("the refused draft sent back was %q", turns[len(turns)-2].Text)
	}
}

// A fallback starts its compact state afresh: its first turn is the question alone.
func TestACompactFallbackStartsFresh(t *testing.T) {
	v, b := guide.Deps(t)
	big := model("big", mit)
	_, _, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", "values pkg.license", `pkg(?p, "nope") => ?p`), nl.Options{Base: b, Actions: &nl.Actions{Compact: true}, Fallback: big})
	if err != nil || len(big.asked) != 1 || strings.Contains(big.asked[0].Turns[0].Text, "found so far") {
		t.Errorf("%v: the fallback's first turn:\n%s", err, big.asked[0].Turns[0].Text)
	}
}
