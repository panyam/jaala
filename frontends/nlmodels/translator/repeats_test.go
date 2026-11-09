package translator

import (
	"context"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// A model cycling through actions it took already costs no step for each repeat (#517), as a tool call
// or as its reply: x04's model cycled try, look, try, find, and its turn ended on chakra's step cap
// with nothing new run. The cycle is three actions long, so neither a repeat nor an alternation calls
// in the stuck detector; the third repeat ends the actions and the next reply is the answer. Without
// the free refusals, the six actions and the answer are seven calls against the Runner's cap of six.
func TestARepeatedActionCostsNoStep(t *testing.T) {
	v, b := guide.Deps(t)
	nope := "try\n" + `pkg(?p, "nope") => ?p`
	cycle := []string{nope, "look pkg", "values pkg.license", nope, "look pkg", "values pkg.license", mit}
	d := nl.DefaultPrompts()
	for _, text := range []bool{false, true} {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: cycle})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: text}, Base: b, Actions: &nl.Actions{Steps: 4},
			Options: func(o *nl.Options) { o.MaxTries = 1 }})
		if err != nil {
			t.Fatal(err)
		}
		p, trace, err := tr.Ask(context.Background(), mitQ)
		if err != nil || p.Text != mit || p.Confidence.Steps != 4 {
			t.Fatalf("text %v: Ask = %+v, %v; tries %+v", text, p, err, trace.Tries)
		}
		if len(trace.Steps) != 6 {
			t.Fatalf("text %v: %d steps", text, len(trace.Steps))
		}
		for i, req := range rec.reqs {
			for _, m := range req.Messages {
				if m.Role == agent.RoleTool && strings.HasPrefix(m.Text, "tool call not permitted") {
					t.Errorf("text %v: request %d: a repeat read as a denial: %q", text, i, m.Text)
				}
			}
		}
		if !text && !toolSaid(rec.reqs, d.TriedAgain) {
			t.Errorf("no tool result carried the repeat's answer")
		}
		for i, prefix := range []string{d.TriedAgain, d.AskedAgain, d.AskedAgain} {
			if r := trace.Steps[3+i].Result; !strings.Contains(r, prefix) {
				t.Errorf("text %v: repeat %d sent back %q", text, i+1, r)
			}
		}
		if r := trace.Steps[5].Result; !strings.HasSuffix(r, d.LastLook) {
			t.Errorf("text %v: the last repeat didn't end the actions: %q", text, r)
		}
		for i, req := range rec.reqs {
			for _, m := range req.Messages {
				if m.Role == agent.RoleSystem {
					t.Errorf("text %v: request %d has a note %q", text, i, m.Text)
				}
			}
		}
	}
}

// capped is a recordingProvider whose capabilities are caps.
type capped struct {
	*recordingProvider
	caps agent.Capabilities
}

func (c capped) ProviderCapabilities() agent.Capabilities { return c.caps }

// A Role's penalties go with every request, but only the kinds the provider takes (#517): llama.cpp
// all four, Ollama frequency and presence, and a provider listing none, none.
func TestARolesPenaltiesGoWhereTheProviderTakesThem(t *testing.T) {
	v, b := guide.Deps(t)
	dry, freq, last := 0.8, 0.2, 64
	pen := &agent.SamplingPenalties{DRYMultiplier: &dry, Frequency: &freq, RepeatLastN: &last}
	all := []string{agent.PenaltyRepeat, agent.PenaltyFrequency, agent.PenaltyPresence, agent.PenaltyDRY}
	for _, c := range []struct {
		name  string
		kinds []string
		want  *agent.SamplingPenalties
	}{
		{"llama.cpp", all, pen},
		{"ollama", []string{agent.PenaltyFrequency, agent.PenaltyPresence}, &agent.SamplingPenalties{Frequency: &freq}},
		{"anthropic", nil, nil},
	} {
		caps := llamaCaps
		caps.Penalties = c.kinds
		p := capped{&recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {mit}})}, caps}
		tr, err := New(v, nil, Config{Model: Role{Provider: p, Penalties: pen}, Base: b})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.Ask(context.Background(), mitQ); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for i, req := range p.reqs {
			if !samePenalties(req.Penalties, c.want) {
				t.Errorf("%s: request %d carried %+v", c.name, i, req.Penalties)
			}
		}
		if len(p.reqs) == 0 {
			t.Errorf("%s: no requests", c.name)
		}
	}
}

