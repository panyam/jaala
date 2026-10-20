package nl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// ExtractOptions tune Extract. The zero value tries the model three times and checks each fact
// against the vocabulary alone.
type ExtractOptions struct {
	// MaxTries is how many drafts each model gets; 3 when 0.
	MaxTries int
	// Fallback, when set, is asked once Model runs out of tries or repeats a draft it was told was
	// wrong, in a fresh conversation.
	Fallback Model
	// Base, when set, is what the host holds already: an added fact whose Pack.Keys match one of its
	// facts with other values replaces that fact. Eval are the options it is read with.
	Base *datalog.Base
	Eval []datalog.Option
	// Grammar replaces the grammar every Prompt carries, ExtractGrammar(v) by default. NoGrammar
	// sends none.
	Grammar   string
	NoGrammar bool
	// Pack, when set, is the host's domain: its About and Card follow the relations in the prefix, its
	// ExtractExamples follow those, checked before any model is asked, and its Keys say which arguments
	// identify a fact.
	Pack *Pack
	// Budget caps the tokens and time Extract spends on the text.
	Budget Budget
}

// An Extraction is what Extract accepted: the change set, every line read from the draft in order,
// the draft as the model wrote it less any Markdown fence, and the model that wrote it.
type Extraction struct {
	Changes ns.Changes
	Facts   []Fact
	Text    string
	Model   string
}

// A Fact is one line of an accepted draft: a fact the text adds (Add) or says no longer holds, its
// values as the engine reads a query's constants, and the words of the text that state it, Span as
// quoted and Start and End its byte offsets in the text, -1 when the line quoted none. Replaced are the
// facts it replaced through Pack.Keys, from the Base or from an earlier line, which the change set
// removes.
type Fact struct {
	Add        bool
	Relation   string
	Values     []ns.Value
	Span       string
	Start, End int
	Replaced   [][]ns.Value
}

// An ExtractExample is a text with the lines Extract should write for it, which the model reads before
// it writes its own.
type ExtractExample struct {
	Text, Lines string
}

// The Try kinds only Extract gives.
const (
	// KindSpan is a line whose quoted words aren't in the text.
	KindSpan = "span"
	// KindDerived is a fact of a relation the vocabulary derives, which only changes through its rules.
	KindDerived = "derived"
)

// extractInstructions follow the relations in every Extract Prompt's Prefix.
const extractInstructions = `
# Write the facts the text states, one per line, using only the relations above:
#   + relation("value", ...) from "the words of the text that state it"
# for a fact the text states, and
#   - relation("value", ...) from "the words of the text that say so"
# for one it says no longer holds. Values are "quoted" text or numbers. After from, quote the
# text's own words exactly as they appear. Reply with the lines alone, or nothing if the text
# states no fact.
`

