package translator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// script answers each question with its own replies in turn, as a model making the same mistakes
// every run would, keyed by the first line of the conversation's first message. #500 held the Runner
// loop to nl.Translate with it before nl.Translate went (#496); the arms below still check the loop
// reaches each path. A reply that is a text action (look NAME, values TARGET, find NAME, try and a draft)
// is a tool call when the request offers tools, and text otherwise. A guided request offering no
// tools right after a text answer to a request that offered them is chakra's finalizing call, and
// gets that answer again.
type script struct {
	name    string
	replies map[string][]string
	mu      sync.Mutex
	asked   map[string]int
	final   map[string]string // the text answer a finalizing call repeats, per question
	tooled  int               // requests that offered tools
	longest int               // the most messages one request carried
}

func scripted(name string, replies map[string][]string) *script {
	return &script{name: name, replies: replies, asked: map[string]int{}, final: map[string]string{}}
}

func (s *script) next(q string) (string, error) {
	n := s.asked[q]
	s.asked[q]++
	if rs := s.replies[q]; n < len(rs) {
		return rs[n], nil
	}
	return "", errors.New("no reply left")
}

// Generate, Stream and ProviderCapabilities are the agent.Provider, which reports llama.cpp's
// capabilities so the grammar goes with every answer.
func (s *script) Generate(_ context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	t, err := s.turn(req)
	if err != nil {
		return nil, err
	}
	return &agent.ProviderResponse{Text: t.Text, ToolCalls: t.ToolCalls}, nil
}

func (s *script) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	t, err := s.turn(req)
	if err != nil {
		return nil, err
	}
	return agent.NewStubProvider(t).Stream(ctx, req)
}

func (s *script) ProviderCapabilities() agent.Capabilities { return llamaCaps }

func (s *script) turn(req agent.ProviderRequest) (agent.StubTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(req.Tools) > 0 {
		s.tooled++
	}
	s.longest = max(s.longest, len(req.Messages))
	q, _, _ := strings.Cut(req.Messages[0].Text, "\n")
	if text, ok := s.final[q]; ok && req.Guided != nil && len(req.Tools) == 0 {
		delete(s.final, q)
		return agent.StubTurn{Text: text}, nil
	}
	delete(s.final, q)
	r, err := s.next(q)
	if err != nil {
		return agent.StubTurn{}, err
	}
	if len(req.Tools) == 0 {
		return agent.StubTurn{Text: r}, nil
	}
	if name, arg, ok := textAction(r); ok {
		key := map[string]string{nl.ActionLook: "name", nl.ActionFind: "name", nl.ActionValues: "target", nl.ActionTry: "draft", nl.ActionAdd: "literal", nl.ActionPaths: "ends"}[name]
		args := map[string]string{}
		if key != "" {
			args[key] = arg
		}
		var tc agent.ToolCall
		raw, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("call-%d", s.asked[q]), "name": name, "args": args})
		if err := json.Unmarshal(raw, &tc); err != nil {
			return agent.StubTurn{}, err
		}
		return agent.StubTurn{ToolCalls: []agent.ToolCall{tc}}, nil
	}
	s.final[q] = r
	return agent.StubTurn{Text: r}, nil
}

// textAction reads a reply as nl's text actions are read.
func textAction(r string) (action, arg string, ok bool) {
	first, rest, _ := strings.Cut(r, "\n")
	word, inline, _ := strings.Cut(strings.TrimSpace(first), " ")
	switch word {
	case nl.ActionLook, nl.ActionValues, nl.ActionFind, nl.ActionAdd:
		return word, strings.TrimSpace(inline), rest == "" && strings.TrimSpace(inline) != ""
	case nl.ActionDrop:
		return word, "", rest == "" && strings.TrimSpace(inline) == ""
	case nl.ActionPaths:
		return word, strings.TrimSpace(inline), rest == "" && strings.Contains(inline, " => ")
	case nl.ActionTry:
		d := strings.TrimSpace(strings.TrimSpace(inline) + "\n" + rest)
		return word, d, d != ""
	}
	return "", "", false
}

