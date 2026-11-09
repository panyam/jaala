package translator

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// With DraftFirst, a one-shot draft that passes is the answer, and the turn with actions never
// starts (#521): no request offers a tool. control: without it, the first request offers them.
func TestADraftThatPassesSkipsTheActions(t *testing.T) {
	v, b := guide.Deps(t)
	for _, first := range []bool{true, false} {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {mit}})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{DraftFirst: first}})
		if err != nil {
			t.Fatal(err)
		}
		p, trace, err := tr.Ask(context.Background(), mitQ)
		if err != nil || p.Text != mit || len(trace.Tries) != 1 {
			t.Fatalf("first %v: Ask = %+v, %v; tries %+v", first, p, err, trace.Tries)
		}
		if offered := len(rec.reqs[0].Tools) > 0; offered == first {
			t.Errorf("first %v: the first request offered tools: %v", first, offered)
		}
	}
}

// A refused one-shot draft starts the turn with actions (#521): the turn's first message carries it
// and its refusal, and the model sending it again is a repeat, which ends the turn after one
// re-ask, rather than a fresh empty answer. x05's model sent its refused draft five more times.
// control: without DraftFirst, the same replies judge that draft empty, as new.
func TestARefusedDraftStartsTheTurnWithActions(t *testing.T) {
	v, b := guide.Deps(t)
	nope := `pkg(?p, "nope") => ?p`
	d := nl.DefaultPrompts()
	head, _, _ := strings.Cut(d.DraftFirst, "%")
	rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {nope, nope, mit}})}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{DraftFirst: true}})
	if err != nil {
		t.Fatal(err)
	}
	p, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil || p.Text != mit {
		t.Fatalf("Ask = %+v, %v; tries %+v", p, err, trace.Tries)
	}
	if got := strings.Join(tryKinds(trace), " "); got != "empty repeat " {
		t.Errorf("kinds %q, want the draft empty, then its resend a repeat, then the answer", got)
	}
	if !trace.Tries[1].Reasked {
		t.Errorf("the resend wasn't asked again: %+v", trace.Tries[1])
	}
	if len(rec.reqs) < 2 || len(rec.reqs[1].Tools) == 0 {
		t.Fatalf("the second request offered no tools")
	}
	ask := rec.reqs[1].Messages[len(rec.reqs[1].Messages)-1].Text
	for _, m := range rec.reqs[1].Messages {
		if strings.Contains(m.Text, head) {
			ask = m.Text
		}
	}
	if !strings.Contains(ask, head) || !strings.Contains(ask, nope) {
		t.Errorf("the turn's first message %q leaves out the draft", ask)
	}
	ctl := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {nope, mit}})}
	plain, _ := New(v, nil, Config{Model: Role{Provider: ctl}, Base: b, Actions: &nl.Actions{}})
	if _, trace, _ := plain.Ask(context.Background(), mitQ); strings.Join(tryKinds(trace), " ") != "empty " {
		t.Errorf("control: without DraftFirst, kinds %v", tryKinds(trace))
	}
}

func tryKinds(tr nl.Trace) []string {
	var out []string
	for _, t := range tr.Tries {
		out = append(out, t.Kind)
	}
	return out
}
