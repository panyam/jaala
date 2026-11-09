// Package modeltest runs tests' scripted models on the translator's chakra Runners (#496, #502). A
// Model records what it was asked as a Prompt and replies in turn, as nl.Translate's and nl.Extract's
// tests' models did before nl stopped asking models itself; Provider makes one a chakra Provider, and
// Translate and Extract ask a Translator over them what nl.Translate and nl.Extract were asked.
package modeltest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/translator"
	"github.com/panyam/jaala/ns"
)

// A Model writes text from a prompt: a test's scripted model, which nl.Model was until #502.
type Model interface {
	// Name says which model answered, for the Trace and Program.Model.
	Name() string
	Complete(ctx context.Context, p Prompt) (Completion, error)
}

// A Prompt is a request as a Model reads it: the Prefix (the instructions), the conversation so far,
// the Draw a vote's seed stands for (from 1, 0 outside a vote), and the grammar sent, "" for none.
type Prompt struct {
	Prefix  string
	Turns   []nl.Turn
	Draw    int
	Grammar string
}

// A Completion is a Model's reply and what it cost; TokensCached counts the prompt tokens a cache
// answered.
type Completion struct {
	Text                              string
	TokensIn, TokensOut, TokensCached int
}

// Provider is m as a chakra Provider.
func Provider(m Model) agent.Provider { return &provider{m: m} }

// provider is a Model as a chakra Provider, so the tests' models run on the Translator's
// Runners: each request is the Prompt it carries (prompt), the model's reply comes back with its
// tokens as usage, and a reply that is a text action (look NAME, values TARGET, find NAME, or try then
// a draft) is a tool call when the request offers tools. A guided request offering no tools right
// after a text answer to one that offered them is chakra's finalizing call: it gets the answer again
// without asking the model. It reports llama.cpp's capabilities, so the grammar and a vote's seed
// are sent.
type provider struct {
	m     Model
	mu    sync.Mutex
	final string // the answer a finalizing call repeats
	calls int
}

func (p *provider) ProviderCapabilities() agent.Capabilities {
	return agent.Capabilities{Guided: []string{agent.GuidedGrammar}, Seed: true, Temperature: true}
}

// An Offerer is a test model told which tools each request offers, before it is asked, with their
// descriptions; a request offering none tells it nil.
type Offerer interface {
	Offer(names []string, descs map[string]string)
}

// prompt is req as a Prompt: the instructions as the Prefix, each tool call as the text action
// that asks for it, each tool result as the user's turn, a seed as the Draw it stands for.
func prompt(req agent.ProviderRequest) Prompt {
	p := Prompt{Prefix: req.Instructions}
	if req.Guided != nil {
		p.Grammar = req.Guided.Grammar
	}
	if req.Seed != nil {
		p.Draw = *req.Seed + 1
	}
	for _, m := range req.Messages {
		switch {
		case m.Role == agent.RoleAssistant && len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				var args map[string]string
				json.Unmarshal(tc.Args.Raw(), &args)
				arg := ""
				for _, a := range args {
					arg = a
				}
				p.Turns = append(p.Turns, nl.Turn{Role: nl.RoleModel, Text: nl.ActionText(tc.Name, arg)})
			}
		case m.Role == agent.RoleAssistant:
			p.Turns = append(p.Turns, nl.Turn{Role: nl.RoleModel, Text: m.Text})
		case m.Role == agent.RoleSystem:
			p.Turns = append(p.Turns, nl.Turn{Role: nl.Role(agent.RoleSystem), Text: m.Text}) // a note jaala should have sent as the user's
		default:
			p.Turns = append(p.Turns, nl.Turn{Role: nl.RoleUser, Text: m.Text})
		}
	}
	return p
}

