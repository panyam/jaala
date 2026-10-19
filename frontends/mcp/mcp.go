// Package mcp serves a jaala vocabulary to agents over the Model Context Protocol (#347, mission
// #322): a server any graph tool built on jaala can run, whose tools and instructions come from the
// vocabulary and the host's Pack, so wording tuned against one host's agent helps every host. It is a
// module of its own, like frontends/nlmodels, so mcpkit's dependencies never reach jaala's core;
// transport and sessions are mcpkit's.
//
// New builds the server with four tools: query (a program in, rows with the facts they were derived
// from out), schema (the vocabulary card, a module's or relation's members and docs, and the pack's
// data card), ask (plain language through nl.Translate, when a model is configured) and explain
// (where a program's work went, and why its answer is empty). A host registers its own tools on the
// server New returns, and writes answers its own way through Pack.Format. NewWith adds a host's own
// arguments to every tool, and a hook that decides per call what they mean (#357).
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// A Pack is a host's domain as its MCP server presents it: nl.Pack's About, Card, Examples, Grounder
// and Check, which the ask tool hands to nl.Translate and the instructions are written from, plus
// what only a server needs.
type Pack struct {
	nl.Pack
	// Name and Version identify the server to a client; "jaala" and "0.0.0" when empty.
	Name, Version string
	// Instructions, when set, replaces the instructions Instructions writes, word for word, so a host
	// can try wordings against its agent (panyam/declaire#308) without changing code.
	Instructions string
	// Format, when set, writes a query's or an ask's answer instead of the default table, such as with
	// a host's own links or code snippets for what the rows cite.
	Format func(Answer) string
	// Cypher, when set, is how the vocabulary reads as a property graph, and New offers the cypher
	// tool, which compiles a Cypher query over it (frontends/cypher, #366). A Run's Pack may bring
	// another graph for its call; one without a graph refuses the call. cypher.Store writes it for
	// the node(id, kind), edge(from, kind, to), attr(id, key, value) shape.
	Cypher *cypher.Graph
}

// Options configure New.
type Options struct {
	// Base is the Base a call evaluates over, asked once per call, so a host can pick a workspace or
	// a store per caller. Required.
	Base func(ctx context.Context) (*datalog.Base, error)
	// Model, when set, offers the ask tool, which translates through it, and Fallback is the model
	// asked after it (nl.Options.Fallback). The ask tool's mode picks either alone.
	Model, Fallback nl.Model
	// Limit is the most rows a call returns, 100 when 0; a call may ask for fewer.
	Limit int
	// MaxWork caps every call's work (datalog.Budget), and a call may ask for less; 0 is no cap.
	MaxWork int64
	// Eval are further options every evaluation runs with.
	Eval []datalog.Option
	// Timeout is the longest a call may take, Prepare and Base included, after which it stops and
	// answers a tool error saying so (#361); 0 is no limit. A Run's Timeout may shorten it.
	Timeout time.Duration
	// Vocabulary, given in place of New's vocabulary, resolves it on the first call that reads it
	// (schema, ask and cypher), so a host whose vocabulary comes from its data answers initialize
	// at once rather than after opening it (#361). It is called again after it fails, and the checks
	// New would make of the pack (its examples, the card, the Cypher graph) run on the first
	// vocabulary it returns, failing that call when they fail. query and explain read the Base and
	// never wait for it.
	Vocabulary func(ctx context.Context) (*ns.Vocabulary, error)
}

// An Answer is one page of an evaluated program, as Pack.Format receives it.
type Answer struct {
	// Program is the program's text: the program a query sent, or the one a Cypher query compiled to.
	Program string
	// Cypher is the Cypher text the cypher tool compiled Program from, "" for any other tool.
	Cypher string
	// Columns are the answer's columns as the caller named them: Cypher's names (an alias, or n.name)
	// for the cypher tool, and the goal's variables otherwise.
	Columns []string
	// Table is the whole answer, every row with its witness.
	Table *datalog.Table
	// Offset and Len are the page's rows within Table.
	Offset, Len int
	// Next continues the answer from the row after the page, "" when the page is its end.
	Next string
}

