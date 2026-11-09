package translator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/mcpkit/core"
)

// A role is a Role as the loop runs it: the provider, the generation settings it can take, and
// whether its answer is held to the grammar.
type role struct {
	label    string
	provider agent.Provider
	gen      agent.GenerationParams
	think    string // the line a thinking model's prefix gains, "" for any other
	guide    bool   // the provider takes a GBNF grammar and the model isn't thinking
	text     bool   // the model writes its actions as its reply (Role.TextActions)
}

// runtime is r with the role's defaults where r leaves them.
func (r Role) runtime(label string, temp float64) *role {
	if r.Label != "" {
		label = r.Label
	}
	caps := agent.CapabilitiesOf(r.Provider)
	rr := &role{label: label, provider: r.Provider, gen: agent.GenerationParams{MaxTokens: MaxTokens}, text: r.TextActions}
	if caps.Temperature {
		if r.Temperature != nil {
			temp = *r.Temperature
		}
		rr.gen.Temperature = &temp
	}
	rr.gen.Penalties = sendable(r.Penalties, caps)
	switch {
	case r.MaxTokens > 0:
		rr.gen.MaxTokens = r.MaxTokens
	case r.Thinking:
		rr.gen.MaxTokens = ThinkingMaxTokens
	}
	if r.Thinking {
		rr.think = r.ThinkPrompt
		if rr.think == "" {
			rr.think = DefaultThinkPrompt
		}
	}
	rr.guide = !r.Thinking && caps.CanGuide(&agent.GuidedDecoding{Grammar: "root ::= \"x\""})
	return rr
}

// sendable is p with only the kinds of penalty caps lists, nil when none is left.
func sendable(p *agent.SamplingPenalties, caps agent.Capabilities) *agent.SamplingPenalties {
	if p == nil {
		return nil
	}
	var out agent.SamplingPenalties
	can := func(kind string) bool { return slices.Contains(caps.Penalties, kind) }
	if can(agent.PenaltyRepeat) {
		out.Repeat, out.RepeatLastN = p.Repeat, p.RepeatLastN
	}
	if can(agent.PenaltyFrequency) {
		out.Frequency = p.Frequency
	}
	if can(agent.PenaltyPresence) {
		out.Presence = p.Presence
	}
	if can(agent.PenaltyDRY) {
		out.DRYMultiplier, out.DRYBase, out.DRYAllowedLength = p.DRYMultiplier, p.DRYBase, p.DRYAllowedLength
	}
	if out == (agent.SamplingPenalties{}) {
		return nil
	}
	return &out
}

// guided is the grammar r's answers are held to, nil when it takes none or there is none.
func (r *role) guided(grammar string) *agent.GuidedDecoding {
	if !r.guide || grammar == "" {
		return nil
	}
	return &agent.GuidedDecoding{Grammar: grammar}
}

// answer is the draft in a reply: a thinking model's last fenced block, or the reply.
func (r *role) answer(text string) string {
	if r.think != "" {
		return programIn(text)
	}
	return text
}

// errLowAgreement ends a model's turn whose answer its vote disputed (nl.Options.Escalate).
var errLowAgreement = errors.New("the drafts agreed too little on an answer")

