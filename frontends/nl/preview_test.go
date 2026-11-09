package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// Preview reads a relation's rows under its arguments' names and a values target's values, in the
// pack's order, names what it couldn't read, and asks the pack once per session. The values it
// showed repair a later answer's spelling, as an action's do. control: without a Base it reads
// nothing, and a session that previewed nothing doesn't repair.
func TestPreviewShowsRowsAndValues(t *testing.T) {
	v, b := guide.Deps(t)
	asked := 0
	pack := &nl.Pack{Preview: func(_ context.Context, q string) []string {
		asked++
		if q != mitQ {
			t.Errorf("Preview was asked %q", q)
		}
		return []string{"pkg", "imports.1", "nope"}
	}}
	s, err := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Pack: pack, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	samples, err := s.Preview(context.Background())
	if len(samples) != 2 || samples[0].Target != "pkg" || samples[1].Target != "imports.1" {
		t.Fatalf("samples %+v", samples)
	}
	if !strings.HasPrefix(samples[0].Text, "pkg(name, license) answered") || !strings.Contains(samples[0].Text, `"app", "MIT"`) {
		t.Errorf("pkg's sample %q", samples[0].Text)
	}
	if !strings.HasPrefix(samples[1].Text, "values of imports.1 answered") {
		t.Errorf("imports.1's sample %q", samples[1].Text)
	}
	if err == nil || !strings.Contains(err.Error(), "preview nope") {
		t.Errorf("err %v", err)
	}
	if again, _ := s.Preview(context.Background()); len(again) != 2 || asked != 1 {
		t.Errorf("a second Preview read %d samples, the pack asked %d times", len(again), asked)
	}
	vd := s.NewConversation(nil, true).Judge(context.Background(), reply(`pkg(?p, "mit") => ?p`))
	if vd.Program == nil || vd.Program.Text != mit {
		t.Errorf("a guessed spelling after the preview: %+v", vd)
	}

	none, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Pack: pack, Actions: &nl.Actions{}})
	if samples, err := none.Preview(context.Background()); samples != nil || err != nil || asked != 1 {
		t.Errorf("control: without a Base, %+v, %v, asked %d", samples, err, asked)
	}
	plain, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{}})
	if vd := plain.NewConversation(nil, true).Judge(context.Background(), reply(`pkg(?p, "mit") => ?p`)); vd.Program != nil {
		t.Errorf("control: with nothing previewed, the spelling was repaired: %+v", vd.Program)
	}
}
