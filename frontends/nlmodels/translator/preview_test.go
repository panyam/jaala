package translator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

const previewQ, previewMit = "which packages are MIT licensed?", `pkg(?p, "MIT") => ?p`

// The pack's preview goes before the question as the user's, on every call of the turn, Compact's
// included, and to a vote's draws. control: without Preview the question comes first.
func TestThePreviewGoesBeforeTheQuestion(t *testing.T) {
	v, b := guide.Deps(t)
	pack := &nl.Pack{Preview: func(context.Context, string) []string { return []string{"pkg"} }}
	for _, compact := range []bool{false, true} {
		m := model("small", "look pkg", previewMit, previewMit, previewMit)
		o := nl.Options{Base: b, Pack: pack, Actions: &nl.Actions{Compact: compact}, Vote: &nl.Vote{Drafts: 2}}
		if p, tr, err := translate(context.Background(), previewQ, v, nil, m, o); err != nil || p.Text != previewMit {
			t.Fatalf("compact %v: %+v, %v (%v)", compact, p, err, kinds(tr))
		}
		if len(m.asked) < 3 {
			t.Fatalf("compact %v: %d requests", compact, len(m.asked))
		}
		for i, p := range m.asked {
			at := -1
			for j, turn := range p.Turns {
				if turn.Role == nl.RoleUser && strings.Contains(turn.Text, "pkg(name, license) answered") {
					at = j
				}
			}
			if at < 0 || at+1 >= len(p.Turns) || !strings.HasPrefix(p.Turns[at+1].Text, previewQ) {
				t.Errorf("compact %v: request %d: the preview at turn %d of %+v", compact, i, at, p.Turns)
			}
		}
	}
	ctl := model("small", previewMit)
	if _, _, err := translate(context.Background(), previewQ, v, nil, ctl, nl.Options{Base: b, Pack: &nl.Pack{}}); err != nil {
		t.Fatal(err)
	}
	if first := ctl.asked[0].Turns[0].Text; !strings.HasPrefix(first, previewQ) {
		t.Errorf("control: the first turn %q", first)
	}
}