// arm is one scripted configuration both loops run: the replies each model gives, and the options.
type arm struct {
	name         string
	small, big   map[string][]string
	pack         *nl.Pack
	opts         func(b *datalog.Base) nl.Options // without Fallback, which the arm's big replies decide
	cases        []nl.Case
	wantFirstTry int // the reference's first-try passes, so a broken script can't pass by agreeing
	wantPassed   int
	// actions sets the arm apart from the prompt comparison: offered as tools, the actions change
	// the prefix and the conversation's shape on purpose.
	actions bool
}

// The guide's questions with TestEval's drafting replies (frontends/nl's eval_test.go).
var (
	smallDrafts = map[string][]string{
		"what does app import directly?":            {`imports("app", ?d) => ?d`},
		"what does app depend on, directly or not?": {`depends_on("app", ?d) => ?d`, `deps.depends_on("app", ?d) => ?d`},
		"what license does util have?":              {`pkg("util") => ?l`, `pkg("util", ?l) => ?l`},
		"which packages have no license stated?":    {`pkg(?p, ?l), absent(?x) => ?p`, `pkg(?p, ?l), absent(?l) => ?p`},
		"how many packages does app depend on?":     {`deps.depends_on(App, ?d) => count(?d)`, `deps.depends_on("app", ?d) => count(?d)`},
		"what imports util?":                        {`imports("util", ?x) => ?x`},
		"list every package with its license":       {`pkg(?n, ?l) => ?l, ?n`},
		"who maintains api?":                        {`maintainer("api", ?m) => ?m`, `maintainer("api", ?m) => ?m`, `owner("api", ?m) => ?m`},
	}
	bigDrafts = map[string][]string{
		"what imports util?": {`imports(?x, "util") => ?x`},
		"who maintains api?": {`pkg("api", ?m) => ?m`},
	}
	callables = &nl.Pack{Callables: []nl.Callable{
		{Relation: "imports", Doc: "the packages a package imports directly", Args: []string{"the importing package", "the imported one"}},
		{Relation: "deps.depends_on", Args: []string{"the package", "a package it depends on"}},
		{Relation: "pkg", Args: []string{"the package", "its license, absent when unstated"}},
	}}
	picks = map[string][]string{
		"what does app import directly?":            {`imports("app", ?d)`},
		"what does app depend on, directly or not?": {`deps.depends_on("app", ?d)`},
		"what license does util have?":              {`pkg("util", ?l)`},
		"which packages have no license stated?":    {"none", `pkg(?p, ?l), absent(?l) => ?p`},
		"how many packages does app depend on?":     {"none", `deps.depends_on("app", ?d) => count(?d)`},
		"what imports util?":                        {`imports("util", ?x)`, `imports(?x, "util")`},
		"list every package with its license":       {`pkg(?p, ?l)`},
		"who maintains api?":                        {"none", `pkg("api", ?m) => ?m`},
	}
)

const mitQ, mit = "which packages are MIT licensed?", `pkg(?p, "MIT") => ?p`

func guideCases() []nl.Case {
	out := make([]nl.Case, len(guide.Cases))
	for i, c := range guide.Cases {
		out[i] = nl.Case{Name: c.Name, Question: c.Question, Want: c.Want}
	}
	return out
}

