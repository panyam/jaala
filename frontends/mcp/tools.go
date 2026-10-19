package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/core"
)

type tools struct {
	v     *ns.Vocabulary // the vocabulary, once a call has resolved it (see readsVocabulary)
	vocab *vocabulary
	p     Pack
	o     Options
}

const queryDesc = `Run a Datalog program over %s and get its rows, each with the facts it was derived from. Read the schema first. Results are capped by limit (continue with page_token) and max_work; an error says what went wrong and often how to fix it, so fix the program and call again.`

type queryInput struct {
	Program   string `json:"program" jsonschema:"required" jsonschema_description:"A Datalog program: rules, if any, then a goal with =>."`
	Limit     int    `json:"limit,omitempty" jsonschema_description:"The most rows to return; the server caps it too."`
	PageToken string `json:"page_token,omitempty" jsonschema_description:"The page_token the last answer gave, to continue it."`
	MaxWork   int64  `json:"max_work,omitempty" jsonschema_description:"Stop past this much work; the server caps it too."`
	JSON      bool   `json:"json,omitempty" jsonschema_description:"Answer with the columns, rows and witnesses as JSON instead of text."`
}

func (c *call) query(ctx core.ToolContext, in queryInput) (core.ToolResult, error) {
	offset, err := pageOffset(in.PageToken)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	q, err := datalog.Parse(in.Program)
	if err != nil {
		return c.refuse(in.Program, err), nil
	}
	b, err := c.base(ctx)
	if err != nil {
		return core.ErrorResult(err.Error() + c.budgetHint(err)), nil
	}
	tab, err := datalog.SemiNaive{}.EvalTable(ctx, q, b, c.evalOptions(in.MaxWork, datalog.Witnesses())...)
	if err != nil {
		return c.refuse(in.Program, err), nil
	}
	return c.answer(page(in.Program, tab, offset, c.limit(in.Limit)), in.JSON)
}

// page is the rows of tab from offset, at most limit of them.
func page(program string, tab *datalog.Table, offset, limit int) Answer {
	a := Answer{Program: program, Table: tab, Offset: min(offset, tab.Len())}
	for _, c := range tab.Columns {
		a.Columns = append(a.Columns, string(c))
	}
	a.Len = min(limit, tab.Len()-a.Offset)
	if end := a.Offset + a.Len; end < tab.Len() {
		a.Next = strconv.Itoa(end)
	}
	return a
}

func (t *tools) limit(asked int) int {
	if asked > 0 && asked < t.o.Limit {
		return asked
	}
	return t.o.Limit
}

// base is the Base the call reads: its Run's, or else the one Options.Base picks.
func (c *call) base(ctx context.Context) (*datalog.Base, error) {
	if c.run.Base != nil {
		return c.run.Base, nil
	}
	return c.o.Base(ctx)
}

// evalOptions are the server's options, then the Run's, with a budget of asked capped by
// Options.MaxWork, then extra.
func (c *call) evalOptions(asked int64, extra ...datalog.Option) []datalog.Option {
	opts := append(append([]datalog.Option{}, c.o.Eval...), c.run.Eval...)
	budget := c.o.MaxWork
	if asked > 0 && (budget == 0 || asked < budget) {
		budget = asked
	}
	if budget > 0 {
		opts = append(opts, datalog.Budget(budget))
	}
	return append(opts, extra...)
}

// refuse is err as a tool error an agent fixes the program from: nl.Explain's message, place and fix,
// and for a budget or the call's time, how far the server lets it go.
func (c *call) refuse(program string, err error) core.ToolResult {
	return core.ErrorResult(nl.Explain(program, err) + c.budgetHint(err))
}

