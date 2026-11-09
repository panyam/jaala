package translator

import (
	"context"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/presets"
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
				if isStuckNote(m) {
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

// A model asks for the paths between two ends, reads the joins that answered, and answers with the
// first one's program (#514 piece 5), as a tool role and as a text role. control: without the pack's
// Shape the same script's paths is no action, and its turn fails.
func TestAModelPicksAJoinFromPaths(t *testing.T) {
	b := guide.Code(t)
	v := b.Vocabulary()
	shape := nl.Shape{Kinds: "node", Edges: []string{"calls", "contains"}, Refs: []nl.Ref{{Relation: "attr", Key: "callee"}}}
	q := "which oneauth functions does goapp call?"
	answer := `node(?from, "func"), str.glob(?from, "goapp/*"), contains(?from, ?n1), node(?n1, "callsite"), attr(?n1, "callee", ?to), node(?to, "func"), str.glob(?to, "oneauth/*") => ?to, ?from`
	script := []string{`paths func str.glob(?x, "goapp/*") => func str.glob(?x, "oneauth/*")`, answer}
	for _, text := range []bool{false, true} {
		tr, err := New(v, &nl.Pack{Shape: &shape}, Config{Model: Role{Provider: scripted("small", map[string][]string{q: script}), TextActions: text}, Base: b, Actions: &nl.Actions{}})
		if err != nil {
			t.Fatal(err)
		}
		p, trace, err := tr.Ask(context.Background(), q)
		if err != nil || p.Text != answer || len(p.Rows) != 1 {
			t.Fatalf("text %v: Ask = %q (%d rows), %v; steps %+v", text, p.Text, len(p.Rows), err, trace.Steps)
		}
		if len(trace.Steps) != 1 || !strings.Contains(trace.Steps[0].Result, `"goapp/svc.Auth", "oneauth/acct.Get"`) {
			t.Errorf("text %v: steps %+v", text, trace.Steps)
		}
	}
	tr, err := New(v, &nl.Pack{}, Config{Model: Role{Provider: scripted("small", map[string][]string{q: script}), TextActions: true}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, trace, _ := tr.Ask(context.Background(), q); len(trace.Steps) == 1 && strings.Contains(trace.Steps[0].Result, "oneauth/acct.Get") {
		t.Errorf("control: without a Shape, paths answered %q", trace.Steps[0].Result)
	}
}

// isStuckNote says m is the stuck detector's note, Prompts.Stuck's text in whatever role it went as.
func isStuckNote(m agent.Message) bool {
	head, _, _ := strings.Cut(nl.DefaultPrompts().Stuck, "%s")
	return strings.HasPrefix(m.Text, head)
}

// The turn's Runner comes from chakra's graph-query preset (#528): its retrieval, stuck and ledger
// stages, jaala's actions stage in place of the preset's, then jaala's compaction and label, with
// the repeat cache left out (the Conversation answers repeats) and no penalties unless the role sets
// them. control: without a preview or a ledger, those stages are left out.
func TestTheTurnIsBuiltFromThePreset(t *testing.T) {
	v, b := guide.Deps(t)
	pack := &nl.Pack{Preview: func(context.Context, string) []string { return []string{"pkg"} }}
	stages := func(o nl.Options, tr *Translator) (agent.RunnerConfig, []string) {
		s, err := nl.NewSession(context.Background(), mitQ, v, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		l := &loop{t: tr, o: o}
		cfg, err := l.preset(s, s.NewConversation(nil, true), tr.model, s.Prefix(), s.Grammar(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, st := range cfg.StepStages {
			names = append(names, st.Name)
		}
		return cfg, names
	}
	caps := llamaCaps
	caps.Penalties = []string{agent.PenaltyRepeat, agent.PenaltyFrequency, agent.PenaltyPresence, agent.PenaltyDRY}
	tr, err := New(v, pack, Config{Model: Role{Provider: capped{&recordingProvider{Provider: scripted("small", nil)}, caps}}, Base: b})
	if err != nil {
		t.Fatal(err)
	}
	o := nl.Options{Base: b, Pack: pack, Actions: &nl.Actions{Ledger: true, Compact: true}}
	cfg, names := stages(o, tr)
	want := []string{presets.StageRetrieval, presets.StageStuck, "jaala.translator.actions", presets.StageLedger, "jaala.translator.compact", "jaala.translator"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("stages %v, want %v", names, want)
	}
	if len(cfg.ToolMiddleware) != 0 || cfg.Generation.Penalties != nil || cfg.MaxFreeRefusals != nl.MaxRepeats {
		t.Errorf("middleware %d, penalties %+v, free refusals %d", len(cfg.ToolMiddleware), cfg.Generation.Penalties, cfg.MaxFreeRefusals)
	}
	_, plain := stages(nl.Options{Base: b, Actions: &nl.Actions{}}, tr)
	if strings.Join(plain, " ") != presets.StageStuck+" jaala.translator.actions jaala.translator" {
		t.Errorf("control: without a preview or a ledger, stages %v", plain)
	}
}

// x04's pattern brings one try back three times in five steps, between other actions: neither a
// repeat in a row nor an alternation, so only the preset's window catches it, and the note goes with
// the next call. control: the same script with the third try changed draws no note.
func TestAWindowOfRepeatsDrawsTheStuckNote(t *testing.T) {
	v, b := guide.Deps(t)
	nope := "try\n" + `pkg(?p, "nope") => ?p`
	run := func(script []string) bool {
		rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: script})}
		tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{Steps: 8}})
		if err != nil {
			t.Fatal(err)
		}
		if p, _, err := tr.Ask(context.Background(), mitQ); err != nil || p.Text != mit {
			t.Fatalf("Ask = %q, %v", p.Text, err)
		}
		for _, req := range rec.reqs {
			for _, m := range req.Messages {
				if isStuckNote(m) {
					return true
				}
			}
		}
		return false
	}
	if !run([]string{nope, "look pkg", nope, "values pkg.license", nope, mit}) {
		t.Errorf("a try back three times in five steps drew no note")
	}
	if run([]string{nope, "look pkg", nope, "values pkg.license", "try\n" + `pkg(?p, "none") => ?p`, mit}) {
		t.Errorf("control: two tries of one draft drew the note")
	}
}
