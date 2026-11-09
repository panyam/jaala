package translator_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/frontends/nlmodels/internal/modeltest"

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

// byDraw answers each prompt by its Draw, and a vote's draft only once all three of a vote's drafts
// have been asked, so only drafts asked at once are answered.
type byDraw struct {
	replies []string
	arrived chan struct{}
}

func (m *byDraw) Name() string { return "drawn" }
func (m *byDraw) Complete(_ context.Context, p modeltest.Prompt) (modeltest.Completion, error) {
	if p.Draw > 0 {
		m.arrived <- struct{}{}
		deadline := time.After(time.Second)
		for len(m.arrived) < cap(m.arrived) {
			select {
			case <-deadline:
				return modeltest.Completion{}, errors.New("drawn alone")
			case <-time.After(time.Millisecond):
			}
		}
	}
	return modeltest.Completion{Text: m.replies[p.Draw], TokensIn: 10, TokensOut: 5}, nil
}

// finder finds a fixed answer for every name, recording what it was asked.
type finder struct{ asked []string }

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
		p, tr, err := translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Vote: &nl.Vote{Drafts: c.drafts}})
		if err != nil || p.Text != c.want || p.Confidence.Agreed != c.agreed || p.Confidence.Drawn != c.voters || !p.Confidence.Ran {
			t.Errorf("%s: %q %+v %v, want %q agreed %d of %d", c.name, p.Text, p.Confidence, err, c.want, c.agreed, c.voters)
			continue
		}
		if len(tr.Tries) != 1 || len(tr.Votes) != c.drafts-1 || len(m.asked) != c.drafts {
			t.Errorf("%s: %d tries, %d votes, %d asked", c.name, len(tr.Tries), len(tr.Votes), len(m.asked))
		}
		for i, d := range tr.Votes {
			if d.Calls != 1 {
				t.Errorf("%s: vote %d counts %d calls, want its one draw", c.name, i, d.Calls)
			}
		}
		for i, q := range m.asked {
			if q.Draw != i || q.Grammar != m.asked[0].Grammar {
				t.Errorf("%s: ask %d had Draw %d", c.name, i, q.Draw)
			}
			if !slices.Equal(q.Turns, m.asked[0].Turns) {
				t.Errorf("%s: draw %d was asked %+v, not what the answer was asked", c.name, i, q.Turns)
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
		p, tr, err := translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Vote: &nl.Vote{Drafts: 4, Parallel: parallel}})
		agreed := p.Text == apache && p.Confidence.Agreed == 3
		if err != nil || len(tr.Votes) != 3 || agreed != parallel {
			t.Errorf("parallel %v: %q %+v %v", parallel, p.Text, p.Confidence, err)
		}
	}
}