// budgetHint is, for a spent budget, what to do about it and how far the server lets max_work go,
// and for a call stopped at its timeout (#361), how long the server gives one.
func (c *call) budgetHint(err error) string {
	if c.timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("\nThis server stops a call after %s. Ask something narrower, bind more of its arguments, or add a limit.\n", c.timeout)
	}
	t := c.tools
	var be *datalog.BudgetExceeded
	if !errors.As(err, &be) {
		return ""
	}
	text := "\nAsk something narrower, bind more of its arguments, or add a limit"
	if t.o.MaxWork == 0 || be.Limit < t.o.MaxWork {
		text += ", or raise max_work"
		if t.o.MaxWork > 0 {
			text += fmt.Sprintf(" (up to %d)", t.o.MaxWork)
		}
	}
	return text + ".\n"
}

func (t *tools) answer(a Answer, asJSON bool) (core.ToolResult, error) {
	text := formatAnswer(a)
	if t.p.Format != nil {
		text = t.p.Format(a)
	}
	if !asJSON {
		return core.TextResult(text), nil
	}
	data, err := json.Marshal(answerJSON(a))
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.StructuredResult(string(data), json.RawMessage(data)), nil
}

// maxWitness is the most witness lines the default format writes per row.
const maxWitness = 8

// formatAnswer is the default text of a page: its columns, each row numbered with the facts it was
// derived from, and how many rows there are and how to continue.
func formatAnswer(a Answer) string {
	var b strings.Builder
	if a.Cypher != "" {
		fmt.Fprintf(&b, "ran: %s\n", strings.Join(strings.Fields(a.Program), " "))
	}
	fmt.Fprintf(&b, "columns: %s\n", strings.Join(a.Columns, ", "))
	for i := a.Offset; i < a.Offset+a.Len; i++ {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(valuesText(a.Table.Rows[i]), ", "))
		if a.Table.Witness != nil {
			lines := 0
			for _, w := range a.Table.Witness[i] {
				writeWitness(&b, w, 1, &lines)
			}
			if lines > maxWitness {
				fmt.Fprintf(&b, "   … %d more facts\n", lines-maxWitness)
			}
		}
	}
	switch {
	case a.Table.Len() == 0:
		b.WriteString("no rows\n")
	case a.Next != "":
		fmt.Fprintf(&b, "rows %d to %d of %d; continue with page_token %q\n", a.Offset+1, a.Offset+a.Len, a.Table.Len(), a.Next)
	default:
		b.WriteString(rowCount(a.Table.Len()) + "\n")
	}
	return b.String()
}

// writeWitness writes w and its children indented by depth, counting lines and writing only the first
// maxWitness.
func writeWitness(b *strings.Builder, w *datalog.Witness, depth int, lines *int) {
	*lines++
	if *lines <= maxWitness {
		line := fmt.Sprintf("%s(%s)", w.Relation, strings.Join(quoted(w.Values), ", "))
		if w.Negated {
			line = "not " + line
		}
		if len(w.Cites) > 0 {
			line += "  [" + strings.Join(w.Cites, ", ") + "]"
		}
		fmt.Fprintf(b, "%s%s\n", strings.Repeat("   ", depth), line)
	}
	for _, c := range w.Children {
		writeWitness(b, c, depth+1, lines)
	}
}

type witnessJSON struct {
	Relation string        `json:"relation"`
	Values   []string      `json:"values"`
	Negated  bool          `json:"negated,omitempty"`
	Rule     string        `json:"rule,omitempty"`
	Cites    []string      `json:"cites,omitempty"`
	Children []witnessJSON `json:"children,omitempty"`
}

type pageJSON struct {
	Cypher    string          `json:"cypher,omitempty"`
	Program   string          `json:"program,omitempty"`
	Columns   []string        `json:"columns"`
	Rows      [][]string      `json:"rows"`
	Witness   [][]witnessJSON `json:"witness,omitempty"`
	Total     int             `json:"total"`
	PageToken string          `json:"page_token,omitempty"`
}

