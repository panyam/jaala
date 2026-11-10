package translator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
)

const decline = "none: nothing here holds who maintains a package"

// A model that declines ends its turn: the fallback is asked next, and when nothing answers the
// translator abstains with the model's words. Under local-then-frontier the frontier model starts
// from the question alone, since a decline is no draft to hand on. control: the fallback answering
// is an answer, not an abstention.
func TestADeclineMovesOnAndThenAbstains(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	o := nl.Options{Base: b, Abstain: &nl.Abstain{Declined: true}}
	small, big := model("small", decline), model("big", "none: no maintainer relation")
	_, tr, err := translate(ctx, "q", v, nil, small, o, big)
	var ab *nl.Abstained
	if !errors.As(err, &ab) || ab.Reason != nl.AbstainDeclined || ab.Diagnosis != "no maintainer relation" {
		t.Fatalf("both declined: %v", err)
	}
	if len(tr.Tries) != 2 || tr.Tries[0].Kind != nl.KindDeclined || tr.Tries[1].Kind != nl.KindDeclined || len(big.asked) != 1 {
		t.Errorf("tries %+v, fallback asked %d times", tr.Tries, len(big.asked))
	}

	small, big = model("small", decline), model("big", `edge("a", ?b) => ?b`)
	if p, _, err := translate(ctx, "q", v, nil, small, o, big); err != nil || p.Model != "big" {
		t.Errorf("control: the fallback's answer %q from %q: %v", p.Text, p.Model, err)
	}

	tiered := o
	tiered.Tiers = &nl.Tiers{Policy: nl.TierLocalThenFrontier}
	small, big = model("small", decline), model("big", `edge("a", ?b) => ?b`)
	if p, _, err := translate(ctx, "q", v, nil, small, tiered, big); err != nil || p.Model != "big" {
		t.Fatalf("tiered: %q from %q: %v", p.Text, p.Model, err)
	}
	if first := big.asked[0].Turns[0].Text; first != "q" {
		t.Errorf("the frontier model was handed something after a decline:\n%s", first)
	}
}

// Prompts.Decline replaces jaala's line word for word. control: jaala's own line names "none:".
func TestAPackReplacesTheDeclineLine(t *testing.T) {
	ctx := context.Background()
	v := vocabulary(t)
	b := graphBase(t, v)
	for _, own := range []bool{true, false} {
		pack := &nl.Pack{}
		if own {
			pack = with(pack, hostPrompts())
		}
		m := model("small", `edge("a", ?b) => ?b`)
		if _, _, err := translate(ctx, "q", v, nil, m, nl.Options{Base: b, Pack: pack, Abstain: &nl.Abstain{Declined: true}}); err != nil {
			t.Fatal(err)
		}
		prefix := m.asked[0].Prefix
		if strings.Contains(prefix, "H-DECLINE program\n") != own || strings.Contains(prefix, "reply none:") == own {
			t.Errorf("own prompts %v: prefix\n%s", own, prefix)
		}
	}
}
