package nl

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A Pack is what a host knows about its domain beyond its vocabulary (#344): what the data is about,
// what it holds, programs that answer typical questions, how a question's names map to values, and
// what makes an answer suspect. Declaire's code graph, Agni's schematics and a maps app's routes are
// each a pack over the same Translate. Options.Pack hands it to Translate, and a host checks it on its
// own with Validate, so a broken example fails its test rather than a model's prompt. Every field is
// optional.
type Pack struct {
	// About says what the data is about, in the words its users use ("callers, implementers, imports,
	// routes and tests of the code in a workspace"). It opens the pack's part of the prefix, and #347's
	// MCP server writes its instructions from it.
	About string
	// Card says what the data holds that the vocabulary can't: the kinds and values a question will
	// name, how ids are spelled. DataCard writes the generic half from a Base; a host adds what only
	// it knows. It goes into the prefix as written, so each line should start with "#".
	Card string
	// Examples are questions with the programs that answer them, one or two per construct (a join,
	// negation, a rule, an aggregate, a walk). How to call one relation belongs in its doc instead.
	Examples []Example
	// Grounder grounds the question's names when Translate is called with a nil Grounder.
	Grounder Grounder
	// Check, when set, judges a program that checked and, over Options.Base, answered: an error refuses
	// it as KindAnswer and goes back to the model, as a check error does, with the error's text. It is
	// for what makes an answer suspect in this domain, such as every value absent or far more rows
	// than the question could mean. p.Rows is nil when Translate has no Base.
	Check func(ctx context.Context, p Program) error
	// Hint, when set, says what the data holds where an empty answer lost its last binding: given
	// why (never nil), the values its culprit's constants could have meant, in the host's terms ("no
	// node kind entity; the kinds are package, func, type"). jaala names the call that matched nothing
	// and whether it matches alone; only the host knows its data's spellings (#343). Translate adds
	// the hint to an empty draft's feedback, and frontends/mcp to an empty answer of query, cypher and
	// explain. "" adds nothing. It runs once per empty answer, so it should cost a few lookups.
	Hint func(ctx context.Context, why *datalog.EmptyReport) string
	// Keys, for Extract, are the argument positions that identify a relation's fact (#414): an added
	// fact holding the same values there as one the Base or an earlier line holds, with others
	// elsewhere, replaces it, as owner("app", "bob") replaces owner("app", "alice") under {0}. A
	// relation without keys only ever gains facts. The engine never decides which facts are one
	// (#183), so this is the host's to say.
	Keys map[string][]int
	// ExtractExamples are texts with the lines Extract should write for them, which follow the pack's
	// About and Card in Extract's prefix. Translate doesn't read them.
	ExtractExamples []ExtractExample
}

// An Example is a question with the program that answers it, which the model reads before it writes
// its own.
type Example struct {
	Question, Program string
}

// Validate checks that every example parses and checks against v, so a pack can't teach a model a
// relation v doesn't have, that every key names a base relation of v and positions within its
// arity, and that every extraction example's lines are facts its text states. The error names the
// first that doesn't. Translate and Extract call it before asking any model.
func (p *Pack) Validate(v *ns.Vocabulary) error {
	for i, e := range p.Examples {
		q, err := datalog.Parse(e.Program)
		if err == nil {
			err = datalog.Validate(q, v)
		}
		if err != nil {
			return fmt.Errorf("nl: the pack's example %d (%q): %w", i+1, e.Question, err)
		}
	}
	rels := make([]string, 0, len(p.Keys))
	for rel := range p.Keys {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		s, ok := v.Schema(rel)
		if !ok {
			return fmt.Errorf("nl: the pack's keys name %s, which isn't a base relation", rel)
		}
		for _, k := range p.Keys[rel] {
			if k < 0 || k >= s.Arity {
				return fmt.Errorf("nl: the pack's keys for %s name argument %d, and it takes %d", rel, k, s.Arity)
			}
		}
	}
	for i, e := range p.ExtractExamples {
		if _, err := readDraft(context.Background(), e.Text, strings.TrimSpace(e.Lines), v, p, nil, nil); err != nil {
			return fmt.Errorf("nl: the pack's extraction example %d: %w", i+1, err)
		}
	}
	return nil
}

// Prefix is the pack's part of a Prompt's Prefix, which Translate puts after the card and its
// instructions: About as comment lines, Card as written, then the examples, each question as a
// comment over its program. The same pack always gives the same text, so the prefix stays cacheable.
func (p *Pack) Prefix() string {
	var b strings.Builder
	if about := strings.TrimSpace(p.About); about != "" {
		b.WriteString("\n# What the data is about:\n")
		for _, line := range strings.Split(about, "\n") {
			b.WriteString(strings.TrimRight("# "+strings.TrimSpace(line), " ") + "\n")
		}
	}
	if card := strings.TrimSpace(p.Card); card != "" {
		b.WriteString("\n" + card + "\n")
	}
	if len(p.Examples) > 0 {
		b.WriteString("\n# Examples:\n")
		for _, e := range p.Examples {
			fmt.Fprintf(&b, "# %s\n%s\n", e.Question, strings.TrimSpace(e.Program))
		}
	}
	return b.String()
}

// fewValues is the most values DataCard lists for an argument.
const fewValues = 12

// valueMax is the longest value DataCard writes before cutting it short.
const valueMax = 40

// DataCard writes the generic half of a pack's Card from b's Source (#344): each relation it serves,
// in the Source's order, with how many facts it holds and, per argument, the values with how many
// facts hold each, or how many values there are and the commonest when there are more than 12. It
// asks b one counting program per argument, under ctx and opts (a Budget, say), so over a store it
// reads every relation whole: a host computes it once per data version, not per question. A relation
// of no arguments is listed by name alone.
func DataCard(ctx context.Context, b *datalog.Base, opts ...datalog.Option) (string, error) {
	src := b.Source()
	var out strings.Builder
	out.WriteString("# The data: each relation's facts, and each argument's values with how many facts hold each (or how many values, and the commonest, when there are many).\n")
	for _, rel := range src.Relations() {
		s, ok := src.Schema(rel)
		if !ok {
			continue
		}
		labels := make([]string, s.Arity)
		vars := make([]string, s.Arity)
		for i := range labels {
			labels[i] = fmt.Sprintf("arg%d", i+1)
			if i < len(s.Labels) && s.Labels[i] != "" {
				labels[i] = s.Labels[i]
			}
			vars[i] = fmt.Sprintf("?a%d", i)
		}
		if s.Arity == 0 {
			fmt.Fprintf(&out, "# %s()\n", rel)
			continue
		}
		var cols []string
		facts := 0
		for i := range s.Arity {
			counts, err := valueCounts(ctx, b, rel, vars, i, opts)
			if err != nil {
				return "", fmt.Errorf("nl: counting %s's %s: %w", rel, labels[i], err)
			}
			if i == 0 {
				for _, c := range counts {
					facts += c.n
				}
			}
			cols = append(cols, fmt.Sprintf("#   %s: %s\n", labels[i], valuesText(counts)))
		}
		fmt.Fprintf(&out, "# %s(%s): %d facts\n", rel, strings.Join(labels, ", "), facts)
		if facts > 0 {
			out.WriteString(strings.Join(cols, ""))
		}
	}
	return out.String(), nil
}

type valueCount struct {
	v ns.Value
	n int
}

// valueCounts runs rel(?a0, ...) => ?ai, count(?ai), commonest first, then in value order. Every
// argument is a named variable, so each fact is a binding of its own and the count is of facts.
func valueCounts(ctx context.Context, b *datalog.Base, rel string, vars []string, i int, opts []datalog.Option) ([]valueCount, error) {
	text := fmt.Sprintf("%s(%s) => %s, count(%s)", rel, strings.Join(vars, ", "), vars[i], vars[i])
	q, err := datalog.Parse(text)
	if err != nil {
		return nil, err
	}
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, b, opts...)
	if err != nil {
		return nil, err
	}
	name := datalog.Var(strings.TrimPrefix(vars[i], "?"))
	counts := make([]valueCount, 0, len(rows))
	for _, r := range rows {
		n := r.Bind["count("+name+")"].Num
		if n == nil {
			return nil, fmt.Errorf("no count in %s", text)
		}
		counts = append(counts, valueCount{r.Bind[name], int(*n)})
	}
	sort.SliceStable(counts, func(x, y int) bool { return counts[x].n > counts[y].n })
	return counts, nil
}

// valuesText lists counts, or summarizes them when there are more than fewValues.
func valuesText(counts []valueCount) string {
	if len(counts) > fewValues {
		return fmt.Sprintf("%d values, commonest %s (%d)", len(counts), cardValue(counts[0].v), counts[0].n)
	}
	parts := make([]string, len(counts))
	for i, c := range counts {
		parts[i] = fmt.Sprintf("%s (%d)", cardValue(c.v), c.n)
	}
	return strings.Join(parts, ", ")
}

// cardValue writes v as a program would: absent, a number with its unit, text quoted and cut at valueMax runes.
func cardValue(v ns.Value) string {
	switch {
	case v.Absent:
		return "absent"
	case v.Num != nil && v.BaseUnit != "" && !strings.HasSuffix(v.S, v.BaseUnit):
		return v.S + v.BaseUnit
	case v.Num != nil:
		return v.S
	}
	if r := []rune(v.S); len(r) > valueMax {
		return fmt.Sprintf("%q…", string(r[:valueMax]))
	}
	return fmt.Sprintf("%q", v.S)
}