// An Extension adds a host's own arguments, the fields of A, to every jaala tool (#357), such as
// the repos a query reads or how much code an answer shows. An agent sends them flat, beside the
// tool's own program and limit. A is a struct tagged as a tool's input is, with the json and
// jsonschema tags mcpkit reads, so the schema the agent sees and the values Prepare gets both come
// from it.
type Extension[A any] struct {
	// Prepare turns one call's host arguments into how the call runs. tool is the tool's name, so an
	// argument can narrow some tools and not others. An error is the call's tool error, with its
	// text. When nil, every call runs as Options and the Pack say.
	Prepare func(ctx context.Context, tool string, a A) (Run, error)
}

// A Run is how one call evaluates and answers. A field left empty is the server's.
type Run struct {
	// Base is the Base the call reads, such as one narrowed to some repos. Options.Base is asked
	// only when it is nil.
	Base *datalog.Base
	// Eval are options the call runs with after Options.Eval.
	Eval []datalog.Option
	// Pack replaces the server's pack for the call: its Card is what schema says at "data", its
	// Format writes the answer, and ask translates with it. It isn't validated as New's is. Its
	// Name, Version, Instructions, and the About the tool descriptions are written from, went to the
	// client when it connected, so a Run's aren't read.
	Pack *Pack
	// Timeout, when shorter than Options.Timeout (or that is 0), is the longest the rest of the call
	// may take.
	Timeout time.Duration
}

// New builds the server for v and p. It refuses a pack whose examples don't check against v
// (nl.Pack.Validate) or options without a Base. v may be nil when Options.Vocabulary resolves it on
// first use instead, and New refuses both or neither. It is NewWith with no host arguments, so its
// tools too refuse an argument they don't name.
func New(v *ns.Vocabulary, p Pack, o Options) (*server.Server, error) {
	return NewWith(v, p, o, Extension[struct{}]{})
}

// NewWith is New with the host's arguments A on every tool, read per call by x.Prepare. It refuses
// an A that isn't a struct, or whose json names one of a tool's own arguments, ask's included when
// there is no Model, so a name means one thing on every tool. Each tool's schema allows only the
// arguments it names, so a call with any other (a misspelling) is a tool error, never ignored.
func NewWith[A any](v *ns.Vocabulary, p Pack, o Options, x Extension[A]) (*server.Server, error) {
	if o.Base == nil {
		return nil, errors.New("mcp: Options.Base is required")
	}
	if (v == nil) == (o.Vocabulary == nil) {
		return nil, errors.New("mcp: give a vocabulary or Options.Vocabulary, one of the two")
	}
	vocab := &vocabulary{resolve: o.Vocabulary, check: func(v *ns.Vocabulary) error { return checkVocabulary(v, p) }}
	if v != nil {
		if err := vocab.check(v); err != nil {
			return nil, err
		}
		vocab.v = v
	}
	if o.Limit <= 0 {
		o.Limit = 100
	}
	name, version := p.Name, p.Version
	if name == "" {
		name = "jaala"
	}
	if version == "" {
		version = "0.0.0"
	}
	text := p.Instructions
	if text == "" {
		text = instructions(p, o.Model != nil)
	}
	host, err := objectOf(core.GenerateSchema[A]())
	if err != nil {
		return nil, fmt.Errorf("mcp: the host's arguments: %w", err)
	}
	t := &tools{vocab: vocab, p: p, o: o}
	topic := topic(p.About)
	query, qerr := tool(t, x, host, "query", fmt.Sprintf(queryDesc, topic), (*call).query)
	schema, serr := tool(t, x, host, "schema", fmt.Sprintf(schemaDesc, topic), (*call).schema)
	explain, eerr := tool(t, x, host, "explain", explainDesc, (*call).explain)
	ask, aerr := tool(t, x, host, "ask", fmt.Sprintf(askDesc, topic), (*call).ask)
	if err := errors.Join(qerr, serr, eerr, aerr); err != nil {
		return nil, err
	}
	srv := server.NewServer(core.Implementation{Name: name, Version: version}, server.WithInstructions(text))
	srv.Register(query, schema, explain)
	if o.Model != nil {
		srv.Register(ask)
	}
	if p.Cypher != nil {
		cy, err := tool(t, x, host, "cypher", fmt.Sprintf(cypherDesc, topic, p.Cypher.Describe()), (*call).cypher)
		if err != nil {
			return nil, err
		}
		srv.Register(cy)
	}
	return srv, nil
}

