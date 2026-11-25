package nl_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
)

// DefaultScore accepts an answer, escalates a tier that didn't answer with a one-word reason, and
// accepts whatever the last tier gives (#572).
func TestDefaultScoreEscalatesOnlyATierThatDidntAnswer(t *testing.T) {
	ctx := context.Background()
	noProgram := fmt.Errorf("nl: after 3 tries: %w", nl.ErrNoProgram)
	for _, c := range []struct {
		name string
		r    nl.Rung
		want nl.Score
	}{
		{"answer", nl.Rung{}, nl.Score{Verdict: nl.ScoreAccept}},
		{"abstained", nl.Rung{Err: &nl.Abstained{Reason: nl.AbstainRepeat}}, nl.Score{Verdict: nl.ScoreEscalate, Reason: nl.AbstainRepeat}},
		{"budget", nl.Rung{Err: &nl.BudgetSpent{}}, nl.Score{Verdict: nl.ScoreEscalate, Reason: nl.AbstainBudget}},
		{"failed", nl.Rung{Err: noProgram}, nl.Score{Verdict: nl.ScoreEscalate, Reason: nl.AbstainNoAnswer}},
		{"last", nl.Rung{Err: noProgram, Last: true}, nl.Score{Verdict: nl.ScoreAccept}},
	} {
		if got := nl.DefaultScore(ctx, c.r); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Settle turns a Score into what the question answers (#572): an escalated answer moves on as an
// abstention holding it, a ScoreAbstain on an answer abstains with it as the draft, and one on a
// failure keeps the closest refused draft with the Score's reason. Each verdict lands in Scores.
func TestSettleActsOnTheScore(t *testing.T) {
	ctx := context.Background()
	answer := nl.Program{Text: `edge("a", ?b) => ?b`, Model: "small"}
	say := func(verdict string) nl.Options {
		return nl.Options{Scorer: func(context.Context, nl.Rung) nl.Score { return nl.Score{Verdict: verdict, Reason: "out of scope"} }}
	}
	var ab *nl.Abstained

	p, tr, err, next := nl.Settle(ctx, say(nl.ScoreEscalate), nl.TierLocal, answer, nl.Trace{}, nil, false)
	if !next || p.Text != "" || !errors.As(err, &ab) || ab.Reason != nl.AbstainScorer || ab.Draft.Text != answer.Text || ab.Diagnosis != "out of scope" {
		t.Errorf("escalate: next %v, %q: %v", next, p.Text, err)
	}
	if len(tr.Scores) != 1 || tr.Scores[0] != (nl.RungScore{Tier: nl.TierLocal, Verdict: nl.ScoreEscalate, Reason: "out of scope"}) {
		t.Errorf("escalate: scores %+v", tr.Scores)
	}
	if _, _, err, next := nl.Settle(ctx, say(nl.ScoreEscalate), nl.TierFrontier, answer, nl.Trace{}, nil, true); next || !errors.As(err, &ab) || ab.Draft.Text != answer.Text {
		t.Errorf("escalate on the last tier: next %v: %v", next, err)
	}
	if _, _, err, next := nl.Settle(ctx, say(nl.ScoreAbstain), nl.TierLocal, answer, nl.Trace{}, nil, false); next || !errors.As(err, &ab) || ab.Reason != nl.AbstainScorer || ab.Draft.Text != answer.Text {
		t.Errorf("abstain: next %v: %v", next, err)
	}

	refused := nl.Trace{Tries: []nl.Try{{Model: "small", Text: `edge("zz", ?b) => ?b`, Kind: nl.KindEmpty, Err: errors.New("the program answered no rows")}}}
	failed := fmt.Errorf("nl: after 1 try: %w", nl.ErrNoProgram)
	if _, _, err, _ := nl.Settle(ctx, say(nl.ScoreAbstain), nl.TierLocal, nl.Program{}, refused, failed, false); !errors.As(err, &ab) ||
		ab.Reason != nl.AbstainScorer || ab.Draft.Text != refused.Tries[0].Text || ab.Diagnosis != "out of scope" || !errors.Is(err, nl.ErrNoProgram) {
		t.Errorf("abstain on a failure: %v", err)
	}

	if p, tr, err, next := nl.Settle(ctx, say(""), nl.TierLocal, answer, nl.Trace{}, nil, false); next || err != nil || p.Text != answer.Text || tr.Scores[0].Verdict != nl.ScoreAccept {
		t.Errorf("no verdict accepts: next %v, %q, %+v: %v", next, p.Text, tr.Scores, err)
	}
}

// Report counts the escalations by reason, and a record keeps each case's verdicts.
func TestEscalationsAreCountedByReason(t *testing.T) {
	v := vocabulary(t)
	b := graphBase(t, v)
	scores := []nl.RungScore{{Tier: nl.TierLocal, Verdict: nl.ScoreEscalate, Reason: "out of scope"}, {Tier: nl.TierFrontier, Verdict: nl.ScoreAccept}}
	q, err := datalog.Parse(`edge("a", ?b) => ?b`)
	if err != nil {
		t.Fatal(err)
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "a", Question: "a"}, {Name: "b", Question: "b"}}, b,
		func(_ context.Context, question string) (nl.Program, nl.Trace, error) {
			if question == "a" {
				return nl.Program{Query: q}, nl.Trace{Scores: scores}, nil
			}
			return nl.Program{}, nl.Trace{Scores: scores[:1]}, nl.ErrNoProgram
		})
	if len(rep.Escalated) != 1 || rep.Escalated["out of scope"] != 2 {
		t.Errorf("escalated %v", rep.Escalated)
	}
	if rec := nl.NewRecord(rep, nil); len(rec.Cases) != 2 || len(rec.Cases[0].Scores) != 2 || rec.Cases[0].Scores[0] != scores[0] {
		t.Errorf("record %+v", rec.Cases)
	}
}
