package translator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
)

// empties are three drafts that check and answer nothing, so the local tier runs out of tries.
var empties = []string{`edge("zz", ?b) => ?b`, `edge("yy", ?b) => ?b`, `edge("xx", ?b) => ?b`}

// Under local-then-frontier a local abstention goes to the frontier model, which starts from the
// local tier's closest draft and why it wasn't taken, and every try says its tier. control: under
// local alone the same abstention is the answer, and the frontier model is never asked.
func TestTheFrontierTierStartsFromTheLocalTiersClosestDraft(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	o := nl.Options{Base: b, Abstain: &nl.Abstain{NoAnswer: true}, Tiers: &nl.Tiers{Policy: nl.TierLocalThenFrontier}}
	small, big := model("small", empties...), model("big", `edge("a", ?b) => ?b`)
	p, tr, err := translate(ctx, "q", v, nil, small, o, big)
	if err != nil || p.Model != "big" || tr.Tier != nl.TierFrontier {
		t.Fatalf("answer %q from %q in tier %q: %v", p.Text, p.Model, tr.Tier, err)
	}
	var tiers []string
	for _, try := range tr.Tries {
		tiers = append(tiers, try.Model+":"+try.Tier)
	}
	if got := strings.Join(tiers, " "); got != "small:local small:local small:local big:frontier" {
		t.Errorf("tries %s", got)
	}
	first := big.asked[0].Turns[0].Text
	if !strings.HasPrefix(first, "q\n\nA smaller model drafted this program for it first:\n"+empties[2]+"\nIt wasn't taken:\n") {
		t.Errorf("the frontier model's first turn:\n%s", first)
	}
	if strings.Contains(small.asked[0].Turns[0].Text, "smaller model") {
		t.Error("control: the local model was told of a hand-off")
	}

	o.Tiers = &nl.Tiers{Policy: nl.TierLocal}
	small, big = model("small", empties...), model("big", `edge("a", ?b) => ?b`)
	var ab *nl.Abstained
	if _, tr, err := translate(ctx, "q", v, nil, small, o, big); !errors.As(err, &ab) || len(big.asked) != 0 || tr.Tier != "" {
		t.Errorf("control: local alone answered %v, asked the frontier %d times, tier %q", err, len(big.asked), tr.Tier)
	}
}

// A local answer ends the question in the local tier, and frontier alone never asks the local model.
func TestEachPolicyAsksItsOwnTiers(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	for _, c := range []struct {
		policy, tier, model string
		asked               [2]int // small's and big's
	}{
		{nl.TierLocalThenFrontier, nl.TierLocal, "small", [2]int{1, 0}},
		{nl.TierLocal, nl.TierLocal, "small", [2]int{1, 0}},
		{nl.TierFrontier, nl.TierFrontier, "big", [2]int{0, 1}},
	} {
		small, big := model("small", `edge("a", ?b) => ?b`), model("big", `edge("a", ?b) => ?b`)
		p, tr, err := translate(ctx, "q", v, nil, small, nl.Options{Base: b, Tiers: &nl.Tiers{Policy: c.policy}}, big)
		if err != nil || p.Model != c.model || tr.Tier != c.tier || [2]int{len(small.asked), len(big.asked)} != c.asked {
			t.Errorf("%s: %q from %q in %q, asked %d and %d: %v", c.policy, p.Text, p.Model, tr.Tier, len(small.asked), len(big.asked), err)
		}
	}
}

// Each tier runs under its own cap: a local tier capped at one call hands off after one draft, and
// the frontier tier, uncapped, still answers. control: uncapped, the local tier takes all its tries.
func TestEachTierHasItsOwnCap(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	ask := func(local nl.Budget) (nl.Program, nl.Trace, *scripted, error) {
		small, big := model("small", empties...), model("big", `edge("a", ?b) => ?b`)
		o := nl.Options{Base: b, Tiers: &nl.Tiers{Policy: nl.TierLocalThenFrontier, Local: local}}
		p, tr, err := translate(ctx, "q", v, nil, small, o, big)
		return p, tr, small, err
	}
	p, tr, small, err := ask(nl.Budget{Calls: 1})
	if err != nil || p.Model != "big" || len(small.asked) != 1 || tr.Tries[0].Tier != nl.TierLocal {
		t.Errorf("capped: %q from %q, local asked %d times: %v", p.Text, p.Model, len(small.asked), err)
	}
	if _, _, small, err := ask(nl.Budget{}); err != nil || len(small.asked) != 3 {
		t.Errorf("control: uncapped, local asked %d times: %v", len(small.asked), err)
	}
}

// A policy the translator doesn't know is refused, and so is one needing a frontier model it lacks.
func TestATierPolicyIsChecked(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	if _, _, err := translate(ctx, "q", v, nil, model("small", empties...), nl.Options{Base: b, Tiers: &nl.Tiers{Policy: "both"}}); err == nil || !strings.Contains(err.Error(), `tier policy "both"`) {
		t.Errorf("unknown policy: %v", err)
	}
	for _, pol := range []string{nl.TierFrontier, nl.TierLocalThenFrontier} {
		if _, _, err := translate(ctx, "q", v, nil, model("small", empties...), nl.Options{Base: b, Tiers: &nl.Tiers{Policy: pol}}); err == nil || !strings.Contains(err.Error(), "needs a Config.Fallback") {
			t.Errorf("%s with no frontier model: %v", pol, err)
		}
	}
}

// Prompts.Handoff replaces the hand-off's words, word for word.
func TestAPackReplacesTheHandoff(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	small, big := model("small", empties...), model("big", `edge("a", ?b) => ?b`)
	o := nl.Options{Base: b, Pack: with(&nl.Pack{}, hostPrompts()), Tiers: &nl.Tiers{Policy: nl.TierLocalThenFrontier}}
	if _, _, err := translate(ctx, "q", v, nil, small, o, big); err != nil {
		t.Fatal(err)
	}
	if msg := want(big.asked[0].Turns[0].Text, "q\n\nH-HANDOFF program|"+empties[2]+"|"); msg != "" {
		t.Error(msg)
	}
}
