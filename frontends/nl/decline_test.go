package nl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/gbnf"
)

const declineReply = "none: nothing here holds who maintains a package"

// With Abstain.Declined the prefix tells the model how to decline and every grammar it may be held
// to takes a decline, as well as a program. control: without it the prefix says nothing of it and
// the grammar refuses the reply, and a program is accepted either way.
func TestADeclineIsOfferedOnlyWhenAsked(t *testing.T) {
	v := vocabulary(t)
	for _, declined := range []bool{true, false} {
		o := nl.Options{Actions: &nl.Actions{}}
		if declined {
			o.Abstain = &nl.Abstain{Declined: true}
		}
		s, err := nl.NewSession(context.Background(), "q", v, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(s.Prefix(), "reply none:"); got != declined {
			t.Errorf("declined %v: the prefix offers a decline %v", declined, got)
		}
		_, text := s.TextActions()
		for name, g := range map[string]string{"answer": s.Grammar(), "text actions": text, "final": s.FinalGrammar()} {
			gr, err := gbnf.Parse(g)
			if err != nil {
				t.Fatalf("declined %v, %s grammar: %v", declined, name, err)
			}
			if got := gr.Accepts(declineReply); got != declined {
				t.Errorf("declined %v: the %s grammar accepts a decline %v", declined, name, got)
			}
			if !gr.Accepts(`graph.reach("a", ?b) => ?b`) {
				t.Errorf("declined %v: the %s grammar refuses a program", declined, name)
			}
			if gr.Accepts("none: two\nlines") {
				t.Errorf("declined %v: the %s grammar takes a reason over two lines", declined, name)
			}
		}
	}
}

// A decline ends the model's turn with a *Declined carrying its reason, recorded as a try of
// KindDeclined classed as an abstention, unchecked and never a repeat. control: without
// Abstain.Declined the same reply is a draft, refused as one.
func TestADeclineEndsTheTurn(t *testing.T) {
	v := vocabulary(t)
	s, err := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Abstain: &nl.Abstain{Declined: true}})
	if err != nil {
		t.Fatal(err)
	}
	vd := s.NewConversation(nil, true).Judge(context.Background(), reply(declineReply))
	var d *nl.Declined
	if vd.Program != nil || !errors.As(vd.Stop, &d) || d.Why != "nothing here holds who maintains a package" || d.Model != "small" {
		t.Fatalf("verdict %+v", vd)
	}
	if len(vd.Tries) != 1 || vd.Tries[0].Kind != nl.KindDeclined || vd.Tries[0].Class() != nl.ClassAbstained {
		t.Errorf("tries %+v", vd.Tries)
	}
	for _, half := range []string{"none:", "none:   \n "} {
		if vd := s.NewConversation(nil, true).Judge(context.Background(), reply(half)); vd.Tries[0].Kind == nl.KindDeclined {
			t.Errorf("%q read as a decline", half)
		}
	}
	// with a decline offered, a program is still judged as one
	if vd := s.NewConversation(nil, true).Judge(context.Background(), reply(`graph.reach("a", ?b) => ?b`)); vd.Program == nil {
		t.Errorf("a program under Declined wasn't accepted: %+v", vd)
	}
	// a reasoning model, held to no grammar, explaining over two lines: still a decline
	vd = s.NewConversation(nil, true).Judge(context.Background(), reply("none: no relation holds maintainers.\nThe vocabulary has pkg and imports."))
	if !errors.As(vd.Stop, &d) || d.Why != "no relation holds maintainers. The vocabulary has pkg and imports." {
		t.Errorf("a decline over two lines: %+v", vd)
	}
	plain, err := nl.NewSession(context.Background(), "q", v, nil, nl.Options{})
	if err != nil {
		t.Fatal(err)
	}
	vd = plain.NewConversation(nil, true).Judge(context.Background(), reply(declineReply))
	if vd.Tries[0].Kind == nl.KindDeclined || errors.As(vd.Stop, &d) || vd.Tries[0].Err == nil {
		t.Errorf("control: without Declined, %+v", vd)
	}
}

// Abstain.Judge turns a run that ended on a decline into an abstention with the model's words, and
// offers no draft from it; Eval scores the case as an abstention. control: without Declined set
// the decline isn't an abstention.
func TestADeclinedRunAbstains(t *testing.T) {
	dec := &nl.Declined{Model: "big", Why: "no maintainers"}
	tr := nl.Trace{Tries: []nl.Try{{Model: "big", Text: declineReply, Kind: nl.KindDeclined, Err: dec}}}
	err := errors.Join(nl.ErrNoProgram, dec)
	_, _, got := (&nl.Abstain{Declined: true}).Judge(nl.Program{}, tr, err)
	var ab *nl.Abstained
	if !errors.As(got, &ab) || ab.Reason != nl.AbstainDeclined || ab.Diagnosis != "no maintainers" || ab.Draft.Text != "" {
		t.Fatalf("judged %v", got)
	}
	if _, _, got := (&nl.Abstain{}).Judge(nl.Program{}, tr, err); errors.As(got, &ab) {
		t.Errorf("control: without Declined a decline abstained: %v", got)
	}
	// a budget that ran out after a decline: the abstention offers a refused draft, never the decline
	// (the decline last, since closest takes the later of two drafts it ranks the same)
	spent := nl.Trace{Tries: []nl.Try{{Model: "big", Text: "x(", Kind: nl.KindParse, Err: errors.New("parse")}, tr.Tries[0]}}
	_, _, got = (&nl.Abstain{Budget: true, Declined: true}).Judge(nl.Program{}, spent, &nl.BudgetSpent{})
	if !errors.As(got, &ab) || ab.Reason != nl.AbstainBudget || ab.Draft.Text != "x(" {
		t.Errorf("budget after a decline: %v, draft %q", got, ab.Draft.Text)
	}
	_, b := depsBase(t)
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "missing", Question: "q", Want: [][]string{{"x"}}}}, b,
		func(context.Context, string) (nl.Program, nl.Trace, error) {
			return (&nl.Abstain{Declined: true}).Judge(nl.Program{}, tr, err)
		})
	if r := rep.Cases[0]; r.Outcome() != nl.OutcomeAbstain || r.AbstainReason() != nl.AbstainDeclined {
		t.Errorf("Eval scored %q (%q)", r.Outcome(), r.AbstainReason())
	}
}
