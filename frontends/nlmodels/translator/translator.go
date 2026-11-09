// Package translator is jaala's translator entry point (#490): a host hands it its vocabulary, its
// nl.Pack and the chakra providers to ask, and asks questions. How the translator talks to each model
// (temperature, token limit, grammar, a thinking model's prompt) is jaala's, so every host gets the
// same tuning without rediscovering it. The host keeps its words (the pack) and its choice of models,
// which it can resolve by name through chakra's connections config.
package translator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// The defaults a Role gets when it leaves a field unset.
const (
	// ModelTemperature is the model's: greedy, so its drafts repeat and a vote's differ only by seed.
	ModelTemperature = 0.0
	// FallbackTemperature is the fallback's: it is asked after the model failed, so it samples.
	FallbackTemperature = 0.7
	// MaxTokens caps a reply, and ThinkingMaxTokens a thinking model's, which writes its reasoning
	// before the program.
	MaxTokens         = 512
	ThinkingMaxTokens = 3072
)

// A Role is one model the translator asks: the provider and how to ask it.
type Role struct {
	// Provider answers the prompts. The grammar and a vote's seed are sent only when
	// agent.CapabilitiesOf says it can take them.
	Provider agent.Provider
	// Label names the model in the Trace and Program.Model; "model" or "fallback" when empty.
	Label string
	// Temperature replaces the role's default (ModelTemperature, FallbackTemperature). It is sent only
	// to a provider whose agent.CapabilitiesOf says it takes one, so an Anthropic role needs no setting.
	Temperature *float64
	// MaxTokens replaces the role's default (MaxTokens, or ThinkingMaxTokens when Thinking).
	MaxTokens int
	// Penalties turn on chakra's sampling penalties for the role's calls (#517): repeat, frequency,
	// presence and DRY, for a model that repeats itself until its token limit
	// (find(interfaceinterfaceinterface(...). As with Temperature, only the kinds agent.CapabilitiesOf
	// lists for the provider are sent, and the rest are left off, so one Role serves llama.cpp (all
	// four kinds), Ollama (frequency and presence) and Anthropic (none). nil sends none.
	Penalties *agent.SamplingPenalties
	// Thinking is for a reasoning model: no grammar, which would hold the reply to a program from its
	// first token, a line asking it to think the question through and put the program in a fenced
	// block, and only the last fenced block, outside any <think> section, read as the draft.
	Thinking bool
	// ThinkPrompt replaces DefaultThinkPrompt, the line Thinking adds to the prefix, word for word
	// (#485). Like DefaultThinkPrompt it should ask for the program in a fenced block, which is what
	// is read as the draft.
	ThinkPrompt string
	// TextActions is for a model that can't call tools (#509), such as Qwen2.5-Coder behind
	// llama-server, which writes a tool call as JSON in its text. Such a role is offered no tools.
	// Its prefix lists the actions, and it takes one by writing it as its reply (look NAME, values
	// TARGET, try then a draft, find NAME). Each reply is held to a grammar of an action or a draft
	// when the provider takes one. After its last step, its replies are held to the draft's grammar
	// alone. It has no effect without Config.Actions.
	TextActions bool
}

// Config is what a host decides per deployment. Everything else is jaala's default.
type Config struct {
	// Model is asked first. Its Provider is required.
	Model Role
	// Fallback, when its Provider is set, is asked once Model runs out of tries, repeats a draft it
	// was told was wrong, or writes a program answering nothing over Base. It starts a fresh
	// conversation rather than reading Model's.
	Fallback Role
	// Grounder, when set, finds the values a question's names stand for before any model is asked,
	// in place of the pack's Grounder.
	Grounder nl.Grounder
	// Base evaluates a draft that checks, so an empty answer goes back to the model; without it a
	// program that checks is accepted.
	Base *datalog.Base
	// Actions, Vote, Escalate, Abstain and Budget are nl.Options' own, passed through.
	Actions  *nl.Actions
	Vote     *nl.Vote
	Escalate float64
	Abstain  *nl.Abstain
	Budget   nl.Budget
	// Tiers, when set, is the default tier policy (#538): Model is the local tier and Fallback the
	// frontier one. Translate's options can set their own.
	Tiers *nl.Tiers
	// Options, when set, edits the nl.Options Ask uses after everything above is filled in, for what
	// Config doesn't name (a front end, a goal-only grammar, Eval options). What it sets wins.
	Options func(*nl.Options)
}

// A Translator answers questions over one vocabulary and pack with the models a Config names. It is
// safe for concurrent use as long as its providers are.
type Translator struct {
	v               *ns.Vocabulary
	opts            nl.Options
	grounder        nl.Grounder
	model, fallback *role
}

// New is a Translator over v and pack. It fails when cfg names no Model provider. pack may be nil.
func New(v *ns.Vocabulary, pack *nl.Pack, cfg Config) (*Translator, error) {
	if cfg.Model.Provider == nil {
		return nil, errors.New("translator: Config.Model needs a Provider")
	}
	t := &Translator{v: v, model: cfg.Model.runtime("model", ModelTemperature), grounder: cfg.Grounder}
	o := nl.Options{Pack: pack, Base: cfg.Base, Actions: cfg.Actions, Vote: cfg.Vote, Escalate: cfg.Escalate, Abstain: cfg.Abstain, Budget: cfg.Budget, Tiers: cfg.Tiers}
	if cfg.Fallback.Provider != nil {
		t.fallback = cfg.Fallback.runtime("fallback", FallbackTemperature)
	}
	if cfg.Options != nil {
		cfg.Options(&o)
	}
	t.opts = o
	return t, nil
}