// Extract asks m for the facts text states over v's base relations (#414), as a change set the host
// can apply: it drafts, reads each line as a fact (+ or -), checks its values as datalog.Validate
// checks a query's constants (types, closed domains, units) and that the quoted words are in text,
// and feeds the first refusal back for m to fix, up to o.MaxTries times, then hands the text to
// o.Fallback the same way. Each added fact cites its words as text:<start>-<end>, byte offsets into
// text. A fact whose relation has Pack.Keys replaces the facts the Base, or an earlier line, holds
// under the same key with other values: the change set removes them, since the engine never decides
// which facts are one. A pack whose examples or keys don't check fails Extract before any model is
// asked. The Trace holds every try, also when Extract fails, and the error then wraps the last try's.
func Extract(ctx context.Context, text string, v *ns.Vocabulary, m Model, o ExtractOptions) (Extraction, Trace, error) {
	var tr Trace
	prefix, err := extractPrefix(v, o.Pack)
	if err != nil {
		return Extraction{}, tr, err
	}
	grammar := o.Grammar
	switch {
	case o.NoGrammar:
		grammar = ""
	case grammar == "":
		if grammar, err = ExtractGrammar(v); err != nil {
			return Extraction{}, tr, err
		}
	}
	tries := o.MaxTries
	if tries <= 0 {
		tries = 3
	}
	spend := &spending{budget: o.Budget, start: time.Now()}
	models := []Model{m}
	if o.Fallback != nil {
		models = append(models, o.Fallback)
	}
	var last error
	for _, model := range models {
		turns := []Turn{{RoleUser, "Text:\n" + text}}
		refused := map[string]error{}
		for t := 0; t < tries; t++ {
			if err := ctx.Err(); err != nil {
				return Extraction{}, tr, err
			}
			if err := spend.check(); err != nil {
				return Extraction{}, tr, err
			}
			try := Try{Model: model.Name()}
			start := time.Now()
			c, err := model.Complete(ctx, Prompt{Prefix: prefix, Turns: turns, Grammar: grammar})
			try.Latency = time.Since(start)
			if err != nil {
				try.Err, try.Kind = err, KindModel
				tr.Tries = append(tr.Tries, try)
				last = fmt.Errorf("model %s: %w", try.Model, err)
				break
			}
			try.TokensIn, try.TokensOut, try.TokensCached = c.TokensIn, c.TokensOut, c.TokensCached
			spend.add(c.TokensIn, c.TokensOut)
			try.Text = unfence(c.Text)
			if first, ok := refused[try.Text]; ok {
				try.Err, try.Kind = fmt.Errorf("the same draft as before: %w", first), KindRepeat
				tr.Tries = append(tr.Tries, try)
				last = try.Err
				break
			}
			x, err := readDraft(ctx, text, try.Text, v, o.Pack, o.Base, o.Eval)
			if err == nil {
				tr.Tries = append(tr.Tries, try)
				x.Text, x.Model = try.Text, try.Model
				return x, tr, nil
			}
			try.Err, try.Kind = err, extractKind(err)
			tr.Tries = append(tr.Tries, try)
			last = err
			refused[try.Text] = err
			why := strings.TrimRight(explainLine(err), "\n")
			turns = append(turns, Turn{RoleModel, try.Text}, Turn{RoleUser, why + "\n\nWrite all the lines again, fixed."})
		}
	}
	return Extraction{}, tr, fmt.Errorf("nl: no facts checked after %d tries: %w", len(tr.Tries), last)
}

// extractPrefix is every text's Prefix: v's base relations, the instructions, then the pack's About,
// Card and extraction examples, each checked first.
func extractPrefix(v *ns.Vocabulary, p *Pack) (string, error) {
	card, err := extractCard(v)
	if err != nil {
		return "", err
	}
	prefix := card + extractInstructions
	if p == nil {
		return prefix, nil
	}
	if err := p.Validate(v); err != nil {
		return "", err
	}
	about := *p
	about.Examples = nil
	prefix += about.Prefix()
	if len(p.ExtractExamples) > 0 {
		var b strings.Builder
		b.WriteString("\n# Examples:\n")
		for _, e := range p.ExtractExamples {
			for _, line := range strings.Split(strings.TrimSpace(e.Text), "\n") {
				b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
			}
			b.WriteString(strings.TrimSpace(e.Lines) + "\n")
		}
		prefix += b.String()
	}
	return prefix, nil
}