func answerJSON(a Answer) pageJSON {
	out := pageJSON{Rows: [][]string{}, Columns: a.Columns, Total: a.Table.Len(), PageToken: a.Next}
	if a.Cypher != "" {
		out.Cypher, out.Program = a.Cypher, a.Program
	}
	for i := a.Offset; i < a.Offset+a.Len; i++ {
		out.Rows = append(out.Rows, valuesText(a.Table.Rows[i]))
		if a.Table.Witness != nil {
			var ws []witnessJSON
			for _, w := range a.Table.Witness[i] {
				ws = append(ws, witnessOf(w))
			}
			out.Witness = append(out.Witness, ws)
		}
	}
	return out
}

func witnessOf(w *datalog.Witness) witnessJSON {
	out := witnessJSON{Relation: w.Relation, Values: valuesText(w.Values), Negated: w.Negated, Rule: w.Rule, Cites: w.Cites}
	for _, c := range w.Children {
		out.Children = append(out.Children, witnessOf(c))
	}
	return out
}

// valuesText is each value as an answer writes it: its text with its unit, or "absent".
func valuesText(vals []ns.Value) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		switch {
		case v.Absent:
			out[i] = "absent"
		case v.Num != nil && v.BaseUnit != "" && !strings.HasSuffix(v.S, v.BaseUnit):
			out[i] = v.S + v.BaseUnit
		default:
			out[i] = v.S
		}
	}
	return out
}

// quoted is values as a program writes them in a witness: text quoted, numbers and absent bare.
func quoted(vals []ns.Value) []string {
	out := valuesText(vals)
	for i, v := range vals {
		if !v.Absent && v.Num == nil {
			out[i] = strconv.Quote(v.S)
		}
	}
	return out
}

const schemaDesc = `What a program over %s can call. With no path, every relation, predicate and module, one line each with its signature and what it holds; with a module's path, its members; with a relation's, its signature, doc and rules; with "data", what the data holds; with "cypher", the graph the cypher tool reads, when it is offered.`

type schemaInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"A module or relation, \"data\" for what the data holds, \"cypher\" for the graph Cypher reads, or empty for everything."`
}

func (c *call) schema(_ core.ToolContext, in schemaInput) (core.ToolResult, error) {
	text, err := c.describe(in.Path)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	return core.TextResult(text), nil
}

// describe is the card, the card's lines under a module, a member's whole entry, or the pack's Card.
func (t *tools) describe(path string) (string, error) {
	card, err := nl.Card(t.v)
	if err != nil {
		return "", err
	}
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return card, nil
	case path == "data":
		if strings.TrimSpace(t.p.Card) == "" {
			return "", errors.New("this server says nothing about its data beyond the schema")
		}
		return t.p.Card, nil
	case path == "cypher":
		if t.p.Cypher == nil {
			return "", errors.New("this server reads no Cypher")
		}
		return t.p.Cypher.Describe() + "\n", nil
	}
	e, err := t.v.Lookup(path)
	if err != nil {
		return "", err
	}
	if e.Kind == ns.EntryModule {
		var b strings.Builder
		fmt.Fprintf(&b, "module %s\n", path)
		for _, line := range strings.Split(card, "\n") {
			if strings.HasPrefix(line, path+".") {
				b.WriteString(line + "\n")
			}
		}
		return b.String(), nil
	}
	var b strings.Builder
	for _, line := range strings.Split(card, "\n") {
		if strings.HasPrefix(line, path+"(") {
			b.WriteString(line + "\n")
		}
	}
	if doc := strings.TrimSpace(e.Doc); doc != "" {
		b.WriteString("\n" + doc + "\n")
	}
	if len(e.Definition) > 0 {
		b.WriteString("\ndefined by:\n")
		for _, d := range e.Definition {
			b.WriteString("  " + d + "\n")
		}
	}
	return b.String(), nil
}

const explainDesc = `Run a Datalog program and say how it went instead of its rows: the work each rule and literal did, and, when it answers nothing, the literal that matched nothing, what it was called with, and whether it matches anything alone (a wrong value, rather than a wrong join).`

type explainInput struct {
	Program string `json:"program" jsonschema:"required" jsonschema_description:"A Datalog program: rules, if any, then a goal with =>."`
	MaxWork int64  `json:"max_work,omitempty" jsonschema_description:"Stop past this much work; the server caps it too."`
}

func (c *call) explain(ctx core.ToolContext, in explainInput) (core.ToolResult, error) {
	q, err := datalog.Parse(in.Program)
	if err != nil {
		return c.refuse(in.Program, err), nil
	}
	b, err := c.base(ctx)
	if err != nil {
		return core.ErrorResult(err.Error() + c.budgetHint(err)), nil
	}
	var r datalog.Report
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, b, c.evalOptions(in.MaxWork, datalog.Explain(&r))...)
	if err != nil {
		return c.refuse(in.Program, err), nil
	}
	return core.TextResult(rowCount(len(rows)) + "\n" + r.String()), nil
}

const askDesc = `Ask a question about %s in plain language: the server writes a Datalog program, checks and runs it, and answers with the program, its rows and each try it took. mode picks the models: local (the small one alone), frontier (the large one alone) or both (the small one, then the large one when it fails). Prefer query when you can write the program.`

type askInput struct {
	Question string `json:"question" jsonschema:"required" jsonschema_description:"The question, in plain language."`
	Mode     string `json:"mode,omitempty" jsonschema_description:"local, frontier or both (the default)."`
	MaxWork  int64  `json:"max_work,omitempty" jsonschema_description:"Stop each draft past this much work; the server caps it too."`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"The most rows to return; the server caps it too."`
	JSON     bool   `json:"json,omitempty" jsonschema_description:"Answer with the columns, rows and witnesses as JSON instead of text."`
}

