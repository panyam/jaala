package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// A draft's second try again ends the actions with TriedTwice (#571): on Declaire's x04 and x07 a
// model tried a refused draft three and four times and never changed it. The draft a turn starts
// from counts as tried. control: two drafts each tried again once leave the actions open.
func TestASecondTryAgainOfOneDraftEndsTheActions(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 6}})
	nope := `pkg(?p, "nope") => ?p`
	c := s.NewConversation(nil, true)
	c.Act(ctx, nl.ActionTry, nope)
	if got := c.Act(ctx, nl.ActionTry, nope); strings.Contains(got, d.TriedTwice) || c.Steps() != 1 {
		t.Errorf("the first try again answered %q, steps %d", got, c.Steps())
	}
	if got := c.Act(ctx, nl.ActionTry, nope); !strings.HasPrefix(got, d.TriedAgain) || !strings.HasSuffix(got, "\n\n"+d.TriedTwice) || c.Steps() != 6 {
		t.Errorf("the second try again answered %q, steps %d", got, c.Steps())
	}

	draft, _, _ := s.Draw(ctx, reply(nope))
	from := s.NewConversation(nil, true)
	from.From(draft)
	from.Act(ctx, nl.ActionTry, nope)
	if got := from.Act(ctx, nl.ActionTry, nope); !strings.HasSuffix(got, d.TriedTwice) || from.Steps() != 6 {
		t.Errorf("from a refused draft, the second try again answered %q, steps %d", got, from.Steps())
	}

	other := `pkg(?p, "none") => ?p`
	ctl := s.NewConversation(nil, true)
	for _, draft := range []string{nope, other, nope, other} {
		if got := ctl.Act(ctx, nl.ActionTry, draft); strings.Contains(got, d.TriedTwice) {
			t.Errorf("control: %s answered %q", draft, got)
		}
	}
	if ctl.Steps() != 2 {
		t.Errorf("control: steps %d", ctl.Steps())
	}
}