// call is the model's next reply to req, as a stub turn with its usage.
func (p *provider) call(ctx context.Context, req agent.ProviderRequest) (agent.StubTurn, *agent.Usage, error) {
	p.mu.Lock()
	if p.final != "" && req.Guided != nil && len(req.Tools) == 0 {
		text := p.final
		p.final = ""
		p.mu.Unlock()
		return agent.StubTurn{Text: text}, nil, nil
	}
	p.final = ""
	p.calls++
	n := p.calls
	p.mu.Unlock()
	var offered []string
	descs := map[string]string{}
	for _, t := range req.Tools {
		offered = append(offered, t.Name)
		descs[t.Name] = t.Description
	}
	if o, ok := p.m.(Offerer); ok {
		o.Offer(offered, descs)
	}
	c, err := p.m.Complete(ctx, prompt(req))
	if err != nil {
		return agent.StubTurn{}, nil, err
	}
	u := &agent.Usage{InputTokens: c.TokensIn, OutputTokens: c.TokensOut, CachedInputTokens: c.TokensCached}
	if len(req.Tools) > 0 {
		if name, arg, ok := TextAction(c.Text); ok && slices.Contains(offered, name) {
			key := map[string]string{nl.ActionLook: "name", nl.ActionFind: "name", nl.ActionValues: "target", nl.ActionTry: "draft", nl.ActionAdd: "literal"}[name]
			args := map[string]string{}
			if key != "" {
				args[key] = arg
			}
			var tc agent.ToolCall
			raw, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("call-%d", n), "name": name, "args": args})
			if err := json.Unmarshal(raw, &tc); err != nil {
				return agent.StubTurn{}, nil, err
			}
			return agent.StubTurn{ToolCalls: []agent.ToolCall{tc}}, u, nil
		}
		p.mu.Lock()
		p.final = c.Text
		p.mu.Unlock()
	}
	return agent.StubTurn{Text: c.Text}, u, nil
}

func (p *provider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	t, u, err := p.call(ctx, req)
	if err != nil {
		return nil, err
	}
	return &agent.ProviderResponse{Text: t.Text, ToolCalls: t.ToolCalls, Usage: u}, nil
}

func (p *provider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	t, u, err := p.call(ctx, req)
	if err != nil {
		return nil, err
	}
	var ds []agent.Delta
	if t.Text != "" {
		ds = append(ds, agent.Delta{Kind: agent.DeltaText, Text: t.Text})
	}
	for i, tc := range t.ToolCalls {
		ds = append(ds, agent.Delta{Kind: agent.DeltaToolCallStart, Index: i, ToolCallID: tc.ID, ToolName: tc.Name},
			agent.Delta{Kind: agent.DeltaToolCallArgs, Index: i, Text: string(tc.Args.Raw())})
	}
	if u != nil {
		ds = append(ds, agent.Delta{Kind: agent.DeltaUsage, Usage: u})
	}
	reason := "stop"
	if len(t.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	ds = append(ds, agent.Delta{Kind: agent.DeltaFinish, FinishReason: reason})
	return agent.NewStubProvider(agent.StubTurn{Deltas: ds}).Stream(ctx, req)
}

// TextAction reads a reply as nl's text actions are read.
func TextAction(r string) (action, arg string, ok bool) {
	first, rest, _ := strings.Cut(r, "\n")
	word, inline, _ := strings.Cut(strings.TrimSpace(first), " ")
	switch word {
	case nl.ActionLook, nl.ActionValues, nl.ActionFind, nl.ActionAdd:
		return word, strings.TrimSpace(inline), rest == "" && strings.TrimSpace(inline) != ""
	case nl.ActionDrop:
		return word, "", rest == "" && strings.TrimSpace(inline) == ""
	case nl.ActionTry:
		d := strings.TrimSpace(strings.TrimSpace(inline) + "\n" + rest)
		return word, d, d != ""
	}
	return "", "", false
}

// Translate is what nl.Translate was, on a Translator: m is the model, fallback, when given, the
// fallback, and g, when not nil, the Grounder. o is the options as given, its Pack the Translator's.
func Translate(ctx context.Context, q string, v *ns.Vocabulary, g nl.Grounder, m Model, o nl.Options, fallback ...Model) (nl.Program, nl.Trace, error) {
	cfg := translator.Config{Model: translator.Role{Provider: Provider(m), Label: m.Name()}, Grounder: g, Options: func(p *nl.Options) { *p = o }}
	if len(fallback) > 0 && fallback[0] != nil {
		cfg.Fallback = translator.Role{Provider: Provider(fallback[0]), Label: fallback[0].Name()}
	}
	tr, err := translator.New(v, o.Pack, cfg)
	if err != nil {
		return nl.Program{}, nl.Trace{}, err
	}
	return tr.Ask(ctx, q)
}

// Extract is what nl.Extract was, on a Translator: m is the model, fallback, when given, the fallback.
func Extract(ctx context.Context, text string, v *ns.Vocabulary, m Model, o nl.ExtractOptions, fallback ...Model) (nl.Extraction, nl.Trace, error) {
	cfg := translator.Config{Model: translator.Role{Provider: Provider(m), Label: m.Name()}}
	if len(fallback) > 0 && fallback[0] != nil {
		cfg.Fallback = translator.Role{Provider: Provider(fallback[0]), Label: fallback[0].Name()}
	}
	tr, err := translator.New(v, o.Pack, cfg)
	if err != nil {
		return nl.Extraction{}, nl.Trace{}, err
	}
	return tr.Extract(ctx, text, o)
}