// Without a Base to run them on, drafts agree when they are the same program.
func TestWithoutABaseDraftsAgreeOnTheirProgram(t *testing.T) {
	v, _ := guide.Deps(t)
	for _, c := range []struct {
		replies []string
		agreed  int
	}{{[]string{mit, mit}, 2}, {[]string{mit, mitToo}, 1}} {
		p, _, err := translate(context.Background(), mitQ, v, nil, model("small", c.replies...), nl.Options{Vote: &nl.Vote{Drafts: 2}})
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

	p, tr, err := translate(context.Background(), mitQ, v, nil, model("small", disputed...), o, model("big", apache, apache, apache))
	if err != nil || p.Model != "big" || p.Text != apache || p.Confidence.Agreement() != 1 {
		t.Errorf("escalated: %s %q %+v %v", p.Model, p.Text, p.Confidence, err)
	}
	if len(tr.Escalations) != 1 || tr.Escalations[0].Model != "small" || tr.Escalations[0].Confidence.Agreed != 1 {
		t.Errorf("escalations %+v", tr.Escalations)
	}

	if p, tr, err := translate(context.Background(), mitQ, v, nil, model("small", disputed...), o, model("big", mit, apache, broken)); err != nil || p.Model != "big" || len(tr.Escalations) != 1 {
		t.Errorf("the fallback's own disputed answer: %s %+v %v", p.Model, tr.Escalations, err)
	}

	if p, _, err := translate(context.Background(), mitQ, v, nil, model("small", disputed...), o, model("big", "!fail")); err != nil || p.Model != "small" || p.Text != mit {
		t.Errorf("the fallback failing: %s %q %v, want small's answer back", p.Model, p.Text, err)
	}

	rep := nl.Eval(context.Background(), []nl.Case{{Name: "apache", Question: mitQ, Want: [][]string{{"api"}, {"log"}, {"util"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return translate(ctx, q, v, nil, model("small", disputed...), o, model("big", apache, apache, apache))
		})
	if rep.Votes != 4 || rep.Escalations != 1 || !strings.Contains(rep.String(), "4 votes, 1 escalated") {
		t.Errorf("report: %d votes, %d escalated\n%s", rep.Votes, rep.Escalations, rep.String())
	}

	o.Escalate = 0
	if p, tr, err := translate(context.Background(), mitQ, v, nil, model("small", disputed...), o, model("big", apache, apache, apache)); err != nil || p.Model != "small" || len(tr.Escalations) != 0 {
		t.Errorf("control: without Escalate, %s %v", p.Model, err)
	}
}

// A Budget stops Translate before the call that would pass it, with a BudgetSpent the report classes
// as budget; Time runs out the same way. control: unbudgeted, the model gets every try.
func TestABudgetStopsTranslate(t *testing.T) {
	v, b := guide.Deps(t)
	wrong, wrong2, wrong3 := `pkg(?p, "mit") => ?p`, `pkg(?q, "mit") => ?q`, `pkg(?r, "mit") => ?r`
	tokens := nl.Options{Base: b, MaxTries: 5, Budget: nl.Budget{Tokens: 250}}
	_, tr, err := translate(context.Background(), mitQ, v, nil, model("small", wrong, wrong2, wrong3, mit), tokens)
	var spent *nl.BudgetSpent
	if !errors.As(err, &spent) || len(tr.Tries) != 2 || spent.Tokens < 250 {
		t.Errorf("tokens: %v after %d tries", err, len(tr.Tries))
	}
	_, tr, err = translate(context.Background(), mitQ, v, nil, model("small", mit), nl.Options{Base: b, Budget: nl.Budget{Time: time.Nanosecond}})
	if !errors.As(err, &spent) || len(tr.Tries) != 0 {
		t.Errorf("time: %v after %d tries", err, len(tr.Tries))
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "mit", Question: mitQ, Want: [][]string{{"app"}}}}, b,
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return translate(ctx, q, v, nil, model("small", wrong, wrong2, mit), tokens)
		})
	if rep.Failures[nl.ClassBudget] != 1 {
		t.Errorf("report failures %v", rep.Failures)
	}
	p, tr, err := translate(context.Background(), mitQ, v, nil, model("small", mit, apache, apache), nl.Options{Base: b, Vote: &nl.Vote{}, Budget: nl.Budget{Tokens: 50}})
	if err != nil || p.Text != mit || p.Confidence.Drawn != 1 || len(tr.Votes) != 0 {
		t.Errorf("a vote past the budget: %q %+v %d votes %v, want the answer undisputed", p.Text, p.Confidence, len(tr.Votes), err)
	}
	m := model("small", mit, apache, apache) // 122 tokens, then 225 for the first draw
	p, tr, err = translate(context.Background(), mitQ, v, nil, m, nl.Options{Base: b, Vote: &nl.Vote{}, Budget: nl.Budget{Tokens: 300}})
	if err != nil || p.Text != mit || p.Confidence.Drawn != 2 || len(tr.Votes) != 1 || len(m.asked) != 2 {
		t.Errorf("a vote the budget runs out in: %q %+v %d votes, asked %d times, %v", p.Text, p.Confidence, len(tr.Votes), len(m.asked), err)
	}
	tokens.Budget = nl.Budget{}
	if p, tr, err := translate(context.Background(), mitQ, v, nil, model("small", wrong, wrong2, mit), tokens); err != nil || p.Text != mit || len(tr.Tries) != 3 {
		t.Errorf("control: unbudgeted, %q after %d tries: %v", p.Text, len(tr.Tries), err)
	}
}

// find asks the Grounder about a name mid-loop and sends back what it found, and it is offered as a
// tool only when there is a Grounder. control: without one, find isn't offered, and a model writing
// it anyway has written an answer that doesn't parse.
func TestFindAsksTheGrounder(t *testing.T) {
	v, b := guide.Deps(t)
	g := &finder{}
	m := model("small", "find mit license", mit)
	p, tr, err := translate(context.Background(), mitQ, v, g, m, nl.Options{Base: b, Actions: &nl.Actions{}})
	if err != nil || p.Text != mit || len(tr.Steps) != 1 || tr.Steps[0].Action != nl.ActionFind {
		t.Fatalf("%q %+v %v", p.Text, tr.Steps, err)
	}
	if !strings.Contains(tr.Steps[0].Result, `mit license: "MIT"`) || len(g.asked) != 2 || g.asked[1] != "mit license" {
		t.Errorf("find sent back %q, the finder was asked %q", tr.Steps[0].Result, g.asked)
	}
	if !slices.Contains(m.offered[0], nl.ActionFind) {
		t.Errorf("find wasn't offered: %v", m.offered[0])
	}
	// the trace keeps what was offered: the question's grounding, then the find's
	if want := []nl.Candidate{{Name: mitQ, Values: []string{"MIT"}}, {Name: "mit license", Values: []string{"MIT"}}}; !reflect.DeepEqual(tr.Offered, want) {
		t.Errorf("offered %+v, want %+v", tr.Offered, want)
	}
	blind := model("small", "find mit license", mit)
	_, tr, _ = translate(context.Background(), mitQ, v, nil, blind, nl.Options{Base: b, Actions: &nl.Actions{}})
	if slices.Contains(blind.offered[0], nl.ActionFind) || len(tr.Steps) != 0 || tr.Tries[0].Kind != nl.KindParse {
		t.Errorf("control: without a finder, offered %v, steps %+v, tries %+v", blind.offered[0], tr.Steps, tr.Tries)
	}
	if tr.Offered != nil {
		t.Errorf("control: without a finder, the trace says %+v was offered", tr.Offered)
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
		return translate(ctx, q, v, nil, model("small", apache, mit), nl.Options{Base: b})
	})
	if rep.Failures[nl.ClassSemantics] != 2 || len(rep.Failures) != 1 {
		t.Errorf("failures %v: each case answered Apache's rows", rep.Failures)
	}
}

func (g *finder) Ground(_ context.Context, name string) ([]nl.Candidate, error) {
	g.asked = append(g.asked, name)
	return []nl.Candidate{{Name: name, Values: []string{"MIT"}}}, nil
}