// run is one question: an nl.Session's pieces on chakra Runners (#496). It is what nl.Translate's loop was,
// with chakra doing the asking: a turn per model, each reply judged by the session's Conversation as
// a TurnEnd hook, the actions as tools, a vote as an agent.Consensus, and the fallback a turn of its
// own over a fresh conversation.
func (t *Translator) run(ctx context.Context, question string, o nl.Options, roles []*role) (nl.Program, nl.Trace, error) {
	l := &loop{t: t, o: o, start: time.Now()}
	if o.Budget.Tokens > 0 {
		ctx = agent.WithTreeBudget(ctx, agent.TreeBudget{MaxTokens: o.Budget.Tokens})
	}
	l.parent = ctx
	if o.Budget.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Budget.Time)
		defer cancel()
	}
	s, err := nl.NewSession(ctx, question, t.v, t.grounder, o)
	if err != nil {
		return nl.Program{}, l.tr, err
	}
	first := roles[0]
	var last error
	var held *nl.Program // an answer escalated past, kept in case nothing better comes
	pick, err := s.Pick()
	if err != nil {
		return nl.Program{}, l.tr, err
	}
	if pick != nil {
		p, history, ok, err := l.pick(ctx, pick, first)
		if err != nil {
			return nl.Program{}, l.tr, err
		}
		if ok {
			p.Confidence = nl.Confidence{Drawn: 1, Agreed: 1, Ran: o.Base != nil, Judged: o.Pack.Check != nil}
			if o.Vote != nil {
				if p, err = l.vote(ctx, pick, first, history, p); err != nil {
					return nl.Program{}, l.tr, err
				}
			}
			if !(o.Escalate > 0 && p.Confidence.Agreement() < o.Escalate) {
				return p, l.tr, nil
			}
			l.tr.Escalations = append(l.tr.Escalations, nl.Escalation{Model: first.label, Text: p.Text, Confidence: p.Confidence})
			held = &p
		}
	}
	for k, r := range roles {
		c := s.NewConversation(last, k == len(roles)-1)
		p, history, err := l.turn(ctx, s, c, r)
		if err != nil {
			if !errors.Is(err, errTurnOver) {
				return nl.Program{}, l.tr, err
			}
			last = l.stop
			continue
		}
		p.Confidence = nl.Confidence{Drawn: 1, Agreed: 1, Ran: o.Base != nil, Judged: o.Pack != nil && o.Pack.Check != nil, Steps: c.Steps()}
		if o.Vote != nil {
			if p, err = l.vote(ctx, s, r, history, p); err != nil {
				return nl.Program{}, l.tr, err
			}
		}
		if o.Escalate > 0 && p.Confidence.Agreement() < o.Escalate && k < len(roles)-1 {
			l.tr.Escalations = append(l.tr.Escalations, nl.Escalation{Model: r.label, Text: p.Text, Confidence: p.Confidence})
			if held == nil {
				held = &p
			}
			last = errLowAgreement
			continue
		}
		return p, l.tr, nil
	}
	if held != nil {
		return *held, l.tr, nil
	}
	return nl.Program{}, l.tr, fmt.Errorf("%w after %d tries: %w", nl.ErrNoProgram, len(l.tr.Tries), last)
}

// errTurnOver is a model's turn ending without an answer; loop.stop says why.
var errTurnOver = errors.New("translator: the turn ended without an answer")

// A loop is one question's run: the trace it builds and what it has spent.
type loop struct {
	t      *Translator
	o      nl.Options      // the question's options
	parent context.Context // the caller's context, under the token budget but not the time one
	start  time.Time
	mu     sync.Mutex // tools may run concurrently with each other
	tr     nl.Trace
	stop   error // why the last turn ended without an answer

	shown *agent.Message // the preview block the retrieval stage put before the question, nil for none

	recorded agent.Usage // the part of the current turn's usage already on a Try or Step
	pending  agent.Usage // the last step's tokens, not yet on a Step
	mark     time.Time   // when the last Try or Step was recorded, or the turn began
}

