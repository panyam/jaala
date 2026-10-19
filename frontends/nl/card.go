// Package nl is the plain-language front end a host puts before the engine (#321, mission #322):
// what a model reads to write a program, and what it is told when the program doesn't check. A host
// keeps what its words mean, its grounding data and its model clients; nl keeps the parts every host
// would otherwise write alike. Engine-only hosts never import it.
//
// Card lists a vocabulary compactly enough to sit in a model's prompt as a cached prefix. Explain
// turns an error from Parse, Validate or Eval into text a model can fix the program from: the
// message, where in the program it points, and the fix when the engine knows one. Translate is the
// loop around them (#325): it asks a host's Model for a program, checks it, feeds each error back
// through Explain, and falls back to a second model, recording every try in a Trace. Eval scores a
// translator over questions with known answers (#326): pass rate on the first try and after retries,
// tokens, and what the refused tries were refused for. nl runs no model itself.
package nl

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/panyam/jaala/ns"
)

// cardLegend opens every card, so a reader knows the notation without the docs.
const cardLegend = `# Every relation and predicate a program can call, by module: name(arg: type, ...) # what it holds.
# ~type is inferred from the relation's rules, "in" marks an argument the caller must bind, [, arg] is optional.
# A predicate says what it needs bound before it runs: "filter" needs every argument.
`

// Card lists every member of v, depth-first from the root, each module's members sorted, one line
// each: its signature and the first line of its doc. The same vocabulary always gives the same text,
// so a host can keep it as a cached prompt prefix. It is built once per state of v (ns.Vocabulary.Memo),
// so a host can call it for every prompt, and a registration on v gives the next call a new card. It
// returns v's Check error when its modules don't check.
func Card(v *ns.Vocabulary) (string, error) {
	c, err := v.Memo(cardKey{}, func() (any, error) { return buildCard(v) })
	if err != nil {
		return "", err
	}
	return c.(string), nil
}

type cardKey struct{}

// cardBuilds counts the cards built, for the test that a second call builds none.
var cardBuilds atomic.Int64

func buildCard(v *ns.Vocabulary) (string, error) {
	cardBuilds.Add(1)
	var b strings.Builder
	b.WriteString(cardLegend)
	if err := cardModule(&b, v, ""); err != nil {
		return "", err
	}
	return b.String(), nil
}

func cardModule(b *strings.Builder, v *ns.Vocabulary, module string) error {
	members, err := v.Members(module)
	if err != nil {
		return err
	}
	var subs []string
	wrote := false
	for _, m := range members {
		if m.Kind == ns.EntryModule {
			subs = append(subs, m.Path)
			continue
		}
		if !wrote && module != "" {
			fmt.Fprintf(b, "\nmodule %s\n", module)
		}
		wrote = true
		b.WriteString(cardLine(v, m))
	}
	if module == "" {
		b.WriteString(closureLine(v))
		b.WriteString(pathsLine(v))
	}
	for _, s := range subs {
		if err := cardModule(b, v, s); err != nil {
			return err
		}
	}
	return nil
}

// closureLine is the card's line for graph.closure (#281), which the engine answers rather than the
// vocabulary, when the vocabulary holds a relation it can walk.
func closureLine(v *ns.Vocabulary) string {
	members, _, err := callees(v)
	if err != nil || len(walkable(members)) == 0 {
		return ""
	}
	return `graph.closure(relation, from, to, min: number, max: number, hops: number) needs relation+min+max # to is reachable from from in min to max steps of the two-argument relation named in quotes (max 0: no bound), hops the fewest` + "\n"
}

// pathsLine is the card's line for graph.paths (#280), when the vocabulary holds a relation it can walk.
func pathsLine(v *ns.Vocabulary) string {
	members, _, err := callees(v)
	if err != nil || len(pathable(members)) == 0 {
		return ""
	}
	return `graph.paths(relation, from, to, mode, min: number, max: number, path) needs relation+mode+min+max # each path in min to max steps of the relation named in quotes, mode "trail", "acyclic", "simple", "shortest" or "walk"` + "\n"
}

func cardLine(v *ns.Vocabulary, m ns.Entry) string {
	required := -1
	pred, isPred := v.Predicate(m.Path)
	if isPred && pred.MaxArity > 0 {
		required = pred.Arity
	}
	var line strings.Builder
	line.WriteString(m.Path + "(")
	for i, a := range m.Args {
		switch {
		case i == required:
			line.WriteString("[, ")
		case i > 0:
			line.WriteString(", ")
		}
		line.WriteString(cardArg(a))
	}
	if required >= 0 && required < len(m.Args) {
		line.WriteString("]")
	}
	line.WriteString(")")
	if isPred {
		line.WriteString(" " + needs(pred, m.Args))
	}
	if doc := firstLine(m.Doc); doc != "" {
		line.WriteString(" # " + doc)
	}
	return line.String() + "\n"
}

func cardArg(a ns.ArgSig) string {
	s := a.Name
	if a.Input {
		s = "in " + s
	}
	t := a.ArgType.String()
	if t == "" {
		return s
	}
	if a.Inferred {
		t = "~" + t
	}
	return s + ": " + t
}

// needs says what a predicate needs bound before it runs: "filter" for a filter, or a generator's
// modes by argument name, "needs a or b", "needs nothing" for one that lists every value.
func needs(bi ns.Builtin, args []ns.ArgSig) string {
	if !bi.IsGenerator() {
		return "filter"
	}
	var modes []string
	for _, m := range bi.Modes {
		var bound []string
		for i, need := range m {
			if need && i < len(args) {
				bound = append(bound, args[i].Name)
			}
		}
		if len(bound) == 0 {
			return "needs nothing"
		}
		modes = append(modes, strings.Join(bound, "+"))
	}
	return "needs " + strings.Join(modes, " or ")
}

func firstLine(doc string) string {
	doc, _, _ = strings.Cut(strings.TrimSpace(doc), "\n")
	return strings.TrimSpace(doc)
}
