package translator_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
)

// doubtsLocal escalates every answer the local tier gives, as a host's domain check would one that
// runs and answers rows outside the module the question names.
func doubtsLocal(_ context.Context, r nl.Rung) nl.Score {
	if r.Tier == nl.TierLocal && r.Err == nil {
		return nl.Score{Verdict: nl.ScoreEscalate, Reason: "a row is outside the module"}
	}
	return nl.Score{Verdict: nl.ScoreAccept}
}

// A Scorer escalates a local answer that ran (#572): the frontier model starts from it and the
// Score's reason, and the trace keeps each tier's verdict. control: without a Scorer the local answer
// is taken, and under local alone the escalation abstains with the answer as its draft.
func TestAScorerEscalatesALocalAnswerItDoubts(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	local := `edge("a", ?b) => ?b`
	o := nl.Options{Base: b, Tiers: &nl.Tiers{Policy: nl.TierLocalThenFrontier}, Scorer: doubtsLocal}
	small, big := model("small", local), model("big", `edge("b", ?b) => ?b`)
	p, tr, err := translate(ctx, "q", v, nil, small, o, big)
	if err != nil || p.Model != "big" || tr.Tier != nl.TierFrontier {
		t.Fatalf("answer %q from %q in tier %q: %v", p.Text, p.Model, tr.Tier, err)
	}
	first := big.asked[0].Turns[0].Text
	if !strings.Contains(first, "A smaller model drafted this program for it first:\n"+local+"\nIt wasn't taken:\na row is outside the module") {
		t.Errorf("the frontier model's first turn:\n%s", first)
	}
	want := []nl.RungScore{{Tier: nl.TierLocal, Verdict: nl.ScoreEscalate, Reason: "a row is outside the module"}, {Tier: nl.TierFrontier, Verdict: nl.ScoreAccept}}
	if !reflect.DeepEqual(tr.Scores, want) {
		t.Errorf("scores %+v", tr.Scores)
	}

	o.Scorer = nil
	small, big = model("small", local), model("big", `edge("b", ?b) => ?b`)
	if p, tr, err := translate(ctx, "q", v, nil, small, o, big); err != nil || p.Model != "small" || len(big.asked) != 0 ||
		!reflect.DeepEqual(tr.Scores, []nl.RungScore{{Tier: nl.TierLocal, Verdict: nl.ScoreAccept}}) {
		t.Errorf("control: without a Scorer, %q from %q, scores %+v: %v", p.Text, p.Model, tr.Scores, err)
	}

	o.Scorer, o.Tiers = doubtsLocal, &nl.Tiers{Policy: nl.TierLocal}
	small, big = model("small", local), model("big", `edge("b", ?b) => ?b`)
	var ab *nl.Abstained
	if _, _, err := translate(ctx, "q", v, nil, small, o, big); !errors.As(err, &ab) || ab.Reason != nl.AbstainScorer ||
		ab.Draft.Text != local || ab.Diagnosis != "a row is outside the module" || len(big.asked) != 0 {
		t.Errorf("control: local alone, %v", err)
	}
}
