package nl_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

const (
	mitQ   = "which packages are MIT licensed?"
	mit    = `pkg(?p, "MIT") => ?p`
	mitToo = `pkg(?x, "MIT") => ?x` // other names, the same rows
	apache = `pkg(?p, "Apache-2.0") => ?p`
	broken = `nope(?p) => ?p`
)

// A vote draws more answers from the model that answered, each asked with its Draw and the answer's
// grammar, and keeps the program whose rows most drafts answer, first among equals; a refused draft
// agrees with nothing, and drafts agree by rows whatever their variables are named. control: a vote of
// one draft draws nothing more.
func TestAVoteKeepsTheRowsMostDraftsAgreeOn(t *testing.T) {
	v, b := guide.Deps(t)
	for _, c := range []struct {
		name           string
		drafts         int
		replies        []string
		want           string
		agreed, voters int
	}{
		{"outvoted", 3, []string{mit, apache, apache}, apache, 2, 3},
		{"by rows", 3, []string{mit, mitToo, broken}, mit, 2, 3},
		{"a tie keeps the first", 3, []string{mit, apache, broken}, mit, 1, 3},
		{"refused drafts agree with nothing", 3, []string{mit, broken, `nada(?p) => ?p`}, mit, 1, 3},
		{"rows in any order", 2, []string{`pkg(?p, _) => ?p`, `pkg(?p, _) => ?p order by ?p desc`}, `pkg(?p, _) => ?p`, 2, 2},
		{"control: one draft", 1, []string{mit}, mit, 1, 1},
	} {
		m := model("small", c.replies...)
		p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Vote: &nl.Vote{Drafts: c.drafts}})
		if err != nil || p.Text != c.want || p.Confidence.Agreed != c.agreed || p.Confidence.Drawn != c.voters || !p.Confidence.Ran {
			t.Errorf("%s: %q %+v %v, want %q agreed %d of %d", c.name, p.Text, p.Confidence, err, c.want, c.agreed, c.voters)
			continue
		}
		if len(tr.Tries) != 1 || len(tr.Votes) != c.drafts-1 || len(m.asked) != c.drafts {
			t.Errorf("%s: %d tries, %d votes, %d asked", c.name, len(tr.Tries), len(tr.Votes), len(m.asked))
		}
		for i, q := range m.asked {
			if q.Draw != i || q.Grammar != m.asked[0].Grammar {
				t.Errorf("%s: ask %d had Draw %d", c.name, i, q.Draw)
			}
		}
	}
}

// Parallel draws the other drafts at once, from a model safe to call concurrently: each waits for
// the others to arrive. control: drawn one at a time, every draft waits alone and fails.
func TestAParallelVoteDrawsTheDraftsAtOnce(t *testing.T) {
	v, b := guide.Deps(t)
	for _, parallel := range []bool{true, false} {
		m := &byDraw{replies: []string{mit, apache, apache, apache}, arrived: make(chan struct{}, 3)}
		p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Vote: &nl.Vote{Drafts: 4, Parallel: parallel}})
		agreed := p.Text == apache && p.Confidence.Agreed == 3
		if err != nil || len(tr.Votes) != 3 || agreed != parallel {
			t.Errorf("parallel %v: %q %+v %v", parallel, p.Text, p.Confidence, err)
		}
	}
}

// byDraw answers each prompt by its Draw, and a vote's draft only once all three of a vote's drafts
// have been asked, so only drafts asked at once are answered.
type byDraw struct {
	replies []string
	arrived chan struct{}
}

func (m *byDraw) Name() string { return "drawn" }
func (m *byDraw) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	if p.Draw > 0 {
		m.arrived <- struct{}{}
		deadline := time.After(time.Second)
		for len(m.arrived) < cap(m.arrived) {
			select {
			case <-deadline:
				return nl.Completion{}, errors.New("drawn alone")
			case <-time.After(time.Millisecond):
			}
		}
	}
	return nl.Completion{Text: m.replies[p.Draw], TokensIn: 10, TokensOut: 5}, nil
}

// Without a Base to run them on, drafts agree when they are the same program.
func TestWithoutABaseDraftsAgreeOnTheirProgram(t *testing.T) {
	v, _ := guide.Deps(t)
	for _, c := range []struct {
		replies []string
		agreed  int
	}{{[]string{mit, mit}, 2}, {[]string{mit, mitToo}, 1}} {
		p, _, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", c.replies...), nl.Options{Vote: &nl.Vote{Drafts: 2}})
		if err != nil || p.Confidence.Agreed != c.agreed || p.Confidence.Ran {
			t.Errorf("%q: %+v %v, want %d agreeing", c.replies, p.Confidence, err, c.agreed)
		}
	}
}

