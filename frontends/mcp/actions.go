package mcp

import (
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/mcpkit/core"
)

// The look, values and find tools are nl.Translate's actions (#393) offered to an agent, so a small
// model's loop and an agent's calls say the same things in the same words. A try is query or cypher
// with sample: run on a few rows, saying why when none come back.

const lookDesc = `Describe one relation, module or (with language cypher) label of %s: its signature and whole doc, or a module's members. Cheaper than schema with no path when you know the name.`

type lookInput struct {
	Name     string `json:"name" jsonschema:"required" jsonschema_description:"A relation or module, or with language cypher a node label or relationship type."`
	Language string `json:"language,omitempty" jsonschema_description:"datalog (the default), or cypher to read the name as the cypher tool's graph does."`
}

func (c *call) look(_ core.ToolContext, in lookInput) (core.ToolResult, error) {
	fe, err := c.frontEnd(in.Language)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	text, err := nl.Look(c.v, fe, in.Name)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	return core.TextResult(strings.TrimRight(text, "\n") + "\n"), nil
}

const valuesDesc = `Some of the values one argument of a relation holds in %s, distinct, as rel.arg (by name, or by position from 0), or with language cypher Label.property. Look before guessing a kind, a key or an id's spelling: a program with a value the data doesn't hold answers nothing.`

type valuesInput struct {
	Target   string `json:"target" jsonschema:"required" jsonschema_description:"rel.arg, such as pkg.license or pkg.1, or with language cypher Label.property."`
	Language string `json:"language,omitempty" jsonschema_description:"datalog (the default), or cypher to name a label's property."`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"How many values, 20 by default; the server caps it too."`
	JSON     bool   `json:"json,omitempty" jsonschema_description:"Answer with the columns, rows and the program it ran as JSON instead of text."`
}

// valuesLimit is how many values the values tool answers when a call doesn't say.
const valuesLimit = 20

func (c *call) values(ctx core.ToolContext, in valuesInput) (core.ToolResult, error) {
	fe, err := c.frontEnd(in.Language)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	n := in.Limit
	if n <= 0 {
		n = valuesLimit
	}
	n = c.limit(n)
	probe, err := nl.Probe(c.v, fe, in.Target, n)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if fe != nil {
		return c.cypher(ctx, cypherInput{Query: probe, Limit: n, JSON: in.JSON, bare: true})
	}
	return c.query(ctx, queryInput{Program: probe, Limit: n, JSON: in.JSON, bare: true})
}

const findDesc = `Find what a name from the question may stand for in %s: the ids or values it matches, likeliest first, to use exactly as written in a program.`

type findInput struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"A name, as the question says it: a function, a package, a person."`
}

func (c *call) find(ctx core.ToolContext, in findInput) (core.ToolResult, error) {
	if c.p.Grounder == nil {
		return core.ErrorResult("this call finds no names; values says what an argument holds"), nil
	}
	cands, err := c.p.Grounder.Ground(ctx, in.Name)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	var b strings.Builder
	for _, cand := range cands {
		quoted := make([]string, len(cand.Values))
		for i, v := range cand.Values {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, "%s: %s\n", cand.Name, strings.Join(quoted, ", "))
	}
	if b.Len() == 0 {
		return core.TextResult(fmt.Sprintf("nothing found for %q\n", in.Name)), nil
	}
	return core.TextResult(b.String()), nil
}

// frontEnd is the front end a look or values call speaks: nil for datalog, Cypher's over the call's
// graph for cypher.
func (c *call) frontEnd(language string) (nl.FrontEnd, error) {
	switch language {
	case "", "datalog":
		return nil, nil
	case "cypher":
		if c.p.Cypher == nil {
			return nil, fmt.Errorf("this call reads no Cypher graph; use language datalog")
		}
		return cypher.NewFrontEnd(*c.p.Cypher), nil
	}
	return nil, fmt.Errorf("language %q: want datalog or cypher", language)
}

// sampled is q with its own limit lowered to sample, so evaluation stops after that many rows, when a
// call asks for a sample.
func sampled(q datalog.Query, sample int) datalog.Query {
	if sample > 0 && (q.Limit == 0 || q.Limit > sample) {
		q.Limit = sample
	}
	return q
}

// whyEmpty is, for a call that answered nothing and was explained into r (a sampled one), why: the
// literal that matched nothing, as nl.Explain writes an empty answer for a model, and "" otherwise.
func whyEmpty(rows int, r *datalog.Report, explain func(error) string) string {
	if rows > 0 || r == nil {
		return ""
	}
	return strings.TrimRight(explain(&nl.EmptyAnswer{Why: r.Empty}), "\n") + "\n"
}