func arms() []arm {
	mitCase := []nl.Case{{Name: "mit", Question: mitQ, Want: [][]string{{"app"}}}}
	actions := func(a nl.Actions) func(*datalog.Base) nl.Options {
		return func(b *datalog.Base) nl.Options { return nl.Options{Base: b, Actions: &a} }
	}
	return []arm{
		{name: "drafting (TestEval)", small: smallDrafts, big: bigDrafts, cases: guideCases(), wantPassed: 7, wantFirstTry: 2,
			opts: func(b *datalog.Base) nl.Options { return nl.Options{Base: b} }},
		{name: "picking (TestEvalCallable)", small: picks, pack: callables, cases: guideCases(), wantPassed: 7, wantFirstTry: 6,
			opts: func(b *datalog.Base) nl.Options { return nl.Options{Base: b} }},
		{name: "actions and a repair", actions: true, small: map[string][]string{mitQ: {"look pkg", "values pkg.license", `pkg(?p, "mit") => ?p`}},
			cases: mitCase, wantPassed: 1, opts: actions(nl.Actions{})},
		{name: "an action asked twice, then out of steps", actions: true, small: map[string][]string{mitQ: {"look pkg", "look pkg", "values pkg.license", "look pkg", mit}},
			cases: mitCase, wantPassed: 1, opts: actions(nl.Actions{Steps: 2})},
		{name: "a try, compacted", actions: true, small: map[string][]string{mitQ: {"try\n" + `pkg(?p, "nope") => ?p`, `pkg(?p, "nope") => ?p`, mit}},
			cases: mitCase, wantPassed: 1, opts: actions(nl.Actions{Compact: true})},
		{name: "a vote", small: map[string][]string{mitQ: {mit, `pkg(?p, "nope") => ?p`, `pkg(?x, "MIT") => ?x`}},
			cases: mitCase, wantPassed: 1, wantFirstTry: 1,
			opts: func(b *datalog.Base) nl.Options { return nl.Options{Base: b, Vote: &nl.Vote{Drafts: 3}} }},
		{name: "a disputed vote escalated", small: map[string][]string{mitQ: {mit, `pkg(?p, "nope") => ?p`, `pkg(?p, ?l) => ?p`}},
			big: map[string][]string{mitQ: {mit, mit, mit}}, cases: mitCase, wantPassed: 1,
			opts: func(b *datalog.Base) nl.Options {
				return nl.Options{Base: b, Vote: &nl.Vote{Drafts: 3}, Escalate: 0.6}
			}},
		{name: "a model failing", small: map[string][]string{mitQ: {`nope(?p) => ?p`}}, big: map[string][]string{mitQ: {mit}},
			cases: mitCase, wantPassed: 1, opts: func(b *datalog.Base) nl.Options { return nl.Options{Base: b} }},
		{name: "a disputed pick escalated to drafting", small: map[string][]string{mitQ: {`pkg(?p, "MIT")`, `pkg(?p, "nope")`, `pkg(?p, ?l)`, mit}},
			pack: callables, cases: mitCase, wantPassed: 1,
			opts: func(b *datalog.Base) nl.Options {
				return nl.Options{Base: b, Vote: &nl.Vote{Drafts: 3}, Escalate: 0.6}
			}},
	}
}

// An arm's script reaches the paths it is named for in the Runner loop: tool calls, the repair, a
// compacted request, the vote's draws and the escalation.
func TestTheDifferentialsArmsReachTheirPaths(t *testing.T) {
	v, b := guide.Deps(t)
	run := func(a arm) (nl.Trace, *script) {
		small := scripted("small", a.small)
		cfg := Config{Model: Role{Provider: small, Label: "small"}, Base: b, Options: func(p *nl.Options) { *p = a.opts(b) }}
		if a.big != nil {
			cfg.Fallback = Role{Provider: scripted("big", a.big), Label: "big"}
		}
		tr, err := New(v, a.pack, cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, trace, _ := tr.Ask(context.Background(), a.cases[0].Question)
		return trace, small
	}
	byName := map[string]arm{}
	for _, a := range arms() {
		byName[a.name] = a
	}
	trace, _ := run(byName["actions and a repair"])
	if len(trace.Steps) != 2 || len(trace.Tries) != 2 || trace.Tries[1].Repair == "" {
		t.Errorf("actions and a repair: %d steps, tries %+v", len(trace.Steps), trace.Tries)
	}
	trace, _ = run(byName["an action asked twice, then out of steps"])
	if len(trace.Steps) != 3 || !strings.HasPrefix(trace.Steps[1].Result, nl.DefaultPrompts().AskedAgain) || len(trace.Tries) != 2 || trace.Tries[0].Kind != nl.KindParse {
		t.Errorf("out of steps: steps %+v, tries %+v", trace.Steps, trace.Tries)
	}
	trace, small := run(byName["a try, compacted"])
	if len(trace.Steps) != 1 || small.tooled == 0 || small.longest != 1 {
		t.Errorf("compacted: %d steps, %d requests with tools, longest %d messages", len(trace.Steps), small.tooled, small.longest)
	}
	trace, _ = run(byName["a vote"])
	if len(trace.Votes) != 2 || trace.Votes[0].Kind != nl.KindEmpty {
		t.Errorf("vote: %+v", trace.Votes)
	}
	trace, _ = run(byName["a disputed vote escalated"])
	if len(trace.Escalations) != 1 || !slices.ContainsFunc(trace.Tries, func(t nl.Try) bool { return t.Model == "big" }) {
		t.Errorf("escalation: %+v", trace.Escalations)
	}
}