func (c *call) ask(ctx core.ToolContext, in askInput) (core.ToolResult, error) {
	model, fallback := c.o.Model, c.o.Fallback
	switch in.Mode {
	case "", "both":
	case "local":
		fallback = nil
	case "frontier":
		if fallback == nil {
			return core.ErrorResult("this server has no frontier model; ask with mode local or both"), nil
		}
		model, fallback = fallback, nil
	default:
		return core.ErrorResult(fmt.Sprintf("mode %q: want local, frontier or both", in.Mode)), nil
	}
	b, err := c.base(ctx)
	if err != nil {
		return core.ErrorResult(err.Error() + c.budgetHint(err)), nil
	}
	p, tr, err := nl.Translate(ctx, in.Question, c.v, nil, model, nl.Options{
		Fallback: fallback, Base: b, Eval: c.evalOptions(in.MaxWork), Pack: &c.p.Pack,
	})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("no program answered it: %v\n%s", err, traceText(tr))), nil
	}
	tab, err := datalog.SemiNaive{}.EvalTable(ctx, p.Query, b, c.evalOptions(in.MaxWork, datalog.Witnesses())...)
	if err != nil {
		return c.refuse(p.Text, err), nil
	}
	res, err := c.answer(page(p.Text, tab, 0, c.limit(in.Limit)), in.JSON)
	if err != nil || in.JSON {
		return res, err
	}
	return core.TextResult(fmt.Sprintf("program:\n  %s\n%s%s", p.Text, res.Content[0].Text, traceText(tr))), nil
}

// traceText is a line per try: the model, why it was refused, and its tokens.
func traceText(tr nl.Trace) string {
	var b strings.Builder
	b.WriteString("tries:\n")
	for i, try := range tr.Tries {
		kind := try.Kind
		if try.Err == nil {
			kind = "accepted"
		}
		fmt.Fprintf(&b, "  %d. %s: %s, %d tokens in, %d out\n", i+1, try.Model, kind, try.TokensIn, try.TokensOut)
	}
	return b.String()
}

// rowCount is n rows in words: "1 row", "3 rows".
func rowCount(n int) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}
