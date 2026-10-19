package nl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/ns"
)

// Actions turn Translate into a loop (#393): before answering, a model may look at the data, with
// replies a small model can write under a grammar, and each one's result comes back to it as the
// next turn. A reply is one of
//
//	look NAME       a member's signature and doc, or a module's members (a label's, in Cypher)
//	values TARGET   up to Sample of the values an argument holds (pkg.license; Package.license)
//	try             on its first line, then a draft: run with Sample rows at most, its rows or why
//	                it answered none sent back
//
// and any other reply is the answer, judged as a draft always is. The loop doesn't read the drafts'
// language: try runs a draft through the front end as an answer is, and a front end that is a Prober
// writes look and values in its own terms. Steps cap the actions a model asks before it must answer;
// a step doesn't count against MaxTries.
type Actions struct {
	// Steps is how many actions each model may ask, 6 when 0.
	Steps int
	// Sample is how many rows a try or values answers, 10 when 0.
	Sample int
}

// A Prober is a FrontEnd's side of Actions: look and values in its language. A front end that isn't
// one answers look with jaala's card and values with a jaala program, which names the model wouldn't
// know from its card.
type Prober interface {
	// Look describes name, as the card does but whole.
	Look(v *ns.Vocabulary, name string) (string, error)
	// Probe is a draft, in the front end's language, answering up to limit of target's values.
	Probe(v *ns.Vocabulary, target string, limit int) (string, error)
	// ValuesTarget is how a target is written, for the instructions ("Label.property").
	ValuesTarget() string
}

// A Step is one action a model asked for: the reply, what went back, and what the reply cost. After
// is how many tries (answers) came before it.
type Step struct {
	Model, Action, Text, Result       string
	After                             int
	TokensIn, TokensOut, TokensCached int
	Latency                           time.Duration
}

// The actions a reply can be.
const (
	ActionLook   = "look"
	ActionValues = "values"
	ActionTry    = "try"
)

func (a *Actions) steps() int {
	if a.Steps > 0 {
		return a.Steps
	}
	return 6
}

func (a *Actions) sample() int {
	if a.Sample > 0 {
		return a.Sample
	}
	return 10
}

// readAction reads a reply as an action and its argument, ok false for an answer.
func readAction(text string) (action, arg string, ok bool) {
	first, rest, _ := strings.Cut(text, "\n")
	word, inline, _ := strings.Cut(strings.TrimSpace(first), " ")
	switch word {
	case ActionLook, ActionValues:
		if rest != "" || strings.TrimSpace(inline) == "" {
			return "", "", false
		}
		return word, strings.TrimSpace(inline), true
	case ActionTry:
		draft := strings.TrimSpace(strings.TrimSpace(inline) + "\n" + rest)
		return word, draft, draft != ""
	}
	return "", "", false
}

// actionInstructions follow the card when Options.Actions is set.
const actionInstructions = `
# Before answering, you may reply with one of these instead, and its result comes back:
#   look NAME       what a relation, module or label holds
#   values TARGET   some values of one argument or property, such as %s
#   try             on its own line, then a draft: it runs on a few rows, and says why when none come back
# Any other reply is your answer.
`

// act runs a reply's action and writes what goes back to the model.
func act(ctx context.Context, action, arg string, v *ns.Vocabulary, o Options) string {
	prober, _ := o.FrontEnd.(Prober)
	switch action {
	case ActionLook:
		var text string
		var err error
		if prober != nil {
			text, err = prober.Look(v, arg)
		} else {
			text, err = look(v, arg)
		}
		if err != nil {
			return err.Error()
		}
		return text
	case ActionValues:
		var probe string
		var err error
		if prober != nil {
			probe, err = prober.Probe(v, arg, o.Actions.sample())
		} else {
			probe, err = valuesProgram(v, arg, o.Actions.sample())
		}
		if err != nil {
			return err.Error()
		}
		return run(ctx, probe, v, o, "values of "+arg)
	}
	return run(ctx, arg, v, o, "it")
}

// run runs a draft as an answer is judged, with at most Sample rows, and writes what came back: the
// rows, or why there were none, in the draft's language.
func run(ctx context.Context, text string, v *ns.Vocabulary, o Options, what string) string {
	o.Pack = nil // a look at the data isn't an answer the pack's Check judges
	try, prog, err := check(ctx, Try{Text: text, explain: func(err error) string { return Explain(text, err) }}, v, o, o.Actions.sample())
	if err != nil {
		return strings.TrimRight(try.explain(err), "\n")
	}
	if o.Base == nil {
		return "it checks; with no data to run on, there are no rows to show"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s answered %s:", what, rowCount(len(prog.Rows), o.Actions.sample()))
	cols := prog.Query.Columns()
	for _, r := range prog.Rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = quotedValue(r.Bind[c])
		}
		b.WriteString("\n  " + strings.Join(vals, ", "))
	}
	return b.String()
}