// turn asks r for an answer in conversation c: one Runner turn with the actions as tools, or read
// from its replies for a TextActions role, ended by the Conversation's verdicts. It returns the accepted program and the history it was asked with,
// errTurnOver when the turn ended without one (l.stop says why), or an error that ends the question.
func (l *loop) turn(ctx context.Context, s *nl.Session, c *nl.Conversation, r *role) (nl.Program, []agent.Message, error) {
	var accepted *nl.Program
	l.stop = nil
	prefix, grammar := s.Prefix(), s.Grammar()
	if r.text {
		prefix, grammar = s.TextActions()
	}
	cfg := agent.RunnerConfig{
		Provider:        r.provider,
		Instructions:    prefix + r.think,
		MaxSteps:        s.MaxTries() + s.MaxSteps() + 1,
		MaxFreeRefusals: nl.MaxRepeats,
		Generation:      r.gen,
		ResponseGuided:  r.guided(grammar),
		TurnEnd: []agent.TurnEndHook{func(ctx context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
			if err := l.timeUp(ctx); err != nil {
				return agent.TurnEndVerdict{}, err
			}
			if result, repeat, ok := l.textAction(ctx, s, c, r, info); ok {
				return agent.TurnEndVerdict{Continue: result, AsUser: true, Free: repeat}, nil
			}
			vd := c.Judge(ctx, l.try(r, r.answer(info.Text), info.Usage))
			l.tr.Tries = append(l.tr.Tries, vd.Tries...)
			switch {
			case vd.Program != nil:
				accepted = vd.Program
				return agent.TurnEndVerdict{}, nil
			case vd.Stop != nil:
				l.stop = vd.Stop
				return agent.TurnEndVerdict{Stop: vd.Stop.Error()}, nil
			}
			return agent.TurnEndVerdict{Continue: vd.Continue, AsUser: true}, nil
		}},
		StepStages: l.stages(s, c, r),
	}
	if tools := s.Tools(); tools != nil && !r.text {
		cfg.Tools = l.tools(tools, c, r)
		cfg.ToolMiddleware = []agent.ToolMiddleware{l.repeated(c, r)}
	}
	l.shown = nil
	history := []agent.Message{{Role: agent.RoleUser, Text: c.Ask()}}
	res, err := l.runTurn(ctx, cfg, r, "", agent.TurnRequest{History: history})
	if err != nil {
		return nl.Program{}, nil, err
	}
	if accepted == nil {
		return nl.Program{}, nil, errTurnOver
	}
	if l.o.Actions != nil && l.o.Actions.Compact {
		var asked []agent.Message
		for _, t := range c.Compact() {
			asked = append(asked, agent.Message{Role: agent.RoleUser, Text: t.Text})
		}
		return *accepted, l.withPreview(asked), nil
	}
	return *accepted, l.withPreview(answered(append(history, res.Messages...))), nil
}

// withPreview is history with the preview block before it, as the turn's model saw it, so a vote's
// draws see it too: the retrieval stage keeps it out of the turn's record.
func (l *loop) withPreview(history []agent.Message) []agent.Message {
	if l.shown == nil {
		return history
	}
	return append([]agent.Message{*l.shown}, history...)
}

// runTurn runs one turn on a Runner over cfg, counting its tokens. A turn the Conversation stopped,
// or that ran out of steps, is errTurnOver; a failed model call is a Try of kind model and
// errTurnOver too; the budget running out is an *nl.BudgetSpent; anything else ends the question.
func (l *loop) runTurn(ctx context.Context, cfg agent.RunnerConfig, r *role, mode string, req agent.TurnRequest) (*agent.TurnResult, error) {
	runner, err := agent.NewRunner(cfg)
	if err != nil {
		return nil, err
	}
	l.recorded, l.pending, l.mark = agent.Usage{}, agent.Usage{}, time.Now()
	req.Emit = l.emit
	res, err := runner.RunTurn(ctx, req)
	switch {
	case err == nil:
		return res, nil
	case errors.Is(err, agent.ErrTurnStopped):
		return res, errTurnOver
	case errors.Is(err, agent.ErrMaxSteps):
		if l.stop == nil {
			l.stop = err
		}
		return res, errTurnOver
	case errors.Is(err, agent.ErrTreeBudget), l.parent.Err() == nil && ctx.Err() != nil:
		return res, l.spentAll()
	case ctx.Err() != nil:
		return res, ctx.Err()
	}
	l.stop = fmt.Errorf("model %s: %w", r.label, err)
	try := nl.Try{Model: r.label, Err: err, Kind: nl.KindModel, Mode: mode, Latency: time.Since(l.mark)}
	if mode == "" {
		try.Language = l.language()
	} else {
		try.Err = l.stop
	}
	l.tr.Tries = append(l.tr.Tries, try)
	return res, errTurnOver
}

// spentAll is the BudgetSpent the question ends with: the tokens every Try, Step and vote carried.
func (l *loop) spentAll() *nl.BudgetSpent {
	n := 0
	for _, ts := range [][]nl.Try{l.tr.Tries, l.tr.Votes} {
		for _, t := range ts {
			n += t.TokensIn + t.TokensOut
		}
	}
	for _, st := range l.tr.Steps {
		n += st.TokensIn + st.TokensOut
	}
	return &nl.BudgetSpent{Budget: l.o.Budget, Tokens: n, Elapsed: time.Since(l.start)}
}