// extractCard lists v's base relations, in path order, each with its arguments' labels, a closed
// domain's values and its doc.
func extractCard(v *ns.Vocabulary) (string, error) {
	rels, err := baseRelations(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# The relations a fact can be added to or removed from:\n")
	for _, r := range rels {
		s, _ := v.Schema(r.path)
		args := make([]string, s.Arity)
		for i := range args {
			args[i] = fmt.Sprintf("arg%d", i+1)
			if i < len(s.Labels) && s.Labels[i] != "" {
				args[i] = s.Labels[i]
			}
			if i < len(r.domains) && len(r.domains[i]) > 0 {
				quoted := make([]string, len(r.domains[i]))
				for k, d := range r.domains[i] {
					quoted[k] = `"` + d + `"`
				}
				args[i] += ": one of " + strings.Join(quoted, ", ")
			}
		}
		line := fmt.Sprintf("# %s(%s)", r.path, strings.Join(args, ", "))
		if s.Doc != "" {
			line += "  " + s.Doc
		}
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}

// baseRelations are v's members a fact can be added to: those with a base schema, which a derived
// relation or a predicate has none of.
func baseRelations(v *ns.Vocabulary) ([]callee, error) {
	members, _, err := callees(v)
	if err != nil {
		return nil, err
	}
	var out []callee
	for _, m := range members {
		if m.kind == ns.EntryPredicate {
			continue
		}
		if _, ok := v.Schema(m.path); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("nl: the vocabulary holds no base relation to extract facts into")
	}
	return out, nil
}

// ExtractGrammar is GBNF holding a reply to Extract over v: lines of + or - and a fact of one of v's
// base relations at its arity, each argument a constant, or one of a closed domain's values, and after
// an added fact the quoted words it came from. Like GrammarFor, it is built once per state of v.
func ExtractGrammar(v *ns.Vocabulary) (string, error) {
	g, err := v.Memo(extractGrammarKey{}, func() (any, error) {
		rules, err := extractRules(v)
		if err != nil {
			return nil, err
		}
		return render(rules), nil
	})
	if err != nil {
		return "", err
	}
	return g.(string), nil
}

type extractGrammarKey struct{}

func extractRules(v *ns.Vocabulary) ([]rule, error) {
	rels, err := baseRelations(v)
	if err != nil {
		return nil, err
	}
	var extra []rule
	var facts alt
	for i, r := range rels {
		name := fmt.Sprintf("x%d", i)
		s, _ := v.Schema(r.path)
		form := seq{lit(r.path + "(")}
		for k := 0; k < s.Arity; k++ {
			if k > 0 {
				form = append(form, lit(", "))
			}
			if k >= len(r.domains) || len(r.domains[k]) == 0 {
				form = append(form, ref("constant"))
				continue
			}
			dname := fmt.Sprintf("%sa%d", name, k)
			var vals alt
			for _, d := range r.domains[k] {
				vals = append(vals, lit(`"`+d+`"`))
			}
			extra = append(extra, rule{dname, vals})
			form = append(form, ref(dname))
		}
		extra = append(extra, rule{name, append(form, lit(")"))})
		facts = append(facts, ref(name))
	}
	out := []rule{
		{"root", seq{ref("eline"), star(seq{lit("\n"), ref("eline")})}},
		{"eline", alt{seq{alt{lit("+ "), lit("- ")}, ref("efact"), lit(" from "), ref("string")}, seq{lit("- "), ref("efact")}}},
		{"efact", facts},
	}
	for _, r := range programRules() {
		if r.name != "root" {
			out = append(out, r)
		}
	}
	return reachable(append(out, extra...)), nil
}

// A lineError is a refused line of a draft: its number from 1, its text, and where in it the fact
// starts (-1 when the line has none), which explainLine points into.
type lineError struct {
	line   int
	text   string
	atomAt int
	kind   string
	err    error
}

func (e *lineError) Error() string { return fmt.Sprintf("line %d: %v", e.line, e.err) }
func (e *lineError) Unwrap() error { return e.err }

// extractKind sorts a refusal as Try.Kind does: a CheckError's Kind, or the line's own.
func extractKind(err error) string {
	var le *lineError
	if errors.As(err, &le) && le.kind != "" {
		return le.kind
	}
	return kindOf(err, KindEval)
}

// explainLine writes err as a model reads it to fix its draft: the message, then the line it is
// about with a caret under the place, when the engine can place it, then the fix when it knows one.
func explainLine(err error) string {
	var le *lineError
	if !errors.As(err, &le) {
		return Explain("", err)
	}
	var b strings.Builder
	b.WriteString(le.Error())
	if le.atomAt >= 0 {
		atom := le.text[le.atomAt:]
		if end := factEnd(atom); end > 0 {
			atom = atom[:end]
		}
		if pos, ok := datalog.Locate(atom, le.err); ok {
			col := utf8.RuneCountInString(le.text[:le.atomAt]) + utf8.RuneCountInString(atom[:pos.Offset])
			fmt.Fprintf(&b, "\n  %s\n  %s^", le.text, strings.Repeat(" ", col))
		}
	}
	var ce *datalog.CheckError
	if errors.As(err, &ce) && ce.Fix != "" {
		b.WriteString("\nfix: " + ce.Fix)
	}
	return b.String()
}

// readDraft reads draft's lines as facts text states over v, with p's Keys against b and the earlier
// lines, into an Extraction, or the first line's refusal as a lineError.
func readDraft(ctx context.Context, text, draft string, v *ns.Vocabulary, p *Pack, b *datalog.Base, opts []datalog.Option) (Extraction, error) {
	var x Extraction
	added := map[string]map[string][]ns.Value{} // relation, then key, to the values an earlier line added
	for n, line := range strings.Split(draft, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f, err := readLine(text, line, v)
		if err != nil {
			err.line = n + 1
			return Extraction{}, err
		}
		t := ns.Tuple{Vals: f.Values}
		if f.Start >= 0 {
			t.Cites = []string{fmt.Sprintf("text:%d-%d", f.Start, f.End)}
		}
		if !f.Add {
			x.Changes.Remove(f.Relation, t)
			x.Facts = append(x.Facts, f)
			continue
		}
		if keys := keysOf(p, f.Relation); len(keys) > 0 && len(keys) < len(f.Values) {
			key := valuesKey(pick(f.Values, keys))
			if added[f.Relation] == nil {
				added[f.Relation] = map[string][]ns.Value{}
			}
			if prev, ok := added[f.Relation][key]; ok && !sameValues(prev, f.Values) {
				f.Replaced = append(f.Replaced, prev)
				x.Changes.Remove(f.Relation, ns.Tuple{Vals: prev})
			}
			added[f.Relation][key] = f.Values
			if b != nil {
				held, err := heldUnder(ctx, b, opts, f.Relation, f.Values, keys)
				if err != nil {
					return Extraction{}, &lineError{line: n + 1, text: line, atomAt: -1, kind: KindEval, err: err}
				}
				for _, old := range held {
					if !sameValues(old, f.Values) {
						f.Replaced = append(f.Replaced, old)
						x.Changes.Remove(f.Relation, ns.Tuple{Vals: old})
					}
				}
			}
		}
		x.Changes.Add(f.Relation, t)
		x.Facts = append(x.Facts, f)
	}
	return x, nil
}

// readLine reads one line of a draft as a Fact, checking it against v and text.
func readLine(text, line string, v *ns.Vocabulary) (Fact, *lineError) {
	refuse := func(atomAt int, kind string, err error) (Fact, *lineError) {
		return Fact{}, &lineError{text: line, atomAt: atomAt, kind: kind, err: err}
	}
	f := Fact{Start: -1, End: -1}
	switch {
	case strings.HasPrefix(line, "+"):
		f.Add = true
	case strings.HasPrefix(line, "-"):
	default:
		return refuse(-1, KindParse, errors.New(`a line is + and a fact the text states, or - and one it says no longer holds`))
	}
	at := 1 + len(line[1:]) - len(strings.TrimLeft(line[1:], " "))
	rest := line[at:]
	end := factEnd(rest)
	if end < 0 {
		end = len(rest)
	}
	atom, tail := rest[:end], strings.TrimSpace(rest[end:])
	q, err := datalog.Parse(atom)
	if err != nil {
		return refuse(at, KindParse, err)
	}
	if len(q.Rules) > 0 || len(q.Goal.Literals) != 1 || q.Goal.Literals[0].Pos == nil || len(q.Select) > 0 {
		return refuse(at, KindParse, errors.New("a line holds one fact, a relation and its values"))
	}
	a := q.Goal.Literals[0].Pos
	for _, arg := range a.Args {
		if arg.Const == nil {
			name := "?" + string(arg.Var)
			if arg.Var == "_" {
				name = "_"
			}
			return refuse(at, KindParse, fmt.Errorf("a fact's arguments are values, and %s is a variable: write the value the text states", name))
		}
	}
	if err := datalog.Validate(q, v); err != nil {
		return refuse(at, kindOf(err, KindEval), err)
	}
	f.Relation = a.Relation
	for _, arg := range a.Args {
		f.Values = append(f.Values, *arg.Const)
	}
	var one ns.Changes
	if err := one.Add(f.Relation, ns.Tuple{Vals: f.Values}).Check(v); err != nil {
		return refuse(at, KindDerived, err)
	}
	switch {
	case tail == "" && f.Add:
		return refuse(-1, KindSpan, fmt.Errorf(`an added fact says where the text states it: + %s from "the text's words"`, atom))
	case tail == "":
		return f, nil
	case !strings.HasPrefix(tail, "from "):
		return refuse(-1, KindParse, fmt.Errorf(`after the fact only from "the text's words" may follow, not %q`, tail))
	}
	quoted := strings.TrimSpace(strings.TrimPrefix(tail, "from "))
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return refuse(-1, KindParse, fmt.Errorf(`the text's words after from are "quoted", not %s`, quoted))
	}
	f.Span = quoted[1 : len(quoted)-1]
	words := unescape(f.Span)
	i := strings.Index(text, words)
	if words == "" || i < 0 {
		return refuse(-1, KindSpan, fmt.Errorf("the text doesn't say %q: quote its words exactly as they appear", words))
	}
	f.Start, f.End = i, i+len(words)
	return f, nil
}

// factEnd is the index just past the parenthesis closing the fact s starts with, outside quoted
// text, where a backslash keeps the next character as the parser does, or -1 when none closes it.
func factEnd(s string) int {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == ')':
			return i + 1
		}
	}
	return -1
}