func rowCount(n, sample int) string {
	switch {
	case n == sample:
		return fmt.Sprintf("its first %d rows", n)
	case n == 1:
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

// quotedValue writes a value as a draft would: text quoted, so "" and a number read apart, and absent
// by name.
func quotedValue(v ns.Value) string {
	switch {
	case v.Absent:
		return "absent"
	case v.Num != nil && v.BaseUnit != "" && !strings.HasSuffix(v.S, v.BaseUnit):
		return v.S + v.BaseUnit
	case v.Num != nil:
		return v.S
	}
	return strconv.Quote(v.S)
}

// look is jaala's look: a member's card line with its whole doc, or a module's members.
func look(v *ns.Vocabulary, name string) (string, error) {
	e, err := v.Lookup(name)
	if err != nil {
		return "", err
	}
	if e.Kind == ns.EntryModule {
		var b strings.Builder
		if err := cardModule(&b, v, e.Path); err != nil {
			return "", err
		}
		return strings.TrimSpace(b.String()), nil
	}
	text := strings.TrimSpace(cardLine(v, e))
	if doc := strings.TrimSpace(e.Doc); strings.Contains(doc, "\n") {
		text += "\n" + doc
	}
	return text, nil
}

// valuesProgram is jaala's values: target is a relation and one of its arguments, by label or
// position from 0 (pkg.license, pkg.1), and the program lists that argument's distinct values.
func valuesProgram(v *ns.Vocabulary, target string, limit int) (string, error) {
	i := strings.LastIndexByte(target, '.')
	if i <= 0 {
		return "", fmt.Errorf("values %s: name a relation and one of its arguments, such as rel.arg", target)
	}
	rel, arg := target[:i], target[i+1:]
	e, err := v.Lookup(rel)
	if err != nil {
		return "", err
	}
	if e.Kind == ns.EntryModule {
		return "", fmt.Errorf("values %s: %s is a module; look %s says what it holds", target, rel, rel)
	}
	sig := e.Args
	at := -1
	for j, a := range sig {
		if a.Name == arg {
			at = j
		}
	}
	if n, err := strconv.Atoi(arg); err == nil && n >= 0 && n < len(sig) {
		at = n
	}
	if at < 0 {
		names := make([]string, len(sig))
		for j, a := range sig {
			names[j] = a.Name
		}
		return "", fmt.Errorf("values %s: %s's arguments are %s", target, rel, strings.Join(names, ", "))
	}
	args := make([]string, len(sig))
	for j := range args {
		args[j] = "_"
	}
	args[at] = "?v"
	return fmt.Sprintf("%s(%s) => ?v limit %d", rel, strings.Join(args, ", "), limit), nil
}

// errAction is draft's error for a reply that was an action, which Translate answers and goes on.
var errAction = errors.New("an action, not an answer")

// actionGrammar wraps a draft's grammar so a reply is an action or a draft: the draft grammar's root
// becomes program, and a NAME or TARGET is a dotted name of nameMax characters at most, which look
// and values check.
func actionGrammar(grammar string) string {
	if grammar == "" {
		return ""
	}
	return gbnf.Render([]gbnf.Rule{
		{Name: "root", E: gbnf.Alt{gbnf.Seq{gbnf.Lit("look "), gbnf.Ref("target")}, gbnf.Seq{gbnf.Lit("values "), gbnf.Ref("target")},
			gbnf.Seq{gbnf.Lit("try\n"), gbnf.Ref("program")}, gbnf.Ref("program")}},
		{Name: "target", E: gbnf.Seq{gbnf.Class{Spec: "A-Za-z_", Sample: "aPx_"}, gbnf.UpTo(gbnf.Class{Spec: "A-Za-z0-9_.", Sample: "ae1._"}, nameMax-1)}},
	}) + rootRule.ReplaceAllString(grammar, "program ::=")
}

var rootRule = regexp.MustCompile(`(?m)^root ::=`)

// actionsPrefix is actionInstructions with a values target written as the model's language writes one.
func actionsPrefix(fe FrontEnd) string {
	target := "relation.argument"
	if p, ok := fe.(Prober); ok {
		target = p.ValuesTarget()
	}
	return fmt.Sprintf(actionInstructions, target)
}