func samePenalties(a, b *agent.SamplingPenalties) bool {
	if a == nil || b == nil {
		return a == b
	}
	f := func(x, y *float64) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	n := func(x, y *int) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	return f(a.Repeat, b.Repeat) && n(a.RepeatLastN, b.RepeatLastN) && f(a.Frequency, b.Frequency) && f(a.Presence, b.Presence) &&
		f(a.DRYMultiplier, b.DRYMultiplier) && f(a.DRYBase, b.DRYBase) && n(a.DRYAllowedLength, b.DRYAllowedLength)
}

// With Ledger, every step's call after an action ends with the ledger in chakra's slots, as the
// user's, under LedgerHeader with a slot per list; the first, before anything happened, doesn't.
// chakra's guided finalizing call rewrites the answer without the request stages, so it carries none.
// control: without Ledger, no call carries one.
func TestTheLedgerFollowsTheConversation(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	for _, ledger := range []bool{true, false} {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {"values pkg.license", mit}})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{Ledger: ledger}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.Ask(context.Background(), mitQ); err != nil {
			t.Fatal(err)
		}
		if len(rec.reqs) != 3 {
			t.Fatalf("ledger %v: %d requests", ledger, len(rec.reqs))
		}
		for i, req := range rec.reqs[:2] {
			ms := req.Messages
			last := ms[len(ms)-1]
			found := "\n\n[" + strings.TrimSuffix(d.LedgerFound, ":") + "]\n- values pkg.license"
			has := last.Role == agent.RoleUser && strings.HasPrefix(last.Text, d.LedgerHeader+found)
			if want := ledger && i > 0; has != want {
				t.Errorf("ledger %v: request %d ends with %q", ledger, i, last.Text)
			}
		}
	}
}

// A model building its program a literal at a time (#514) adds, sees a wrong value answer nothing at
// the literal that wrote it, drops it, adds the right one and answers with the tail alone, as a tool
// role and as a text role. control: the same script without Build can't take an add, and its tail
// alone never parses.
func TestAModelBuildsItsProgramALiteralAtATime(t *testing.T) {
	v, b := guide.Deps(t)
	script := []string{"add pkg(?p, ?l)", `add ?l = "mit"`, "drop", `add ?l = "MIT"`, "=> ?p"}
	for _, text := range []bool{false, true} {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: script})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: text}, Base: b, Actions: &nl.Actions{Build: true}})
		if err != nil {
			t.Fatal(err)
		}
		p, trace, err := tr.Ask(context.Background(), mitQ)
		if err != nil || p.Text != `pkg(?p, ?l), ?l = "MIT" => ?p` {
			t.Fatalf("text %v: Ask = %q, %v; tries %+v", text, p.Text, err, trace.Tries)
		}
		if len(trace.Steps) != 4 || !strings.Contains(trace.Steps[1].Result, "no rows") || !strings.HasPrefix(trace.Steps[2].Result, "Dropped") {
			t.Errorf("text %v: steps %+v", text, trace.Steps)
		}
		if len(trace.Tries) != 1 {
			t.Errorf("text %v: %d tries", text, len(trace.Tries))
		}
	}
	ctl := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: script})}
	tr, err := New(v, nil, Config{Model: Role{Provider: ctl, TextActions: true}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	if p, _, err := tr.Ask(context.Background(), mitQ); err == nil {
		t.Errorf("control: without Build the script answered %q", p.Text)
	}
}

// toolSaid says some request carries a tool result opening with prefix.
func toolSaid(reqs []agent.ProviderRequest, prefix string) bool {
	for _, req := range reqs {
		for _, m := range req.Messages {
			if m.Role == agent.RoleTool && strings.HasPrefix(m.Text, prefix) {
				return true
			}
		}
	}
	return false
}
