package mcp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/core"
)

const cypherDesc = `Ask a Cypher query over %s: MATCH patterns, WHERE, and RETURN with DISTINCT, count, min, max, sum, collect, ORDER BY, SKIP and LIMIT. The answer names the program it ran and gives each row the facts it was derived from. Bind $parameters with params. Constructs it can't take yet (OPTIONAL MATCH, paths, UNWIND, writes) come back as an error naming them, so rewrite rather than retry. %s`

type cypherInput struct {
	Query     string         `json:"query" jsonschema:"required" jsonschema_description:"A Cypher query: MATCH ... WHERE ... RETURN ..."`
	Params    map[string]any `json:"params,omitempty" jsonschema_description:"Values for the query's $parameters, by name: strings and numbers."`
	Limit     int            `json:"limit,omitempty" jsonschema_description:"The most rows to return; the server caps it too."`
	PageToken string         `json:"page_token,omitempty" jsonschema_description:"The page_token the last answer gave, to continue it."`
	MaxWork   int64          `json:"max_work,omitempty" jsonschema_description:"Stop past this much work; the server caps it too."`
	JSON      bool           `json:"json,omitempty" jsonschema_description:"Answer with the columns, rows, witnesses and the program it ran as JSON instead of text."`
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
	extra := []datalog.Option{datalog.Witnesses()}
	if len(values) > 0 {
		bind, err := p.Bind(values)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		extra = append(extra, bind)
	}
	b, err := c.base(ctx)
	if err != nil {
		return core.ErrorResult(err.Error() + c.budgetHint(err)), nil
	}
	tab, err := datalog.SemiNaive{}.EvalTable(ctx, p.Query, b, c.evalOptions(in.MaxWork, extra...)...)
	if err != nil {
		// Said in the query's terms, at its place in the query, with the program it ran for reference.
		text := cypher.Explain(in.Query, p, err) + c.budgetHint(err)
		return core.ErrorResult(text + "ran: " + strings.Join(strings.Fields(p.Text), " ") + "\n"), nil
	}
	a := page(p.Text, tab, offset, c.limit(in.Limit))
	a.Cypher = in.Query
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
		switch x := v.(type) {
		case string:
			out[name] = ns.S(x)
		case float64:
			out[name] = ns.N(x)
		default:
			return nil, fmt.Errorf("params: $%s is %v; a parameter is a string or a number", name, v)
		}
	}
	return out, nil
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

// graphText says what a Cypher query can name: each label with its properties, each relationship type
// with its own, the properties the host computes for any node, and that any other node property is
// read from the property relation.
func graphText(g cypher.Graph) string {
	var labels, types []string
	for _, n := range g.Nodes {
		labels = append(labels, n.Label+propsText(n.Props))
	}
	for _, e := range g.Edges {
		types = append(types, e.Type+propsText(e.Props))
	}
	text := fmt.Sprintf("Node labels: %s. Relationship types: %s.", strings.Join(labels, ", "), strings.Join(types, ", "))
	if len(g.Derived) > 0 {
		text += " Any node may have" + propsText(g.Derived) + ", which the host computes."
	}
	if g.Props != nil {
		text += " A node's other properties are any key its " + g.Props.Relation + " facts name."
	}
	return text
}

func propsText[V any](props map[string]V) string {
	if len(props) == 0 {
		return ""
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	return " {" + strings.Join(names, ", ") + "}"
}
