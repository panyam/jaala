// Package memory holds a long chat history as jaala versions, for chakra's LongMemEval harness
// (#415). Each session is read into facts and events by nl's extraction and committed as the next
// version, so a later statement replaces an earlier one while the earlier version still holds it.
//
// A Memory is a chakra longmemeval.SessionMemory, so chakra's MemoryArm grades an agent answering
// with its tools. OneShotArm grades the other way: the translator writes one program for the question,
// and the agent phrases its rows.
package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval/longmemeval"
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/translator"
	"github.com/panyam/jaala/ns"
)

// Config says which models a Memory asks.
type Config struct {
	// Extract reads each session into facts. Its Provider is required. Its Label names the model in
	// the usage a Memory reports, so set it to the model's name for chakra's Suite.Prices to price it.
	Extract translator.Role
	// Ask writes a program for a question, for Memory.Ask and OneShotArm. Leave its Provider nil for a
	// memory only an agent's tools read.
	Ask translator.Role
	// MaxTries is how many drafts each model gets per session or question; nl's default when 0.
	MaxTries int
	// Prompts replaces the text the memory shows a model. Its zero fields keep DefaultPrompts'.
	Prompts Prompts
}

// A Memory is one history as jaala versions: version 0 is empty and each ingested session adds the
// next. It is for one history and one goroutine at a time, as chakra's MemoryArm uses it: Ingest
// commits to the store, which no Eval may be reading then. Its tools may run concurrently once
// ingestion is done.
type Memory struct {
	src      *ns.TimedMemSource
	v        *ns.Vocabulary
	base     *datalog.Base
	pack     *nl.Pack
	extract  *translator.Translator
	ask      *translator.Translator
	tries    int
	prompts  Prompts
	at       ns.Version
	sessions map[string]string
}

var _ longmemeval.SessionMemory = (*Memory)(nil)

// New builds an empty Memory.
func New(cfg Config) (*Memory, error) {
	src := ns.NewTimedMemSource(ns.Span(0, 1))
	v, err := vocabulary(src)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	m := &Memory{src: src, v: v, base: b, pack: Pack(), tries: cfg.MaxTries,
		prompts: cfg.Prompts.withDefaults(), sessions: map[string]string{}}
	if m.extract, err = translator.New(v, m.pack, translator.Config{Model: cfg.Extract, Base: b}); err != nil {
		return nil, fmt.Errorf("memory: extract: %w", err)
	}
	if cfg.Ask.Provider != nil {
		if m.ask, err = translator.New(v, m.pack, translator.Config{Model: cfg.Ask, Base: b}); err != nil {
			return nil, fmt.Errorf("memory: ask: %w", err)
		}
	}
	return m, nil
}

// Ingest reads one session into facts and events and commits them, with the session's own row, as
// the next version. It reports the extraction's tokens by model label, also when it fails. A session
// whose extraction is refused commits nothing, and the error says why.
func (m *Memory) Ingest(ctx context.Context, s longmemeval.Session) (map[string]agent.Usage, error) {
	text := sessionText(s)
	x, tr, err := m.extract.Extract(ctx, text, nl.ExtractOptions{Base: m.base, Pack: m.pack, MaxTries: m.tries})
	spent := usageOf(tr)
	if err != nil {
		return spent, fmt.Errorf("memory: session %s: %w", s.ID, err)
	}
	c := cited(x.Changes, s.ID, text)
	c.Add(RelSession, ns.Tuple{Vals: []ns.Value{ns.S(s.ID), ns.S(s.Date)}, Cites: []string{"session:" + s.ID}})
	if err := m.src.Commit(c, m.at, m.at+1); err != nil {
		return spent, fmt.Errorf("memory: session %s: %w", s.ID, err)
	}
	m.at++
	m.sessions[s.ID] = text
	return spent, nil
}

// Version is the latest version: how many sessions were ingested.
func (m *Memory) Version() ns.Version { return m.at }

// Base reads the memory. A plain Eval reads the latest version, and datalog.At(v) an earlier one.
func (m *Memory) Base() *datalog.Base { return m.base }

// An Answer is a program's rows over the memory, each with the facts it came from.
type Answer struct {
	Program string
	Columns []string
	Rows    [][]string
	Cites   [][]string
}