// timeUp is ctx's error once it is done, nil before: the caller cancelled or the time budget ran
// out, and nothing more is asked or judged, as nl.Translate did before each call. runTurn
// tells the two apart by the caller's context.
func (l *loop) timeUp(ctx context.Context) error { return ctx.Err() }

// language is the front end's language, "" for jaala's.
func (l *loop) language() string {
	if fe := l.o.FrontEnd; fe != nil {
		return fe.Language()
	}
	return ""
}

// emit keeps each step's tokens until a Step or Try takes them.
func (l *loop) emit(e agent.Event) {
	if e.Kind == agent.EventStep && e.Usage != nil {
		l.mu.Lock()
		l.pending = add(l.pending, *e.Usage)
		l.mu.Unlock()
	}
}

// try is the reply text as a Try from r, carrying the tokens the turn spent since the last Try or
// Step (the reply's own call, and a guided call that rewrote it).
func (l *loop) try(r *role, text string, turn agent.Usage) nl.Try {
	l.mu.Lock()
	defer l.mu.Unlock()
	u := sub(turn, l.recorded)
	l.recorded, l.pending = turn, agent.Usage{}
	now := time.Now()
	try := nl.Try{Model: r.label, Text: text, TokensIn: u.InputTokens, TokensOut: u.OutputTokens, TokensCached: u.CachedInputTokens, Latency: now.Sub(l.mark)}
	l.mark = now
	return try
}

// stages name each call for the turn's usage, watch for a model repeating its actions (stuck), stop
// offering the actions once the model has taken MaxSteps of them (for a TextActions role, by holding
// the call to the draft's grammar, a try line allowed), and send Compact's one message in place of the conversation when Actions.Compact
// asks for it. Compacting is a turn stage, since it rewrites the working messages, which chakra's
// guided finalizing call reads too; a request stage's messages reach only the step's own call.
func (l *loop) stages(s *nl.Session, c *nl.Conversation, r *role) []agent.StepStage {
	st := []agent.StepStage{{Name: "jaala.translator", Scope: agent.ScopeRequest, Run: func(ctx context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
		if err := l.timeUp(ctx); err != nil {
			return v, err
		}
		v.ProviderName = r.label
		l.mu.Lock()
		out := c.Steps() >= s.MaxSteps()
		l.mu.Unlock()
		if out {
			v.Tools = nil
			if r.text {
				v.Guided = r.guided(s.FinalGrammar())
			}
		}
		return v, nil
	}}}
	if l.o.Actions != nil {
		st = append([]agent.StepStage{l.stuck(s, c, r)}, st...)
	}
	if p := l.o.Pack; p != nil && p.Preview != nil && l.o.Base != nil {
		st = append(st, l.preview(s))
	}
	if l.o.Actions != nil && l.o.Actions.Compact {
		st = append(st, agent.StepStage{Name: "jaala.translator.compact", Scope: agent.ScopeTurn, Run: func(_ context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			v.Messages = nil
			for _, t := range c.Compact() {
				v.Messages = append(v.Messages, agent.Message{Role: agent.RoleUser, Text: t.Text})
			}
			v.Messages = l.withPreview(v.Messages)
			return v, nil
		}})
	}
	if l.o.Actions != nil && l.o.Actions.Ledger {
		st = append(st, agent.StepStage{Name: "jaala.translator.ledger", Scope: agent.ScopeRequest, Run: func(_ context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			l.mu.Lock()
			ledger := c.Ledger()
			l.mu.Unlock()
			if ledger != "" {
				v.Messages = append(slices.Clip(v.Messages), agent.Message{Role: agent.RoleSystem, Text: ledger})
			}
			return v, nil
		}})
	}
	return st
}

