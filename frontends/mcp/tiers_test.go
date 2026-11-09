package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
)

// tiering records the tier policy and models each ask call hands the Translator.
type tiering struct {
	*sessions
	asked []string
}

func (t *tiering) Translate(ctx context.Context, q string, o nl.Options, model, fallback bool) (nl.Program, nl.Trace, error) {
	got := "no tiers"
	if o.Tiers != nil {
		got = o.Tiers.Policy
		if o.Tiers.Local.Calls != 2 || o.Tiers.Frontier.Tokens != 900 {
			got += " without the server's caps"
		}
	}
	if model {
		got += " +model"
	}
	if fallback {
		got += " +fallback"
	}
	t.asked = append(t.asked, got)
	p, tr, err := t.sessions.Translate(ctx, q, o, model, fallback)
	tr.Stamp(nl.TierLocal)
	return p, tr, err
}

// ask's mode is the tier policy, both and no mode reading as local-then-frontier, with the server's
// caps for each tier; the trace names each try's tier. control: with no frontier model, no mode asks
// the local tier alone, as both did, and frontier is refused.
func TestAskModeIsATierPolicy(t *testing.T) {
	caps := nl.Tiers{Local: nl.Budget{Calls: 2}, Frontier: nl.Budget{Tokens: 900}}
	reply := func() *scripted { return &scripted{name: "small", replies: []string{`road("a", ?t) => ?t`}} }
	tr := &tiering{sessions: translating(reply(), &scripted{name: "big"})}
	c := dial(t, pack, mcp.Options{Translator: tr, Tiers: caps})
	for _, mode := range []string{"", "both", "local-then-frontier", "local", "frontier"} {
		tr.sessions.model = reply()
		tr.sessions.fallback = &scripted{name: "big", replies: []string{`road("a", ?t) => ?t`}}
		text := c.ToolCall("ask", map[string]any{"question": "which town is next after a?", "mode": mode})
		if !strings.Contains(text, " (local): accepted") {
			t.Errorf("mode %q: the trace doesn't name the tier:\n%s", mode, text)
		}
	}
	want := []string{
		"local-then-frontier +model +fallback", "local-then-frontier +model +fallback", "local-then-frontier +model +fallback",
		"local +model", "frontier +fallback",
	}
	if strings.Join(tr.asked, "|") != strings.Join(want, "|") {
		t.Errorf("asked\n%s\nwant\n%s", strings.Join(tr.asked, "\n"), strings.Join(want, "\n"))
	}
	if res := call(t, c, "ask", map[string]any{"question": "q", "mode": "everything"}); !res.IsError || !strings.Contains(res.Content[0].Text, "want local, frontier or local-then-frontier") {
		t.Errorf("an unknown mode: %+v", res.Content)
	}

	alone := &tiering{sessions: translating(reply())}
	c = dial(t, pack, mcp.Options{Translator: alone, Tiers: caps})
	c.ToolCall("ask", map[string]any{"question": "which town is next after a?"})
	if len(alone.asked) != 1 || alone.asked[0] != "local +model" {
		t.Errorf("control: with no frontier model, asked %v", alone.asked)
	}
	if res := call(t, c, "ask", map[string]any{"question": "q", "mode": "frontier"}); !res.IsError || !strings.Contains(res.Content[0].Text, "no frontier model") {
		t.Errorf("control: frontier with no frontier model: %+v", res.Content)
	}
}