// Ask translates question on chakra Runners (#496): the pack's grounder, examples and checks, the
// model and then the fallback, each a turn whose replies an nl.Session judges, with the Config's
// actions as tools, its vote as an agent.Consensus and its budget as the turn's. The Trace holds
// every try, also when Ask fails, and Abstain turns what its policy covers into an *nl.Abstained.
func (t *Translator) Ask(ctx context.Context, question string) (nl.Program, nl.Trace, error) {
	return t.Translate(ctx, question, t.opts, true, t.fallback != nil)
}

// Translate is Ask under options o in place of the Config's, asking the model when model is set and
// the fallback when fallback is, in that order. It is what frontends/mcp's ask tool calls
// (mcp.Translator): the server writes o for each call (its Base, Eval options, pack, front end and
// Abstain) and picks the models from the call's mode. The Config's roles and their tuning still
// apply. Asking for a fallback the Config names none of asks the model alone, and asking for neither
// fails. With o.Tiers set, its Policy picks the models instead and model and fallback are ignored:
// Model is the local tier and Fallback the frontier one, which a policy asking for it needs.
func (t *Translator) Translate(ctx context.Context, question string, o nl.Options, model, fallback bool) (nl.Program, nl.Trace, error) {
	if o.Actions != nil {
		a := *o.Actions
		a.AsTools = true
		o.Actions = &a
	}
	if o.Tiers != nil {
		return t.tiered(ctx, question, o)
	}
	var roles []*role
	if model {
		roles = append(roles, t.model)
	}
	if fallback && t.fallback != nil {
		roles = append(roles, t.fallback)
	}
	if len(roles) == 0 {
		return nl.Program{}, nl.Trace{}, errors.New("translator: Translate asked no model")
	}
	p, tr, err := t.run(ctx, question, o, roles, nil)
	return o.Abstain.Judge(p, tr, err)
}

// tiered runs o.Tiers' policy (#538): the local tier under its own cap, judged by o.Abstain, then,
// under local-then-frontier when it didn't answer, the frontier tier from its closest draft. A
// cancelled context ends the question at the local tier.
func (t *Translator) tiered(ctx context.Context, question string, o nl.Options) (nl.Program, nl.Trace, error) {
	pol := *o.Tiers
	if err := pol.Check(); err != nil {
		return nl.Program{}, nl.Trace{}, err
	}
	if pol.Policy != nl.TierLocal && t.fallback == nil {
		return nl.Program{}, nl.Trace{}, fmt.Errorf("translator: tier policy %q needs a Config.Fallback, the frontier tier", pol.Policy)
	}
	var tr nl.Trace
	var handoff *nl.Try
	if pol.Policy != nl.TierFrontier {
		lo := o
		lo.Budget = pol.For(nl.TierLocal, o.Budget)
		p, ltr, err := t.run(ctx, question, lo, []*role{t.model}, nil)
		p, ltr, err = o.Abstain.Judge(p, ltr, err)
		ltr.Stamp(nl.TierLocal)
		tr = ltr
		if err == nil {
			tr.Tier = nl.TierLocal
			return p, tr, nil
		}
		if pol.Policy == nl.TierLocal || ctx.Err() != nil {
			return p, tr, err
		}
		if d, ok := nl.HandoffDraft(ltr, err); ok {
			handoff = &d
		}
	}
	fo := o
	fo.Budget = pol.For(nl.TierFrontier, o.Budget)
	p, ftr, err := t.run(ctx, question, fo, []*role{t.fallback}, handoff)
	ftr.Stamp(nl.TierFrontier)
	if err == nil {
		ftr.Tier = nl.TierFrontier
	}
	tr.Then(ftr)
	return o.Abstain.Judge(p, tr, err)
}

// HasFallback says the Config names a Fallback.
func (t *Translator) HasFallback() bool { return t.fallback != nil }

// DefaultThinkPrompt is what a thinking model's prefix gains, unless its Role's ThinkPrompt replaces it.
const DefaultThinkPrompt = `
# Before you write the program, think it through: which things the question names and their ids, which relations connect them, which variables join them, and what the answer's columns are. Then write the program alone in a fenced block.
`

var (
	thought = regexp.MustCompile(`(?s)<think>.*?</think>`)
	fenced  = regexp.MustCompile("(?s)```[a-zA-Z]*\n(.*?)```")
)

// programIn is the last fenced block of reply outside its <think> sections, or the rest of the reply
// when it has none. The last, since a model reasoning in the open drafts before it answers.
func programIn(reply string) string {
	reply = thought.ReplaceAllString(reply, "")
	if bs := fenced.FindAllStringSubmatch(reply, -1); len(bs) > 0 {
		return strings.TrimSpace(bs[len(bs)-1][1])
	}
	return strings.TrimSpace(reply)
}
