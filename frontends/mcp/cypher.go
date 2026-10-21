package mcp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/core"
)

const cypherDesc = `Ask a Cypher query over %s: MATCH and OPTIONAL MATCH patterns, variable lengths and a path variable over one relationship (p = (a)-[:T*]->(b), with length, nodes and relationships), WHERE, WITH, UNWIND, lists (collect, size, IN, l[i]), and RETURN with DISTINCT, count, min, max, sum, collect, ORDER BY, SKIP and LIMIT. The answer names the program it ran and gives each row the facts it was derived from. Bind $parameters with params. Constructs it can't take yet (maps, list comprehensions, CASE, writes) come back as an error naming them, so rewrite rather than retry. %s`

type cypherInput struct {
	Query     string         `json:"query" jsonschema:"required" jsonschema_description:"A Cypher query: MATCH ... WHERE ... RETURN ..."`
	Params    map[string]any `json:"params,omitempty" jsonschema_description:"Values for the query's $parameters, by name: strings and numbers."`
	Limit     int            `json:"limit,omitempty" jsonschema_description:"The most rows to return; the server caps it too."`
	PageToken string         `json:"page_token,omitempty" jsonschema_description:"The page_token the last answer gave, to continue it."`
	MaxWork   int64          `json:"max_work,omitempty" jsonschema_description:"Stop past this much work; the server caps it too."`
	Sample    int            `json:"sample,omitempty" jsonschema_description:"Try the query on this many rows: it stops after them, and when none come back the answer points at the pattern that matched nothing."`
	JSON      bool           `json:"json,omitempty" jsonschema_description:"Answer with the columns, rows, witnesses and the program it ran as JSON instead of text."`

	bare bool // no witnesses, for the values tool
}

// cypher compiles the query over the call's graph and answers the program as query does, naming the
// columns as the query did and saying what ran.
func (c *call) cypher(ctx core.ToolContext, in cypherInput) (core.ToolResult, error) {
	if c.p.Cypher == nil {
		return core.ErrorResult("this call reads no Cypher graph; ask with the query tool"), nil
	}
	offset, err := pageOffset(in.PageToken)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	p, err := cypher.Compile(c.v, *c.p.Cypher, in.Query)
	if err != nil {
		return core.ErrorResult(cypher.Explain(in.Query, p, err) + "Or ask with the query tool.\n"), nil
	}
	values, err := paramValues(in.Params)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	var extra []datalog.Option
	if !in.bare {
		extra = append(extra, datalog.Witnesses())
	}
	var r *datalog.Report
	if in.Sample > 0 || c.p.Hint != nil {
		r = &datalog.Report{}
		extra = append(extra, datalog.Explain(r))
	}
	if len(values) > 0 {
		bind, err := p.Bind(values)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		extra = append(extra, bind)
	}
	b, err := c.base(ctx, &p.Query)
	if err != nil {
		return core.ErrorResult(err.Error() + c.budgetHint(err)), nil
	}
	tab, err := datalog.SemiNaive{}.EvalTable(ctx, sampled(p.Query, in.Sample), b, c.evalOptions(in.MaxWork, extra...)...)
	if err != nil {
		// Said in the query's terms, at its place in the query, with the program it ran for reference.
		text := cypher.Explain(in.Query, p, err) + c.budgetHint(err)
		return core.ErrorResult(text + "ran: " + strings.Join(strings.Fields(p.Text), " ") + "\n"), nil
	}
	a := page(p.Text, tab, offset, c.limit(in.Limit))
	a.Cypher = in.Query
	if len(values) > 0 {
		a.Bind = map[string][]ns.Value{}
		for name, v := range values {
			a.Bind[name] = []ns.Value{v}
		}
	}
	a.Why = c.whyEmpty(ctx, tab.Len(), r, func(err error) string { return cypher.Explain(in.Query, p, err) })
	if len(p.Columns) == len(a.Columns) {
		a.Columns = p.Columns
	}
	return c.answer(a, in.JSON)
}

// paramValues reads JSON values as a query's parameters: a string is text and a number a number.
// Anything else has no jaala value, so it is refused by name.
func paramValues(params map[string]any) (map[string]ns.Value, error) {
	out := map[string]ns.Value{}
	for name, v := range params {
		val, ok := valueOf(v)
		if !ok {
			return nil, fmt.Errorf("params: $%s is %v; a parameter is a string or a number", name, v)
		}
		out[name] = val
	}
	return out, nil
}

// values is b as an answer reports it: each variable, named without its ?, to its value, or to each
// value of a list, which it ranges over.
func (b bind) values() (map[string][]ns.Value, error) {
	out := make(map[string][]ns.Value, len(b))
	for name, v := range b {
		name = strings.TrimPrefix(name, "?")
		list, ok := v.([]any)
		if !ok {
			list = []any{v}
		}
		vals := make([]ns.Value, 0, len(list))
		for _, x := range list {
			val, ok := valueOf(x)
			if !ok {
				return nil, fmt.Errorf("bind: ?%s is %v; a variable is bound to a string, a number, or a list of them", name, x)
			}
			vals = append(vals, val)
		}
		out[name] = vals
	}
	return out, nil
}

// option is vals as datalog.Bind takes them.
func option(vals map[string][]ns.Value) datalog.Option {
	out := make(map[datalog.Var][]ns.Value, len(vals))
	for name, v := range vals {
		out[datalog.Var(name)] = v
	}
	return datalog.Bind(out)
}

// valueOf is a JSON value as jaala's: a string is text and a number a number, and nothing else is one.
func valueOf(v any) (ns.Value, bool) {
	switch x := v.(type) {
	case string:
		return ns.S(x), true
	case float64:
		return ns.N(x), true
	}
	return ns.Value{}, false
}

// pageOffset reads a page_token: "" is the start, and anything else must be an offset this server gave.
func pageOffset(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("page_token %q isn't one this server gave", token)
	}
	return n, nil
}