// String writes the answer as the program, then one line per row with its citations.
func (a Answer) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "program: %s\ncolumns: %s\n", a.Program, strings.Join(a.Columns, ", "))
	if len(a.Rows) == 0 {
		b.WriteString("no rows\n")
	}
	for i, r := range a.Rows {
		fmt.Fprintf(&b, "%s", strings.Join(r, " | "))
		if len(a.Cites[i]) > 0 {
			fmt.Fprintf(&b, "   (from: %s)", strings.Join(a.Cites[i], "; "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ErrNoAskModel is Ask's error when Config.Ask has no Provider.
var ErrNoAskModel = errors.New("memory: Config.Ask has no Provider")

// Ask has the Ask model write a program for the question and runs it over the latest version. It
// reports the model's tokens by label, also when it fails.
func (m *Memory) Ask(ctx context.Context, question string) (Answer, map[string]agent.Usage, error) {
	if m.ask == nil {
		return Answer{}, nil, ErrNoAskModel
	}
	p, tr, err := m.ask.Ask(ctx, question)
	spent := usageOf(tr)
	if err != nil {
		return Answer{}, spent, fmt.Errorf("memory: ask: %w", err)
	}
	a, err := m.run(ctx, p.Query, p.Text)
	return a, spent, err
}

// run evaluates q over the latest version.
func (m *Memory) run(ctx context.Context, q datalog.Query, text string) (Answer, error) {
	tab, err := datalog.SemiNaive{}.EvalTable(ctx, q, m.base)
	if err != nil {
		return Answer{}, err
	}
	a := Answer{Program: text}
	for _, c := range tab.Columns {
		a.Columns = append(a.Columns, string(c))
	}
	for i, row := range tab.Rows {
		vals := make([]string, len(row))
		for j, v := range row {
			vals[j] = v.S
		}
		a.Rows = append(a.Rows, vals)
		a.Cites = append(a.Cites, tab.Cites[i])
	}
	return a, nil
}

// Tools are what an agent answers with: query runs a program it writes over the latest version, and
// read_session gives a session's text, for what the facts left out. Neither calls a model, so the
// agent's own usage is the whole cost of answering.
func (m *Memory) Tools() agent.ToolSource {
	src := agent.NewFuncSource()
	card, err := nl.Card(m.v)
	if err != nil {
		card = "(no card: " + err.Error() + ")"
	}
	type programArg struct {
		Program string `json:"program" jsonschema:"description=A program in jaala's Datalog"`
	}
	type sessionArg struct {
		ID string `json:"id" jsonschema:"description=A session's id, as the session relation holds it"`
	}
	// AddFunc only fails on a repeated name or an argument type it can't describe, neither of which
	// these two can have.
	_ = agent.AddFunc(src, "query", m.prompts.Query+"\n\n"+card, func(ctx context.Context, in programArg) (string, error) {
		q, err := datalog.Parse(in.Program)
		if err != nil {
			return nl.Explain(in.Program, err), nil
		}
		a, err := m.run(ctx, q, in.Program)
		if err != nil {
			return nl.Explain(in.Program, err), nil
		}
		return a.String(), nil
	}, agent.ReadOnly())
	_ = agent.AddFunc(src, "read_session", m.prompts.ReadSession, func(_ context.Context, in sessionArg) (string, error) {
		text, ok := m.sessions[in.ID]
		if !ok {
			return fmt.Sprintf("no session %q", in.ID), nil
		}
		return text, nil
	}, agent.ReadOnly())
	return src
}

// sessionText is a session as the extraction model reads it: a header with its id and date, then
// each turn as "role: content".
func sessionText(s longmemeval.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s, %s\n", s.ID, s.Date)
	for _, t := range s.Turns {
		fmt.Fprintf(&b, "%s: %s\n", t.Role, t.Content)
	}
	return b.String()
}

// cited is c with each "text:start-end" citation an extraction writes replaced by the session's id
// and the words at those offsets, so a row read across sessions says which one said it. It drops any
// session lines the model wrote, since Ingest writes the session's own row.
func cited(c ns.Changes, session, text string) ns.Changes {
	var out ns.Changes
	for _, rel := range c.Relations() {
		if rel == RelSession {
			continue
		}
		for _, t := range c.Removed(rel) {
			out.Remove(rel, t)
		}
		for _, t := range c.Added(rel) {
			cites := make([]string, len(t.Cites))
			for i, ct := range t.Cites {
				cites[i] = ct
				var start, end int
				if _, err := fmt.Sscanf(ct, "text:%d-%d", &start, &end); err == nil && 0 <= start && start <= end && end <= len(text) {
					cites[i] = fmt.Sprintf("%s: %q", session, text[start:end])
				}
			}
			t.Cites = cites
			out.Add(rel, t)
		}
	}
	return out
}

// usageOf adds up a trace's tokens by the model label each try and step names.
func usageOf(tr nl.Trace) map[string]agent.Usage {
	var out map[string]agent.Usage
	add := func(model string, in, outTok, cached int) {
		if in == 0 && outTok == 0 && cached == 0 {
			return
		}
		if out == nil {
			out = map[string]agent.Usage{}
		}
		u := out[model]
		u.InputTokens += in
		u.OutputTokens += outTok
		u.CachedInputTokens += cached
		out[model] = u
	}
	for _, t := range tr.Tries {
		add(t.Model, t.TokensIn, t.TokensOut, t.TokensCached)
	}
	for _, s := range tr.Steps {
		add(s.Model, s.TokensIn, s.TokensOut, s.TokensCached)
	}
	for _, t := range tr.Votes {
		add(t.Model, t.TokensIn, t.TokensOut, t.TokensCached)
	}
	return out
}

// addUsage adds src's usage into dst, by model, and returns dst.
func addUsage(dst, src map[string]agent.Usage) map[string]agent.Usage {
	for k, u := range src {
		if dst == nil {
			dst = map[string]agent.Usage{}
		}
		d := dst[k]
		d.InputTokens += u.InputTokens
		d.OutputTokens += u.OutputTokens
		d.CachedInputTokens += u.CachedInputTokens
		dst[k] = d
	}
	return dst
}
