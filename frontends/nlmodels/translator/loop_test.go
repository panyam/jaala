package translator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// metered is a script whose every call reports in tokens in and out tokens out, in plus out more
// on each call than the last, so a Try or Step shows which calls it was charged.
type metered struct {
	*script
	in, out, calls int
}

func (m *metered) usage() *agent.Usage {
	m.calls++
	return &agent.Usage{InputTokens: m.in * m.calls, OutputTokens: m.out * m.calls}
}

func (m *metered) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	res, err := m.script.Generate(ctx, req)
	if res != nil {
		res.Usage = m.usage()
	}
	return res, err
}

func (m *metered) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	st, err := m.script.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var ds []agent.Delta
	for {
		d, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if d.Kind == agent.DeltaFinish {
			ds = append(ds, agent.Delta{Kind: agent.DeltaUsage, Usage: m.usage()})
		}
		ds = append(ds, d)
	}
	return agent.NewStubProvider(agent.StubTurn{Deltas: ds}).Stream(ctx, req)
}

// A Step carries its own call's tokens, and a Try its reply's and the guided call that rewrote it.
// control: the turn's tokens are all on one Step or Try, none twice.
func TestEachStepAndTryCarriesItsOwnTokens(t *testing.T) {
	v, b := guide.Deps(t)
	m := &metered{script: scripted("small", map[string][]string{mitQ: {"look pkg", mit}}), in: 10, out: 1}
	tr, err := New(v, nil, Config{Model: Role{Provider: m, Label: "small"}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	_, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil {
		t.Fatal(err)
	}
	// calls: 1 the look (10, 1), 2 the free answer (20, 2), 3 its guided rewrite (30, 3)
	if len(trace.Steps) != 1 || trace.Steps[0].TokensIn != 10 || trace.Steps[0].TokensOut != 1 || trace.Steps[0].Calls != 1 {
		t.Errorf("step %+v", trace.Steps)
	}
	if len(trace.Tries) != 1 || trace.Tries[0].TokensIn != 50 || trace.Tries[0].TokensOut != 5 || trace.Tries[0].Calls != 2 {
		t.Errorf("try %+v", trace.Tries)
	}
	if m.calls != 3 {
		t.Errorf("control: %d calls", m.calls)
	}
}

// A token budget the first answers spend ends Ask with a BudgetSpent, and so does a time budget;
// Abstain's Budget turns either into an abstention. control: a budget left unspent answers.
func TestABudgetStopsTheRunnerLoop(t *testing.T) {
	v, b := guide.Deps(t)
	bad := map[string][]string{mitQ: {`pkg(?p, "nope") => ?p`, `pkg(?p, "zz") => ?p`, mit}}
	ask := func(budget nl.Budget, abstain *nl.Abstain) (nl.Trace, error) {
		m := &metered{script: scripted("small", bad), in: 100, out: 10}
		tr, err := New(v, nil, Config{Model: Role{Provider: m}, Base: b, Budget: budget, Abstain: abstain})
		if err != nil {
			t.Fatal(err)
		}
		_, trace, err := tr.Ask(context.Background(), mitQ)
		return trace, err
	}
	var spent *nl.BudgetSpent
	if trace, err := ask(nl.Budget{Tokens: 150}, nil); !errors.As(err, &spent) || spent.Tokens < 150 || len(trace.Tries) == 0 || len(trace.Tries) > 2 {
		t.Errorf("tokens: %v after %d tries", err, len(trace.Tries))
	}
	if _, err := ask(nl.Budget{Time: time.Nanosecond}, nil); !errors.As(err, &spent) {
		t.Errorf("time: %v", err)
	}
	var abstained *nl.Abstained
	if _, err := ask(nl.Budget{Tokens: 150}, &nl.Abstain{Budget: true}); !errors.As(err, &abstained) || abstained.Reason != nl.AbstainBudget {
		t.Errorf("abstain: %v", err)
	}
	if _, err := ask(nl.Budget{Tokens: 100000}, nil); err != nil {
		t.Errorf("control: an unspent budget: %v", err)
	}
}

// A model with no answer accepted abstains under Abstain's NoAnswer, with its closest draft.
// control: without Abstain it fails with ErrNoProgram.
func TestTheRunnerLoopAbstains(t *testing.T) {
	v, b := guide.Deps(t)
	replies := map[string][]string{mitQ: {`pkg(?p, "nope") => ?p`, `pkg(?p, "zz") => ?p`, `pkg(?p, "yy") => ?p`}}
	tr, err := New(v, nil, Config{Model: Role{Provider: scripted("small", replies)}, Base: b, Abstain: &nl.Abstain{NoAnswer: true}})
	if err != nil {
		t.Fatal(err)
	}
	var abstained *nl.Abstained
	if _, _, err := tr.Ask(context.Background(), mitQ); !errors.As(err, &abstained) || abstained.Reason != nl.AbstainNoAnswer || abstained.Draft.Text == "" {
		t.Errorf("Ask: %v", err)
	}
	plain, _ := New(v, nil, Config{Model: Role{Provider: scripted("small", replies)}, Base: b})
	if _, _, err := plain.Ask(context.Background(), mitQ); !errors.Is(err, nl.ErrNoProgram) {
		t.Errorf("control: %v", err)
	}
}

// The actions are tools only while the model has steps left, and a Continue is the user's turn.
func TestTheRunnerOffersToolsUntilTheStepsRunOut(t *testing.T) {
	v, b := guide.Deps(t)
	m := scripted("small", map[string][]string{mitQ: {"look pkg", `pkg(?p, "nope") => ?p`, mit}})
	rec := &recordingProvider{Provider: m}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{Steps: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Ask(context.Background(), mitQ); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) < 3 || len(rec.reqs[0].Tools) != 3 || len(rec.reqs[1].Tools) != 0 || rec.reqs[1].Guided == nil {
		t.Fatalf("requests: %d, tools %d then %d", len(rec.reqs), len(rec.reqs[0].Tools), len(rec.reqs[1].Tools))
	}
	last := rec.reqs[len(rec.reqs)-1].Messages
	if note := last[len(last)-1]; note.Role != agent.RoleUser {
		t.Errorf("the refusal went back as %s", note.Role)
	}
}

// recordingProvider keeps each request it passes on.
type recordingProvider struct {
	agent.Provider
	reqs []agent.ProviderRequest
}

func (r *recordingProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	r.reqs = append(r.reqs, req)
	return r.Provider.Stream(ctx, req)
}

func (r *recordingProvider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	r.reqs = append(r.reqs, req)
	return r.Provider.Generate(ctx, req)
}

func (r *recordingProvider) ProviderCapabilities() agent.Capabilities { return llamaCaps }

// A TextActions role is offered no tools: its prefix lists the actions, a reply that is one is a
// Step, and every reply is held to the action grammar until its steps run out, then to the draft's.
// control: the same script on a tool role takes its look as a tool call.
func TestATextRoleTakesItsActionsInItsReplies(t *testing.T) {
	v, b := guide.Deps(t)
	replies := map[string][]string{mitQ: {"look pkg", `pkg(?p, "nope") => ?p`, mit}}
	rec := &recordingProvider{Provider: scripted("small", replies)}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: true}, Base: b, Actions: &nl.Actions{Steps: 1}})
	if err != nil {
		t.Fatal(err)
	}
	p, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil || p.Text != mit {
		t.Fatalf("Ask = %+v, %v", p, err)
	}
	if len(trace.Steps) != 1 || trace.Steps[0].Action != nl.ActionLook || !strings.Contains(trace.Steps[0].Result, "pkg(name, license)") {
		t.Fatalf("steps %+v", trace.Steps)
	}
	if len(trace.Tries) != 2 || trace.Tries[0].Kind != nl.KindEmpty {
		t.Fatalf("tries %+v", trace.Tries)
	}
	if len(rec.reqs) != 3 {
		t.Fatalf("%d requests", len(rec.reqs))
	}
	for i, req := range rec.reqs {
		if len(req.Tools) != 0 || !strings.Contains(req.Instructions, "values TARGET") || req.Guided == nil {
			t.Errorf("request %d: %d tools, actions listed %v, grammar %v", i, len(req.Tools), strings.Contains(req.Instructions, "values TARGET"), req.Guided != nil)
		}
	}
	if g := rec.reqs[0].Guided; g == nil || !strings.Contains(g.Grammar, `"look "`) {
		t.Errorf("the first request's grammar has no actions")
	}
	for _, i := range []int{1, 2} {
		if g := rec.reqs[i].Guided; g == nil || strings.Contains(g.Grammar, `"look "`) || !strings.HasPrefix(g.Grammar, "root ::=") {
			t.Errorf("request %d, after the last step, is held to the actions", i)
		}
	}
	if last := rec.reqs[1].Messages; len(last) != 3 || last[1].Text != "look pkg" || last[2].Role != agent.RoleUser {
		t.Errorf("the look went back as %+v", last)
	}

	ctl := &recordingProvider{Provider: scripted("small", replies)}
	tools, err := New(v, nil, Config{Model: Role{Provider: ctl}, Base: b, Actions: &nl.Actions{Steps: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, trace, err := tools.Ask(context.Background(), mitQ); err != nil || len(trace.Steps) != 1 || len(ctl.reqs[0].Tools) != 3 {
		t.Errorf("control: %v, steps %d, tools %d", err, len(trace.Steps), len(ctl.reqs[0].Tools))
	}
}

// A text step is charged its own call, and the answer after it only its own, with no guided call
// to rewrite it. control: the turn made two calls.
func TestATextStepCarriesItsOwnTokens(t *testing.T) {
	v, b := guide.Deps(t)
	m := &metered{script: scripted("small", map[string][]string{mitQ: {"look pkg", mit}}), in: 10, out: 1}
	tr, err := New(v, nil, Config{Model: Role{Provider: m, Label: "small", TextActions: true}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	_, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil {
		t.Fatal(err)
	}
	// calls: 1 the look (10, 1), 2 the answer (20, 2)
	if len(trace.Steps) != 1 || trace.Steps[0].TokensIn != 10 || trace.Steps[0].TokensOut != 1 || trace.Steps[0].Model != "small" || trace.Steps[0].Calls != 1 {
		t.Errorf("step %+v", trace.Steps)
	}
	if len(trace.Tries) != 1 || trace.Tries[0].TokensIn != 20 || trace.Tries[0].TokensOut != 2 || trace.Tries[0].Calls != 1 {
		t.Errorf("try %+v", trace.Tries)
	}
	if m.calls != 2 {
		t.Errorf("control: %d calls", m.calls)
	}
}

// TextActions is per role: a text model's turn offers no tools, and the tool fallback after it is
// offered them, its prefix not listing the text actions.
func TestATextModelFallsBackToAToolModel(t *testing.T) {
	v, b := guide.Deps(t)
	small := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {"look pkg", `pkg(?p, "nope") => ?p`}})}
	big := &recordingProvider{Provider: scripted("big", map[string][]string{mitQ: {"look pkg", mit}})}
	tr, err := New(v, nil, Config{Model: Role{Provider: small, TextActions: true}, Fallback: Role{Provider: big}, Base: b, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	p, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil || p.Text != mit || p.Model != "fallback" {
		t.Fatalf("Ask = %+v, %v", p, err)
	}
	if len(trace.Steps) != 2 || trace.Steps[0].Model != "model" || trace.Steps[1].Model != "fallback" {
		t.Errorf("steps %+v", trace.Steps)
	}
	if len(small.reqs) != 2 || len(small.reqs[0].Tools) != 0 || !strings.Contains(small.reqs[0].Instructions, "values TARGET") {
		t.Errorf("the text model: %d requests, %d tools", len(small.reqs), len(small.reqs[0].Tools))
	}
	if len(big.reqs) == 0 || len(big.reqs[0].Tools) != 3 || big.reqs[0].Guided != nil || strings.Contains(big.reqs[0].Instructions, "values TARGET") {
		t.Errorf("the tool fallback: %d requests, %d tools", len(big.reqs), len(big.reqs[0].Tools))
	}
}

// Compact sends a text role one turn holding what its actions found.
func TestATextRoleCompacts(t *testing.T) {
	v, b := guide.Deps(t)
	rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {"values pkg.license", mit}})}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: true}, Base: b, Actions: &nl.Actions{Compact: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, trace, err := tr.Ask(context.Background(), mitQ); err != nil || len(trace.Steps) != 1 {
		t.Fatalf("%v, steps %+v", err, trace.Steps)
	}
	if ms := rec.reqs[1].Messages; len(ms) != 1 || ms[0].Role != agent.RoleUser || !strings.Contains(ms[0].Text, `"MIT"`) {
		t.Errorf("the second request: %+v", ms)
	}
}