// checkVocabulary is what New checks of a pack against its vocabulary: that its examples check
// (nl.Pack.Validate), that the card can be written, and that its Cypher graph reads v's relations.
func checkVocabulary(v *ns.Vocabulary, p Pack) error {
	if err := p.Validate(v); err != nil {
		return err
	}
	if _, err := nl.Card(v); err != nil {
		return err
	}
	if p.Cypher != nil {
		if err := p.Cypher.Validate(v); err != nil {
			return fmt.Errorf("mcp: the pack's graph: %w", err)
		}
	}
	return nil
}

// A vocabulary is the server's, given up front or resolved by Options.Vocabulary on the first call
// that reads it, and checked once.
type vocabulary struct {
	mu      sync.Mutex
	v       *ns.Vocabulary
	resolve func(context.Context) (*ns.Vocabulary, error)
	check   func(*ns.Vocabulary) error
}

// get is the vocabulary, resolving and checking it the first time; a failure isn't kept, so the next
// call tries again.
func (s *vocabulary) get(ctx context.Context) (*ns.Vocabulary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v != nil {
		return s.v, nil
	}
	v, err := s.resolve(ctx)
	if err == nil && v == nil {
		err = errors.New("mcp: Options.Vocabulary returned no vocabulary")
	}
	if err == nil {
		err = s.check(v)
	}
	if err != nil {
		return nil, err
	}
	s.v = v
	return v, nil
}

// Instructions are what New tells a client the server is for, when the pack doesn't replace them:
// the pack's About, how to write a program, the pack's first two examples, and which tools to call
// first. ask is mentioned only when the server offers it, which takes a Model, and cypher, with the
// graph's labels and types, only when the pack has a graph.
func Instructions(p Pack, ask bool) string { return instructions(p, ask) }

func instructions(p Pack, ask bool) string {
	var b strings.Builder
	if about := strings.TrimSpace(p.About); about != "" {
		b.WriteString(about + "\n\n")
	}
	b.WriteString(`Ask with the query tool, in jaala's Datalog. A program is rules, if any, then one goal: 'rule(?a, ?b) :- body; ... body => ?a, ?b'. Variables are ?name and strings are "quoted"; there's no "?-" before a goal, and its columns follow => without parentheses. Every row carries the facts it was derived from.` + "\n\n")
	b.WriteString("Before writing a program, call schema with no path for every relation and module, and with a module's or relation's path for its members and docs.")
	if strings.TrimSpace(p.Card) != "" {
		b.WriteString(` Call schema with the path "data" for what the data holds.`)
	}
	b.WriteString("\n")
	if n := min(2, len(p.Examples)); n > 0 {
		b.WriteString("\nFor example:\n")
		for _, e := range p.Examples[:n] {
			fmt.Fprintf(&b, "  # %s\n  %s\n", e.Question, strings.Join(strings.Fields(e.Program), " "))
		}
	}
	if p.Cypher != nil {
		b.WriteString("\nOr ask in Cypher with the cypher tool: MATCH, WHERE and RETURN over this graph, and it answers with the program it ran. " + p.Cypher.Describe() + "\n")
	}
	b.WriteString("\nThe explain tool says where a program's work went, and why its answer is empty.")
	if ask {
		b.WriteString(" The ask tool takes a question in plain language and writes the program itself.")
	}
	return b.String() + "\n"
}

// AgentLine is the line a host adds to its agent's instructions (a CLAUDE.md, say) so the agent
// reaches for the server: an agent with its own tools called Declaire's on 1 of 42 questions until it
// was told in one sentence what it is for (panyam/declaire#288).
func AgentLine(p Pack) string {
	name := p.Name
	if name == "" {
		name = "jaala"
	}
	next := "query"
	if p.Cypher != nil {
		next = "query, or cypher to ask in Cypher"
	}
	return fmt.Sprintf("For questions about %s, use the %s MCP server before searching files: it answers exactly from its graph. Call its schema tool, then %s.", topic(p.About), name, next)
}

// topic is the first sentence of about without its full stop, lower-cased at the start, or "the
// server's data".
func topic(about string) string {
	s, _, _ := strings.Cut(strings.TrimSpace(about), "\n")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if s == "" {
		return "the server's data"
	}
	if r := []rune(s); len(r) > 1 && !isUpper(r[1]) {
		s = strings.ToLower(string(r[0])) + string(r[1:])
	}
	return s
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