// preview is chakra's retrieval stage over the session's Preview (#514): before the turn's first
// call it puts a block of the pack's samples before the question, the pack's first target ranked
// first, and keeps the block out of the turn's record. The block is kept in l.shown, so Compact's
// stage, which rewrites the messages on every call, puts it back, and a vote's history carries it.
// It goes as the user's, since a chat template may refuse a system message after the first.
func (l *loop) preview(s *nl.Session) agent.StepStage {
	inner := agent.NewRetrievalStage(agent.RetrievalConfig{AsUser: true, Retrievers: []agent.Retriever{
		agent.NewRetriever("jaala.preview", func(ctx context.Context, _ agent.RetrievalQuery) ([]agent.Snippet, error) {
			l.mu.Lock()
			samples, err := s.Preview(ctx)
			l.mu.Unlock()
			if len(samples) == 0 {
				return nil, err
			}
			out := make([]agent.Snippet, len(samples))
			for i, sm := range samples {
				out[i] = agent.Snippet{Source: sm.Target, Text: sm.Text, Score: float64(len(samples) - i)}
			}
			return out, nil
		})}})
	return agent.StepStage{Name: "jaala.translator.preview", Scope: agent.ScopeTurn, Run: func(ctx context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
		before := slices.Clone(v.Messages)
		v, err := inner.Run(ctx, info, v)
		if err != nil || len(v.Messages) == len(before) {
			return v, err
		}
		for i, m := range v.Messages {
			if i == len(before) || m.Role != before[i].Role || m.Text != before[i].Text {
				l.mu.Lock()
				l.shown = &m
				l.mu.Unlock()
				break
			}
		}
		return v, nil
	}}
}

// stuck is chakra's stuck detector over the turn's actions (#512): the same action twice in a row, or
// two taking turns twice, with Prompts.Stuck as the note and, where the provider takes one, the
// fallback's temperature for that call, since a greedy model given the same input writes the same
// reply. If the model is still stuck at the next call, its actions end, and the stage after this one
// holds it to its answer. It runs first, so that stage sees the steps spent.
func (l *loop) stuck(s *nl.Session, c *nl.Conversation, r *role) agent.StepStage {
	warm := FallbackTemperature
	nudge := agent.StuckGeneration(agent.GenerationParams{Temperature: &warm})
	return agent.NewStuckDetector(agent.StuckConfig{
		Key:     func(rec agent.StepRecord) (agent.StuckKey, bool) { return actionKey(r, rec) },
		Repeats: 2, Alternations: 2, Errors: -1,
		Policy: func(ctx context.Context, info agent.StepInfo, v agent.StepView, f agent.StuckFinding) (agent.StepView, error) {
			if f.Count > 2 {
				l.mu.Lock()
				c.EndActions()
				l.mu.Unlock()
				return v, nil
			}
			v.Messages = append(slices.Clip(v.Messages), agent.Message{Role: agent.RoleSystem, Text: s.StuckNote(f.String())})
			if r.gen.Temperature != nil {
				return nudge(ctx, info, v, f)
			}
			return v, nil
		},
	})
}

// actionKey keys a step by the action it took: a tool call's, or for a TextActions role the action
// its reply wrote. An answer is no action and breaks a pattern. The result needs no key of its own:
// over one question's Base, the same action answers the same.
func actionKey(r *role, rec agent.StepRecord) (agent.StuckKey, bool) {
	if tcs := rec.Assistant.ToolCalls; len(tcs) > 0 {
		var k agent.StuckKey
		for _, tc := range tcs {
			k.Call += nl.ActionText(tc.Name, toolArg(tc)) + "\x00"
			k.Tools = append(k.Tools, tc.Name)
		}
		return k, true
	}
	if !r.text {
		return agent.StuckKey{}, false
	}
	action, arg, ok := nl.ReadAction(r.answer(rec.Assistant.Text))
	if !ok {
		return agent.StuckKey{}, false
	}
	// Tools names the action so the note says "repeated try"; no policy here drops tools
	return agent.StuckKey{Call: nl.ActionText(action, arg), Tools: []string{action}}, true
}

// The arguments of the action tools, one text each.
type (
	nameArg struct {
		Name string `json:"name"`
	}
	targetArg struct {
		Target string `json:"target"`
	}
	draftArg struct {
		Draft string `json:"draft"`
	}
)

