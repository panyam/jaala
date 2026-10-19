package cypher

import (
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// NewFrontEnd is the nl.FrontEnd for a model writing Cypher over g (#354): nl.Translate with it shows
// the model g in Cypher's terms and examples (questions with Cypher answering them), holds it to
// Grammar(g), compiles each draft with Compile, and sends every refusal back as Explain writes it.
// A construct a program can't express yet is refused as Try.Kind "gap", anything else Compile refuses
// as "compile".
func NewFrontEnd(g Graph, examples ...nl.Example) nl.FrontEnd {
	return frontEnd{g: g, examples: examples}
}

type frontEnd struct {
	g        Graph
	examples []nl.Example
}

// KindGap is the Try.Kind of a draft using Cypher a program can't express yet.
const KindGap = "gap"

func (frontEnd) Language() string { return "cypher" }

// Card is the graph a query can name, a line for each label and each relationship type, so a model
// reads the names it may use.
func (f frontEnd) Card(*ns.Vocabulary) (string, error) {
	var b strings.Builder
	b.WriteString("# The graph a Cypher query reads. Node labels, with their properties:\n")
	for _, n := range f.g.Nodes {
		fmt.Fprintf(&b, "#   (:%s%s)\n", n.Label, propsText(n.Props))
	}
	b.WriteString("# Relationship types:\n")
	for _, e := range f.g.Edges {
		fmt.Fprintf(&b, "#   -[:%s%s]->\n", e.Type, propsText(e.Props))
	}
	if len(f.g.Derived) > 0 {
		fmt.Fprintf(&b, "# Any node may have%s, which the host computes.\n", propsText(f.g.Derived))
	}
	if f.g.Props != nil {
		b.WriteString("# A node's other properties are any key its facts name.\n")
	}
	return b.String(), nil
}

// Instructions say what of Cypher compiles, what doesn't, and how a node answers.
func (frontEnd) Instructions() string {
	return `
# Write one Cypher query answering the question, using only the labels, types and properties above:
# MATCH patterns, WHERE, WITH, UNION, and RETURN with DISTINCT, count, min, max, sum, collect,
# ORDER BY, SKIP and LIMIT. A node is its key, so RETURN n answers n's key. Strings are 'quoted'.
# Not supported: OPTIONAL MATCH, relationship variables and paths, UNWIND and lists, CASE, writes.
# Reply with the query alone.
`
}

func (f frontEnd) Examples() []nl.Example { return f.examples }

func (f frontEnd) Grammar(*ns.Vocabulary) (string, error) { return Grammar(f.g), nil }

func (f frontEnd) Compile(v *ns.Vocabulary, text string) (nl.Compiled, error) {
	p, err := Compile(v, f.g, text)
	c := nl.Compiled{Program: p.Text, Query: p.Query, Explain: func(err error) string { return Explain(text, p, err) }}
	var ce *Error
	if errors.As(err, &ce) && ce.Gap != 0 {
		c.Kind = KindGap
	}
	return c, err
}
