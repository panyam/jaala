package nl_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/panyam/jaala/frontends/nl"
)

// A later tier's steps count the tries before them across both tiers, and its Tier wins.
func TestThenJoinsATiersTraceAfterAnother(t *testing.T) {
	tr := nl.Trace{Tries: []nl.Try{{Model: "small"}, {Model: "small"}}, Steps: []nl.Step{{After: 1}}, Offered: []nl.Candidate{{Name: "a"}}}
	tr.Then(nl.Trace{Tries: []nl.Try{{Model: "big"}}, Steps: []nl.Step{{After: 0}}, Votes: []nl.Try{{}}, Tier: nl.TierFrontier, Offered: []nl.Candidate{{Name: "b"}}})
	if len(tr.Tries) != 3 || tr.Steps[0].After != 1 || tr.Steps[1].After != 2 || len(tr.Votes) != 1 || tr.Tier != nl.TierFrontier || len(tr.Offered) != 2 {
		t.Errorf("joined %+v", tr)
	}
}

// A tier with no cap of its own runs under Options.Budget, and one with a cap under that cap alone.
func TestATiersBudgetIsItsOwnWhenSet(t *testing.T) {
	base := nl.Budget{Tokens: 100}
	tiers := nl.Tiers{Local: nl.Budget{Calls: 2}}
	if got := tiers.For(nl.TierLocal, base); got != (nl.Budget{Calls: 2}) {
		t.Errorf("local %+v", got)
	}
	if got := tiers.For(nl.TierFrontier, base); got != base {
		t.Errorf("frontier %+v, want the base", got)
	}
}

// The hand-off draft is the refused try itself, so the frontier model is told what it was told; a
// disputed answer goes with its diagnosis as the error; a model that failed leaves nothing to start
// from.
func TestHandoffDraftIsTheClosestTry(t *testing.T) {
	refused := nl.Try{Model: "small", Text: `edge("zz", ?b) => ?b`, Kind: nl.KindEmpty, Err: errors.New("no rows")}
	tr := nl.Trace{Tries: []nl.Try{{Text: "x(", Kind: nl.KindParse, Err: errors.New("parse")}, refused}}
	if d, ok := nl.HandoffDraft(tr, nl.ErrNoProgram); !ok || d.Text != refused.Text || d.Err != refused.Err {
		t.Errorf("a plain failure hands off %+v %v, want the empty draft", d, ok)
	}
	disputed := &nl.Abstained{Reason: nl.AbstainLowAgreement, Draft: nl.Program{Text: "p", Model: "small"}, Diagnosis: "1 of 3 drafts agreed on its rows"}
	if d, ok := nl.HandoffDraft(nl.Trace{Tries: []nl.Try{{Text: "p"}}}, disputed); !ok || d.Text != "p" || d.Err == nil || d.Err.Error() != disputed.Diagnosis {
		t.Errorf("a disputed answer hands off %+v %v", d, ok)
	}
	failed := nl.Trace{Tries: []nl.Try{{Kind: nl.KindModel, Err: errors.New("down")}}}
	if _, ok := nl.HandoffDraft(failed, nl.ErrNoProgram); ok {
		t.Error("a model that failed handed off a draft")
	}
}

func TestATierPolicyMustBeOneOfThree(t *testing.T) {
	for _, p := range []string{nl.TierLocal, nl.TierLocalThenFrontier, nl.TierFrontier} {
		if err := (nl.Tiers{Policy: p}).Check(); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := (nl.Tiers{Policy: "both"}).Check(); err == nil {
		t.Error("both passed")
	}
}

// A case run in tiers splits its cost by tier, each tier's own tokens, calls, dollars and latency,
// and the summary counts which tier answered. control: a case without tiers has no split.
func TestARecordSplitsCostByTier(t *testing.T) {
	tr := nl.Trace{
		Tries: []nl.Try{
			{Model: "small", Tier: nl.TierLocal, TokensIn: 100, Calls: 1, Latency: 2 * time.Second, Kind: nl.KindEmpty, Err: errors.New("no rows")},
			{Model: "big", Tier: nl.TierFrontier, TokensIn: 1_000_000, TokensOut: 10, Calls: 1, Latency: 5 * time.Second},
		},
		Steps: []nl.Step{{Model: "small", Tier: nl.TierLocal, TokensIn: 50, Calls: 1, Latency: time.Second}},
		Tier:  nl.TierFrontier,
	}
	rep := scored(t, []nl.Case{
		{Name: "p", Question: "pass", Want: [][]string{{"a"}}},
		{Name: "plain", Question: "pass2", Want: [][]string{{"a"}}},
	}, map[string]nl.Trace{"pass": tr})
	prices := nl.Prices{"small": {}, "big": {In: 2}}
	got := rep.Cases[0].TierCost(prices)
	want := map[string]nl.Cost{
		nl.TierLocal:    {TokensIn: 150, Calls: 2, Seconds: 3},
		nl.TierFrontier: {TokensIn: 1_000_000, TokensOut: 10, Calls: 1, Seconds: 5, Dollars: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tier cost %+v, want %+v", got, want)
	}
	if c := rep.Cases[1].TierCost(prices); c != nil {
		t.Errorf("control: a case without tiers split its cost %+v", c)
	}
	s := rep.Summary(prices)
	if !reflect.DeepEqual(s.Tiers, want) || !reflect.DeepEqual(s.Answered, map[string]int{nl.TierFrontier: 1}) {
		t.Errorf("summary tiers %+v, answered %+v", s.Tiers, s.Answered)
	}
	rec := nl.NewRecord(rep, prices)
	if c := rec.Cases[0]; c.Tier != nl.TierFrontier || !reflect.DeepEqual(c.Tiers, want) || c.Tries[0].Tier != nl.TierLocal || c.Steps[0].Tier != nl.TierLocal {
		t.Errorf("case record %+v", c)
	}
}