// An answer whose drafts agree less than Escalate asks goes on to the fallback, which starts afresh;
// when the fallback answers nothing better, the first such answer comes back rather than an error.
// The last model's answer is never escalated, since nothing follows it. control: without Escalate the
// small model's answer stands.
func TestEscalateMovesADisputedAnswerToTheFallback(t *testing.T) {
	v, b := guide.Deps(t)
	o := nl.Options{Base: b, Vote: &nl.Vote{Drafts: 3}, Escalate: 0.6}
	disputed := []string{mit, apache, broken}

	o.Fallback = model("big", apache, apache, apache)
	p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", disputed...), o)
	if err != nil || p.Model != "big" || p.Text != apache || p.Confidence.Agreement() != 1 {
		t.Errorf("escalated: %s %q %+v %v", p.Model, p.Text, p.Confidence, err)
	}
	if len(tr.Escalations) != 1 || tr.Escalations[0].Model != "small" || tr.Escalations[0].Confidence.Agreed != 1 {
		t.Errorf("escalations %+v", tr.Escalations)
	}

	o.Fallback = model("big", mit, apache, broken)
	if p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", disputed...), o); err != nil || p.Model != "big" || len(tr.Escalations) != 1 {
		t.Errorf("the fallback's own disputed answer: %s %+v %v", p.Model, tr.Escalations, err)
	}

	o.Fallback = model("big", "!fail")
	if p, _, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", disputed...), o); err != nil || p.Model != "small" || p.Text != mit {
		t.Errorf("the fallback failing: %s %q %v, want small's answer back", p.Model, p.Text, err)
	}

	rep := nl.Eval(context.Background(), []nl.Case{{Name: "apache", Question: mitQ, Want: [][]string{{"api"}, {"log"}, {"util"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			o := o
			o.Fallback = model("big", apache, apache, apache)
			return nl.Translate(ctx, q, v, nil, model("small", disputed...), o)
		})
	if rep.Votes != 4 || rep.Escalations != 1 || !strings.Contains(rep.String(), "4 votes, 1 escalated") {
		t.Errorf("report: %d votes, %d escalated\n%s", rep.Votes, rep.Escalations, rep.String())
	}

	o.Escalate, o.Fallback = 0, model("big", apache, apache, apache)
	if p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", disputed...), o); err != nil || p.Model != "small" || len(tr.Escalations) != 0 {
		t.Errorf("control: without Escalate, %s %v", p.Model, err)
	}
}

// A Budget stops Translate before the call that would pass it, with a BudgetSpent the report classes
// as budget; Time runs out the same way. control: unbudgeted, the model gets every try.
func TestABudgetStopsTranslate(t *testing.T) {
	v, b := guide.Deps(t)
	wrong, wrong2, wrong3 := `pkg(?p, "mit") => ?p`, `pkg(?q, "mit") => ?q`, `pkg(?r, "mit") => ?r`
	tokens := nl.Options{Base: b, MaxTries: 5, Budget: nl.Budget{Tokens: 250}}
	_, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", wrong, wrong2, wrong3, mit), tokens)
	var spent *nl.BudgetSpent
	if !errors.As(err, &spent) || len(tr.Tries) != 2 || spent.Tokens < 250 {
		t.Errorf("tokens: %v after %d tries", err, len(tr.Tries))
	}
	_, tr, err = nl.Translate(context.Background(), mitQ, v, nil, model("small", mit), nl.Options{Base: b, Budget: nl.Budget{Time: time.Nanosecond}})
	if !errors.As(err, &spent) || len(tr.Tries) != 0 {
		t.Errorf("time: %v after %d tries", err, len(tr.Tries))
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "mit", Question: mitQ, Want: [][]string{{"app"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, model("small", wrong, wrong2, mit), tokens)
		})
	if rep.Failures[nl.ClassBudget] != 1 {
		t.Errorf("report failures %v", rep.Failures)
	}
	p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", mit, apache, apache), nl.Options{Base: b, Vote: &nl.Vote{}, Budget: nl.Budget{Tokens: 50}})
	if err != nil || p.Text != mit || p.Confidence.Drawn != 1 || len(tr.Votes) != 0 {
		t.Errorf("a vote past the budget: %q %+v %d votes %v, want the answer undisputed", p.Text, p.Confidence, len(tr.Votes), err)
	}
	tokens.Budget = nl.Budget{}
	if p, tr, err := nl.Translate(context.Background(), mitQ, v, nil, model("small", wrong, wrong2, mit), tokens); err != nil || p.Text != mit || len(tr.Tries) != 3 {
		t.Errorf("control: unbudgeted, %q after %d tries: %v", p.Text, len(tr.Tries), err)
	}
}

// finder finds a fixed answer for every name, recording what it was asked.
type finder struct{ asked []string }

func (g *finder) Ground(_ context.Context, name string) ([]nl.Candidate, error) {
	g.asked = append(g.asked, name)
	return []nl.Candidate{{Name: name, Values: []string{"MIT"}}}, nil
}

// find asks the Grounder about a name mid-loop and sends back what it found, and the prompt offers
// it only when there is a Grounder. control: without one, the prompt and grammar don't name find, and
// a model writing it anyway is told there's nothing to find with.
func TestFindAsksTheGrounder(t *testing.T) {
	v, b := guide.Deps(t)
	g := &finder{}
	m := model("small", "find mit license", mit)
	p, tr, err := nl.Translate(context.Background(), mitQ, v, g, m, nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != mit || len(tr.Steps) != 1 || tr.Steps[0].Action != nl.ActionFind {
		t.Fatalf("%q %+v %v", p.Text, tr.Steps, err)
	}
	if !strings.Contains(tr.Steps[0].Result, `mit license: "MIT"`) || len(g.asked) != 2 || g.asked[1] != "mit license" {
		t.Errorf("find sent back %q, the finder was asked %q", tr.Steps[0].Result, g.asked)
	}
	if !strings.Contains(m.asked[0].Prefix, "find NAME") || !strings.Contains(m.asked[0].Grammar, `"find "`) {
		t.Errorf("the prompt doesn't offer find")
	}
	blind := model("small", "find mit license", mit)
	_, tr, _ = nl.Translate(context.Background(), mitQ, v, nil, blind, nl.Options{Base: b, Actions: &nl.Actions{}})
	if strings.Contains(blind.asked[0].Prefix, "find NAME") || strings.Contains(blind.asked[0].Grammar, `"find "`) || len(tr.Steps) != 1 || !strings.Contains(tr.Steps[0].Result, "nothing to find") {
		t.Errorf("control: without a finder, steps %+v, tries %+v", tr.Steps, tr.Tries)
	}
}

// A refused try is classed by what went wrong: no rows is grounding, whatever repeats it; a check
// error is construct, an order-by one too though its Kind spells "answer" as the pack's refusal does;
// the pack's Check refusing is semantics; and a budget is budget. A case that answered the wrong rows
// is semantics.
func TestClassesSortWhatWentWrong(t *testing.T) {
	empty := &nl.EmptyAnswer{}
	answerKind := &datalog.CheckError{Kind: datalog.KindAnswer}
	for _, c := range []struct {
		try  nl.Try
		want string
	}{
		{nl.Try{}, ""},
		{nl.Try{Err: empty, Kind: nl.KindEmpty}, nl.ClassGrounding},
		{nl.Try{Err: fmt.Errorf("the same draft as before: %w", empty), Kind: nl.KindRepeat}, nl.ClassGrounding},
		{nl.Try{Err: &datalog.CheckError{Kind: datalog.KindUnknown}, Kind: "unknown"}, nl.ClassConstruct},
		{nl.Try{Err: answerKind, Kind: string(datalog.KindAnswer)}, nl.ClassConstruct},
		{nl.Try{Err: errors.New("not a list of packages"), Kind: nl.KindAnswer}, nl.ClassSemantics},
		{nl.Try{Err: errors.New("bad"), Kind: nl.KindParse}, nl.ClassConstruct},
		{nl.Try{Err: &datalog.BudgetExceeded{}, Kind: nl.KindEval}, nl.ClassBudget},
		{nl.Try{Err: errors.New("down"), Kind: nl.KindModel}, nl.ClassModel},
	} {
		if got := c.try.Class(); got != c.want {
			t.Errorf("%v (%s): class %q, want %q", c.try.Err, c.try.Kind, got, c.want)
		}
	}
	v, b := guide.Deps(t)
	rep := nl.Eval(context.Background(), []nl.Case{
		{Name: "wrong rows", Question: mitQ, Want: [][]string{{"app"}}},
		{Name: "right", Question: mitQ, Want: [][]string{{"app"}}},
	}, b, func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
		return nl.Translate(ctx, q, v, nil, model("small", apache, mit), nl.Options{Base: b})
	})
	if rep.Failures[nl.ClassSemantics] != 2 || len(rep.Failures) != 1 {
		t.Errorf("failures %v: each case answered Apache's rows", rep.Failures)
	}
}
