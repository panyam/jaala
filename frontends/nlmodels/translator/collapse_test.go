package translator

import (
	"context"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// copies counts the messages before req's stuck note that carry draft: a tool call's argument or a
// text reply. It fails the test when req has no note.
func copies(t *testing.T, req agent.ProviderRequest, draft string) int {
	t.Helper()
	n := 0
	for _, m := range req.Messages {
		if isStuckNote(m) {
			return n
		}
		if m.Role != agent.RoleAssistant {
			continue
		}
		hit := strings.Contains(m.Text, draft)
		for _, tc := range m.ToolCalls {
			hit = hit || strings.Contains(toolArg(tc), draft)
		}
		if hit {
			n++
		}
	}
	t.Fatalf("no stuck note in the request")
	return 0
}

// With Collapse, the call after the stuck detector's first finding holds the repeated try once, then
// the note, for a tool role and for a role writing its actions as text, and the trace still holds
// every step. control: without it, that call holds every copy.
func TestCollapseShowsARepeatedActionOnce(t *testing.T) {
	v, b := guide.Deps(t)
	draft := `pkg(?p, "nope") => ?p`
	nope := "try\n" + draft
	for _, text := range []bool{false, true} {
		for _, collapse := range []bool{true, false} {
			rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {nope, nope, nope, "try\n" + mit, mit}})}
			tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: text}, Base: b, Actions: &nl.Actions{Steps: 6, Collapse: collapse}})
			if err != nil {
				t.Fatal(err)
			}
			_, trace, err := tr.Ask(context.Background(), mitQ)
			if err != nil {
				t.Fatalf("text %v, collapse %v: %v", text, collapse, err)
			}
			noted := -1
			for i, req := range rec.reqs {
				for _, m := range req.Messages {
					if isStuckNote(m) && noted < 0 {
						noted = i
					}
				}
			}
			if noted < 0 {
				t.Fatalf("text %v, collapse %v: no request carried the note", text, collapse)
			}
			want := 2 // the first try, and the repeat that tripped the detector
			if collapse {
				want = 1
			}
			if got := copies(t, rec.reqs[noted], draft); got != want {
				t.Errorf("text %v, collapse %v: the noted call holds %d copies of the try, want %d", text, collapse, got, want)
			}
			tries := 0
			for _, s := range trace.Steps {
				if s.Action == nl.ActionTry && strings.Contains(s.Text, draft) {
					tries++
				}
			}
			if tries < 2 {
				t.Errorf("text %v, collapse %v: the trace keeps %d of the repeated tries", text, collapse, tries)
			}
		}
	}
}

// Under Compact the model is sent one turn holding each action once, so Collapse sends the same
// requests as without it.
func TestCollapseChangesNothingUnderCompact(t *testing.T) {
	v, b := guide.Deps(t)
	nope := "try\n" + `pkg(?p, "nope") => ?p`
	var sent [2][]string
	for i, collapse := range []bool{false, true} {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {nope, nope, nope, "try\n" + mit, mit}})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{Steps: 6, Compact: true, Collapse: collapse}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.Ask(context.Background(), mitQ); err != nil {
			t.Fatal(err)
		}
		for _, req := range rec.reqs {
			for _, m := range req.Messages {
				sent[i] = append(sent[i], string(m.Role)+":"+m.Text)
			}
			sent[i] = append(sent[i], "--")
		}
	}
	if strings.Join(sent[0], "\n") != strings.Join(sent[1], "\n") {
		t.Errorf("Collapse changed what Compact sent:\n%s\n\nagainst\n%s", strings.Join(sent[1], "\n"), strings.Join(sent[0], "\n"))
	}
}