// unescape is quoted words as the text writes them: a backslash keeps the next character, which a
// fact's value holds both of (the parser's rule) but the text holds alone.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// keysOf is p's key positions for rel, nil without a pack or keys.
func keysOf(p *Pack, rel string) []int {
	if p == nil {
		return nil
	}
	return p.Keys[rel]
}

func pick(vals []ns.Value, at []int) []ns.Value {
	out := make([]ns.Value, len(at))
	for i, k := range at {
		out[i] = vals[k]
	}
	return out
}

// heldUnder are the facts of rel b holds with vals' values at keys, read by evaluating rel's call with
// those values as constants.
func heldUnder(ctx context.Context, b *datalog.Base, opts []datalog.Option, rel string, vals []ns.Value, keys []int) ([][]ns.Value, error) {
	isKey := map[int]bool{}
	for _, k := range keys {
		isKey[k] = true
	}
	args := make([]string, len(vals))
	var cols []string
	for i, v := range vals {
		if isKey[i] {
			args[i] = constantText(v)
			continue
		}
		args[i] = fmt.Sprintf("?v%d", i)
		cols = append(cols, args[i])
	}
	q, err := datalog.Parse(fmt.Sprintf("%s(%s) => %s", rel, strings.Join(args, ", "), strings.Join(cols, ", ")))
	if err != nil {
		return nil, err
	}
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, b, opts...)
	if err != nil {
		return nil, err
	}
	out := make([][]ns.Value, len(rows))
	for r, row := range rows {
		held := append([]ns.Value(nil), vals...)
		for i := range held {
			if !isKey[i] {
				held[i] = row.Bind[datalog.Var(fmt.Sprintf("v%d", i))]
			}
		}
		out[r] = held
	}
	return out, nil
}

// constantText writes v as a query's constant: a number with its unit, or quoted text, whose
// backslashes are already the parser's.
func constantText(v ns.Value) string {
	if v.Num != nil {
		return v.S + v.BaseUnit
	}
	return `"` + v.S + `"`
}

// sameValues reports whether a and b are one fact's values: numbers by value and unit, text by text.
func sameValues(a, b []ns.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		switch {
		case (x.Num == nil) != (y.Num == nil):
			return false
		case x.Num != nil:
			if *x.Num != *y.Num || x.BaseUnit != y.BaseUnit {
				return false
			}
		case x.S != y.S:
			return false
		}
	}
	return true
}

// valuesKey keys values for sameValues' equality.
func valuesKey(vals []ns.Value) string {
	var b strings.Builder
	for _, v := range vals {
		if v.Num != nil {
			fmt.Fprintf(&b, "n%v%s\x00", *v.Num, v.BaseUnit)
		} else {
			fmt.Fprintf(&b, "s%s\x00", v.S)
		}
	}
	return b.String()
}