// tools are the session's actions as chakra tools, each a Step in the Trace.
func (l *loop) tools(ts []nl.Tool, c *nl.Conversation, r *role) agent.ToolSource {
	src := agent.NewFuncSource()
	act := func(ctx context.Context, action, arg string) string { return l.toolAct(ctx, c, r, action, arg) }
	for _, t := range ts {
		var err error
		switch t.Action {
		case nl.ActionValues:
			err = agent.AddFunc(src, t.Action, t.Description, func(ctx context.Context, in targetArg) (string, error) {
				return act(ctx, nl.ActionValues, in.Target), nil
			}, agent.ReadOnly())
		case nl.ActionTry:
			err = agent.AddFunc(src, t.Action, t.Description, func(ctx context.Context, in draftArg) (string, error) {
				return act(ctx, nl.ActionTry, in.Draft), nil
			}, agent.ReadOnly())
		default:
			action := t.Action
			err = agent.AddFunc(src, action, t.Description, func(ctx context.Context, in nameArg) (string, error) {
				return act(ctx, action, in.Name), nil
			}, agent.ReadOnly())
		}
		if err != nil {
			panic("translator: registering " + t.Action + ": " + err.Error()) // the names are fixed and distinct
		}
	}
	return src
}

// toolAct runs an action a tool call asked for, charging its Step the last call's tokens.
func (l *loop) toolAct(ctx context.Context, c *nl.Conversation, r *role, action, arg string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	u := l.pending
	l.recorded, l.pending = add(l.recorded, u), agent.Usage{}
	return l.act(ctx, c, r, action, arg, u)
}

// repeated answers a tool call that repeats an action the conversation took as a free refusal
// (#517): the Conversation answers it from before and takes no step, so chakra's Runner takes none
// either while the turn is under MaxFreeRefusals. Under a step budget a model cycling try, look, try,
// find ran out of steps with nothing new run. The model reads the earlier result after chakra's
// denial prefix. Any other call goes on to the tool.
func (l *loop) repeated(c *nl.Conversation, r *role) agent.ToolMiddleware {
	return func(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
		action, arg := info.Call.Name, toolArg(info.Call)
		l.mu.Lock()
		repeat := c.Repeats(action, arg)
		l.mu.Unlock()
		if !repeat {
			return next(ctx, info)
		}
		result := l.toolAct(ctx, c, r, action, arg)
		return nil, &agent.ToolDeniedError{Reason: "repeated " + action, ModelReason: result, Free: true}
	}
}

// textAction runs the action a TextActions role's reply asks for while it has steps left, and returns
// what goes back to it, and whether it repeated one the conversation took, which costs no step
// (#517). ok is false for any other reply, which is judged as an answer. The step is charged what the
// turn spent since the last Try or Step.
func (l *loop) textAction(ctx context.Context, s *nl.Session, c *nl.Conversation, r *role, info agent.TurnEndInfo) (result string, repeat, ok bool) {
	if !r.text || l.o.Actions == nil {
		return "", false, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c.Steps() >= s.MaxSteps() {
		return "", false, false
	}
	action, arg, ok := nl.ReadAction(r.answer(info.Text))
	if !ok {
		return "", false, false
	}
	u := sub(info.Usage, l.recorded)
	l.recorded, l.pending = info.Usage, agent.Usage{}
	repeat = c.Repeats(action, arg)
	return l.act(ctx, c, r, action, arg, u), repeat, true
}

// act runs an action c's model asked for and records it as a Step costing u. l.mu is held.
func (l *loop) act(ctx context.Context, c *nl.Conversation, r *role, action, arg string, u agent.Usage) string {
	result := c.Act(ctx, action, arg)
	now := time.Now()
	l.tr.Steps = append(l.tr.Steps, nl.Step{Model: r.label, Action: action, Text: nl.ActionText(action, arg), Result: result, After: len(l.tr.Tries),
		TokensIn: u.InputTokens, TokensOut: u.OutputTokens, TokensCached: u.CachedInputTokens, Latency: now.Sub(l.mark)})
	l.mark = now
	return result
}

// answered is what a vote's draws are asked: the turn's record up to the accepted answer, each tool
// call written as the reply that would have asked for it and each result as the user's next turn, so
// a draw needs no tools offered (a provider may refuse tool messages in a request offering none).
func answered(record []agent.Message) []agent.Message {
	if n := len(record); n > 0 && record[n-1].Role == agent.RoleAssistant && len(record[n-1].ToolCalls) == 0 {
		record = record[:n-1]
	}
	var out []agent.Message
	for _, m := range record {
		switch {
		case m.Role == agent.RoleAssistant && len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				out = append(out, agent.Message{Role: agent.RoleAssistant, Text: nl.ActionText(tc.Name, toolArg(tc))})
			}
		case m.Role == agent.RoleTool:
			out = append(out, agent.Message{Role: agent.RoleUser, Text: m.Text})
		default:
			out = append(out, agent.Message{Role: m.Role, Text: m.Text})
		}
	}
	return out
}

