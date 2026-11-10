package translator

import (
	"context"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// A Reasoning role asks for ReasoningMaxTokens and sends no grammar, since its hidden reasoning
// counts against the cap and a grammar would hold its reply to a program from the first token; a
// MaxTokens set on the role still wins. control: a plain role on the same provider asks for 512
// and is held to the grammar.
func TestAReasoningRoleGetsRoomToReason(t *testing.T) {
	v, b := guide.Deps(t)
	for _, c := range []struct {
		name   string
		role   Role
		tokens int
		guided bool
	}{
		{"reasoning", Role{Reasoning: true}, ReasoningMaxTokens, false},
		{"reasoning with a cap", Role{Reasoning: true, MaxTokens: 900}, 900, false},
		{"control: plain", Role{}, MaxTokens, true},
	} {
		rec := &recordingProvider{Provider: scripted("big", map[string][]string{mitQ: {mit}})}
		c.role.Provider = rec
		tr, err := New(v, nil, Config{Model: c.role, Base: b})
		if err != nil {
			t.Fatal(err)
		}
		if p, _, err := tr.Ask(context.Background(), mitQ); err != nil || p.Text != mit {
			t.Fatalf("%s: %q %v", c.name, p.Text, err)
		}
		req := rec.reqs[0]
		if req.MaxTokens != c.tokens || (req.Guided != nil) != c.guided {
			t.Errorf("%s: asked for %d tokens, guided %v; want %d, %v", c.name, req.MaxTokens, req.Guided != nil, c.tokens, c.guided)
		}
	}
}

// A call that writes as many tokens as the cap marks its Try or Step Truncated. control: under a higher cap nothing is marked.
func TestACallReachingItsCapIsMarkedTruncated(t *testing.T) {
	v, b := guide.Deps(t)
	ask := func(limit int) nl.Trace {
		// calls: 1 the look (out 1), 2 the answer (out 2), 3 its guided rewrite (out 3, no event)
		m := &metered{script: scripted("big", map[string][]string{mitQ: {"look pkg", mit}}), in: 10, out: 1}
		tr, err := New(v, nil, Config{Model: Role{Provider: m, MaxTokens: limit}, Base: b, Actions: &nl.Actions{}})
		if err != nil {
			t.Fatal(err)
		}
		_, trace, err := tr.Ask(context.Background(), mitQ)
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}
	tr := ask(2)
	if len(tr.Steps) != 1 || tr.Steps[0].Truncated || len(tr.Tries) != 1 || !tr.Tries[0].Truncated {
		t.Errorf("cap 2: step %+v, tries %+v; want the step whole and the try truncated", tr.Steps, tr.Tries)
	}
	if tr := ask(100); tr.Steps[0].Truncated || tr.Tries[0].Truncated {
		t.Errorf("control: cap 100 marked %+v %+v", tr.Steps, tr.Tries)
	}
}

// A step and a vote's draw are marked the same way: under a cap of 1 the look's call reaches it,
// and so does a draw. control: under a higher cap neither is.
func TestAStepAndADrawReachingTheCapAreMarked(t *testing.T) {
	v, b := guide.Deps(t)
	m := &metered{script: scripted("big", map[string][]string{mitQ: {"look pkg", mit}}), in: 10, out: 1}
	tr, err := New(v, nil, Config{Model: Role{Provider: m, MaxTokens: 1}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, trace, err := tr.Ask(context.Background(), mitQ); err != nil || !trace.Steps[0].Truncated {
		t.Errorf("cap 1: step %+v, %v", trace.Steps, err)
	}
	for _, c := range []struct {
		limit int
		want  bool
	}{{2, true}, {100, false}} {
		m := &metered{script: scripted("big", map[string][]string{mitQ: {mit, mit}}), in: 10, out: 1}
		tr, err := New(v, nil, Config{Model: Role{Provider: m, MaxTokens: c.limit}, Base: b, Vote: &nl.Vote{Drafts: 2}})
		if err != nil {
			t.Fatal(err)
		}
		_, trace, err := tr.Ask(context.Background(), mitQ)
		if err != nil || len(trace.Votes) != 1 || trace.Votes[0].Truncated != c.want {
			t.Errorf("cap %d: votes %+v, want truncated %v (%v)", c.limit, trace.Votes, c.want, err)
		}
	}
}