// A text role repeating a try gets the stuck note and a warmer call; still repeating at the next, its
// actions end, and its answer after a try line is taken as the draft. control: a role that doesn't
// repeat gets no note.
func TestATextRoleStuckOnATryIsNudgedThenMadeToAnswer(t *testing.T) {
	v, b := guide.Deps(t)
	nope := "try\n" + `pkg(?p, "nope") => ?p`
	rec := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {nope, nope, nope, "try\n" + mit}})}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec, TextActions: true}, Base: b, Actions: &nl.Actions{Steps: 6}})
	if err != nil {
		t.Fatal(err)
	}
	p, trace, err := tr.Ask(context.Background(), mitQ)
	if err != nil || p.Text != mit {
		t.Fatalf("Ask = %+v, %v", p, err)
	}
	if len(trace.Steps) != 3 || !strings.HasPrefix(trace.Steps[1].Result, nl.DefaultPrompts().TriedAgain) || len(trace.Tries) != 1 {
		t.Fatalf("steps %+v, tries %+v", trace.Steps, trace.Tries)
	}
	if len(rec.reqs) != 4 {
		t.Fatalf("%d requests", len(rec.reqs))
	}
	for i, req := range rec.reqs {
		note := false
		for _, m := range req.Messages {
			note = note || m.Role == agent.RoleUser && strings.HasPrefix(m.Text, "You have repeated try 2 times")
		}
		warm := req.Temperature != nil && *req.Temperature == FallbackTemperature
		if want := i == 2; note != want || warm != want {
			t.Errorf("request %d: note %v, warm %v", i, note, warm)
		}
		final := req.Guided != nil && !strings.Contains(req.Guided.Grammar, `"look "`) && strings.Contains(req.Guided.Grammar, `"try\n"`)
		if final != (i == 3) {
			t.Errorf("request %d: held to the final grammar %v", i, final)
		}
	}

	ctl := &recordingProvider{Provider: scripted("small", map[string][]string{mitQ: {"look pkg", nope, mit}})}
	tools, err := New(v, nil, Config{Model: Role{Provider: ctl, TextActions: true}, Base: b, Actions: &nl.Actions{Steps: 6}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tools.Ask(context.Background(), mitQ); err != nil {
		t.Fatal(err)
	}
	for i, req := range ctl.reqs {
		for _, m := range req.Messages {
			if isStuckNote(m) {
				t.Errorf("control: request %d has a note %q", i, m.Text)
			}
		}
	}
}

// A tool role taking turns between two tries is keyed by its tool calls, and gets the note too.
func TestAToolRoleAlternatingTriesGetsTheNote(t *testing.T) {
	v, b := guide.Deps(t)
	a, c := "try\n"+`pkg(?p, "nope") => ?p`, "try\n"+`pkg(?p, "none") => ?p`
	rec := &recordingProvider{Provider: scripted("big", map[string][]string{mitQ: {a, c, a, c, mit}})}
	tr, err := New(v, nil, Config{Model: Role{Provider: rec}, Base: b, Actions: &nl.Actions{Steps: 6}})
	if err != nil {
		t.Fatal(err)
	}
	if p, _, err := tr.Ask(context.Background(), mitQ); err != nil || p.Text != mit {
		t.Fatalf("Ask = %+v, %v", p, err)
	}
	noted := -1
	for i, req := range rec.reqs {
		for _, m := range req.Messages {
			if m.Role == agent.RoleUser && strings.Contains(m.Text, "alternated") && noted < 0 {
				noted = i
			}
		}
	}
	if noted != 4 {
		t.Errorf("the note first went with request %d of %d", noted, len(rec.reqs))
	}
}