// toolArg is a tool call's one text argument, "" when it has none.
func toolArg(tc agent.ToolCall) string {
	var args map[string]any
	if json.Unmarshal(tc.Args.Raw(), &args) != nil {
		return ""
	}
	for _, v := range args {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// vote draws more answers to what first answered, from history, as nl's Options.Vote does: an
// agent.Consensus with first as its prior, each draw one guided call with no tools, judged and keyed
// by the session. It returns the program most draws agree with, first among equals, with Confidence's
// Drawn and Agreed from the vote, and adds each draw to the Trace's Votes.
func (l *loop) vote(ctx context.Context, s *nl.Session, r *role, history []agent.Message, first nl.Program) (nl.Program, error) {
	vt := l.o.Vote
	if vt.Total() <= 1 || l.overBudget() {
		// nothing to draw, or no budget left: chakra's tree budget stops a draw only after its call
		return first, nil
	}
	runner, err := agent.NewRunner(agent.RunnerConfig{
		Provider:       r.provider,
		Instructions:   s.Prefix() + r.think,
		MaxSteps:       1,
		Generation:     r.gen,
		ResponseGuided: r.guided(s.Grammar()),
		StepStages: []agent.StepStage{{Name: "jaala.translator.vote", Scope: agent.ScopeRequest, Run: func(ctx context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			if err := l.timeUp(ctx); err != nil {
				return v, err
			}
			v.ProviderName = r.label
			return v, nil
		}}},
	})
	if err != nil {
		return nl.Program{}, err
	}
	prior := &agent.TurnResult{Text: first.Text}
	var mu sync.Mutex
	drawn := map[*agent.TurnResult]struct {
		try  nl.Try
		prog nl.Program
	}{}
	parallel := 1
	if vt.Parallel {
		parallel = vt.Total() - 1
	}
	cons := agent.Consensus{Runner: runner, Draws: vt.Total() - 1, Prior: []*agent.TurnResult{prior}, Parallel: parallel,
		Key: func(res *agent.TurnResult) (string, bool) {
			if res == prior {
				return s.Key(first), true
			}
			u := res.Usage
			try, p, err := s.Draw(ctx, nl.Try{Model: r.label, Text: r.answer(res.Text), TokensIn: u.InputTokens, TokensOut: u.OutputTokens, TokensCached: u.CachedInputTokens})
			mu.Lock()
			drawn[res] = struct {
				try  nl.Try
				prog nl.Program
			}{try, p}
			mu.Unlock()
			if err != nil {
				return "", false
			}
			return s.Key(p), true
		}}
	res, err := cons.Run(ctx, agent.TurnRequest{History: history})
	if err != nil {
		return nl.Program{}, err
	}
	won := first
	for _, d := range res.Draws {
		if d.Prior {
			continue
		}
		switch {
		case d.Result != nil:
			l.tr.Votes = append(l.tr.Votes, drawn[d.Result].try)
		case d.Err != "" && (res.Stopped == nil || d.Err != res.Stopped.Error()):
			l.tr.Votes = append(l.tr.Votes, nl.Try{Model: r.label, Language: l.language(), Err: errors.New(d.Err), Kind: nl.KindModel})
		}
	}
	if res.Winner != nil && res.Winner != prior {
		won = drawn[res.Winner].prog
	}
	won.Confidence = first.Confidence
	won.Confidence.Drawn, won.Confidence.Agreed = 1+len(l.drawnIn(res)), res.Agree
	return won, nil
}

// overBudget says the token budget is spent by the calls in the trace so far.
func (l *loop) overBudget() bool {
	b := l.o.Budget.Tokens
	return b > 0 && l.spentAll().Tokens >= b
}

// drawnIn are the vote's draws that were asked, the failed included and the unstarted left out, as
// nl's vote counts them.
func (l *loop) drawnIn(res *agent.ConsensusResult) []agent.DrawResult {
	var out []agent.DrawResult
	for _, d := range res.Draws {
		if !d.Prior && (d.Result != nil || d.Err != "" && (res.Stopped == nil || d.Err != res.Stopped.Error())) {
			out = append(out, d)
		}
	}
	return out
}

// pick asks r to pick one of the pack's Callables (#484) first: a turn over
// the pick's Session, judged by its Conversation. ok is false when the model replied none, failed, or
// had no pick accepted, and the question is then drafted.
func (l *loop) pick(ctx context.Context, pick *nl.Session, r *role) (nl.Program, []agent.Message, bool, error) {
	c := pick.NewConversation(nil, true)
	var accepted *nl.Program
	cfg := agent.RunnerConfig{
		Provider:       r.provider,
		Instructions:   pick.Prefix() + r.think,
		MaxSteps:       pick.MaxTries() + 1,
		Generation:     r.gen,
		ResponseGuided: r.guided(pick.Grammar()),
		TurnEnd: []agent.TurnEndHook{func(ctx context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
			if err := l.timeUp(ctx); err != nil {
				return agent.TurnEndVerdict{}, err
			}
			try := l.try(r, r.answer(info.Text), info.Usage)
			vd := c.Judge(ctx, try)
			switch {
			case errors.Is(vd.Stop, nl.ErrNoCall):
				l.tr.Steps = append(l.tr.Steps, nl.Step{Model: r.label, Action: nl.ActionNone, Text: try.Text, Result: "no relation answers it: drafting a program",
					After: len(l.tr.Tries), TokensIn: try.TokensIn, TokensOut: try.TokensOut, TokensCached: try.TokensCached, Latency: try.Latency})
				return agent.TurnEndVerdict{Stop: vd.Stop.Error()}, nil
			case vd.Program != nil:
				l.tr.Tries = append(l.tr.Tries, vd.Tries...)
				accepted = vd.Program
				return agent.TurnEndVerdict{}, nil
			case vd.Stop != nil:
				l.tr.Tries = append(l.tr.Tries, vd.Tries...)
				return agent.TurnEndVerdict{Stop: vd.Stop.Error()}, nil
			}
			l.tr.Tries = append(l.tr.Tries, vd.Tries...)
			return agent.TurnEndVerdict{Continue: vd.Continue, AsUser: true}, nil
		}},
		StepStages: []agent.StepStage{{Name: "jaala.translator.pick", Scope: agent.ScopeRequest, Run: func(ctx context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			if err := l.timeUp(ctx); err != nil {
				return v, err
			}
			v.ProviderName = r.label
			return v, nil
		}}},
	}
	history := []agent.Message{{Role: agent.RoleUser, Text: c.Ask()}}
	res, err := l.runTurn(ctx, cfg, r, nl.ModeCall, agent.TurnRequest{History: history})
	switch {
	case errors.Is(err, errTurnOver):
		return nl.Program{}, nil, false, nil
	case err != nil:
		return nl.Program{}, nil, false, err
	case accepted == nil:
		return nl.Program{}, nil, false, nil
	}
	return *accepted, answered(append(history, res.Messages...)), true, nil
}

func add(a, b agent.Usage) agent.Usage {
	return agent.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens, CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens}
}

func sub(a, b agent.Usage) agent.Usage {
	return agent.Usage{InputTokens: a.InputTokens - b.InputTokens, OutputTokens: a.OutputTokens - b.OutputTokens, CachedInputTokens: a.CachedInputTokens - b.CachedInputTokens}
}
