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

// Actions let a model look at the data before it answers (#393), and each one's result comes back to
// it. The translator offers them as tools (AsTools); a loop reading them from the reply, which a small
// model can write under a grammar, takes a reply that is one of
//
//	look NAME       a member's signature and doc, or a module's members (a label's, in Cypher)
//	values TARGET   up to Sample of the values an argument holds (pkg.license; Package.license)
//	try             on its first line, then a draft: run with Sample rows at most, its rows or why
//	                it answered none sent back
//	find NAME       the values a name may stand for, from the Grounder, when there is one
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
	// Compact sends the model, in place of the conversation, one turn holding the question and
	// what it has found: each action with its result, each refused answer on a line, and the last
	// refusal whole. A small model's drafts get worse as its context grows (#393).
	Compact bool
	// Ledger shows the model, on every call after the conversation, what its actions found and each
	// draft it tried with what that did (Conversation.Ledger, #514), so a small model keeps them in
	// view without rereading the transcript. With Compact, the one turn leaves those lines to it. The
	// translator shows it; a host's own loop reads Conversation.Ledger.
	Ledger bool
	// Build lets the model write its program a literal at a time (#514): add LITERAL appends one to
	// the conversation's body and answers with the body's rows so far, or why it has none, and drop
	// takes the last one off. An answer may then be the goal's tail alone (=> ?p), which follows the
	// body. A small model finds a wrong value at the literal that wrote it, before it builds on it.
	// Each add or drop is a step. It needs jaala's own language: a FrontEnd turns it off.
	Build bool
	// AsTools says the caller offers the actions as tools of its own (Session.Tools), as nlmodels'
	// translator does on a chakra Runner (#496), rather than reading them from a reply: the prefix
	// leaves out Prompts.Actions, and ActionGrammar is Grammar.
	AsTools bool
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
	ActionFind   = "find"
	ActionAdd    = "add"  // Actions.Build: append a literal to the body
	ActionDrop   = "drop" // Actions.Build: take the body's last literal off
)

// replies says the model asks for an action in its reply: Actions are set and not AsTools.
func (a *Actions) replies() bool { return a != nil && !a.AsTools }

// builds says the model may write its program a literal at a time: Actions.Build in jaala's language.
func (a *Actions) builds(fe FrontEnd) bool { return a != nil && a.Build && fe == nil }

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

// ReadAction reads a reply as an action and its argument, a Markdown fence taken off, for a loop whose
// model writes its actions (Session.TextActions). ok is false for an answer, which goes to
// Conversation.Judge, and so is a malformed action: look with no name, or with lines after it, is
// judged as a draft.
func ReadAction(reply string) (action, arg string, ok bool) { return readAction(unfence(reply)) }

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
	case ActionFind, ActionAdd:
		if rest != "" || strings.TrimSpace(inline) == "" {
			return "", "", false
		}
		return word, strings.TrimSpace(inline), true
	case ActionDrop:
		if rest != "" || strings.TrimSpace(inline) != "" {
			return "", "", false
		}
		return word, "", true
	}
	return "", "", false
}

// actionInstructions follow the card when Options.Actions is set.
const actionInstructions = `
# Before answering, you may reply with one of these instead, and its result comes back:
#   look NAME       what a relation, module or label holds
#   values TARGET   some values of one argument or property, such as %s
#   try             on its own line, then a draft: it runs on a few rows, and says why when none come back
%s# Any other reply is your answer.
`

// buildInstruction is actionInstructions' lines for add and drop, under Actions.Build.
const buildInstruction = "#   add LITERAL     add one literal to the program you are building, and see its rows so far\n" +
	"#   drop            take the last literal off it\n" +
	"# Once the body is right, answer with => and the columns alone, such as => ?p\n"

// findInstruction is actionInstructions' line for find, when there is a Grounder.
const findInstruction = "#   find NAME       the ids or values a name from the question may stand for\n"

// act runs a reply's action and writes what goes back to the model, with the text values it saw the
// data hold, which a later draft's guess may be repaired from.
func act(ctx context.Context, action, arg string, v *ns.Vocabulary, o Options) (string, []string) {
	switch action {
	case ActionFind:
		if o.grounder == nil {
			return "there is nothing to find names with here; look and values say what the data holds", nil
		}
		text, seen, err := find(ctx, o, arg)
		switch {
		case err != nil:
			return err.Error(), nil
		case text == "":
			return fmt.Sprintf("nothing found for %q", arg), nil
		}
		return text, seen
	case ActionLook:
		text, err := Look(v, o.FrontEnd, arg)
		if err == nil {
			return text, nil
		}
		return lookFor(ctx, v, o, arg, err)
	case ActionValues:
		probe, err := Probe(v, o.FrontEnd, arg, o.Actions.sample())
		if err != nil {
			return err.Error(), nil
		}
		return run(ctx, probe, v, o, "values of "+arg, o.Actions.sample())
	}
	return run(ctx, arg, v, o, "it", o.Actions.sample())
}

// run runs a draft as an answer is judged, with at most limit rows, and writes what came back: the
// rows, or why there were none, in the draft's language, with the text values in the rows.
func run(ctx context.Context, text string, v *ns.Vocabulary, o Options, what string, limit int) (string, []string) {
	out, seen, err := runRows(ctx, text, v, o, what, limit)
	if err != nil {
		return err.Error(), nil
	}
	return out, seen
}

// runRows is run with a refusal as its error, whose text is the draft's explanation.
func runRows(ctx context.Context, text string, v *ns.Vocabulary, o Options, what string, limit int) (string, []string, error) {
	o.Pack = nil // a look at the data isn't an answer the pack's Check judges
	try, prog, err := check(ctx, Try{Text: text, explain: func(err error) string { return Explain(text, err) }}, v, o, limit)
	if err != nil {
		return "", nil, errors.New(strings.TrimRight(try.explain(err), "\n"))
	}
	if o.Base == nil {
		return "it checks; with no data to run on, there are no rows to show", nil, nil
	}
	text, seen := answeredText(what, prog, limit)
	return text, seen, nil
}

// answeredText writes what a draft answered: what was run, how many rows, and the rows, with the text
// values in them.
func answeredText(what string, prog Program, limit int) (string, []string) {
	var seen []string
	var b strings.Builder
	fmt.Fprintf(&b, "%s answered %s:", what, rowCount(len(prog.Rows), limit))
	cols := prog.Query.Columns()
	for _, r := range prog.Rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = quotedValue(r.Bind[c])
			if val := r.Bind[c]; !val.Absent && val.Num == nil {
				seen = append(seen, val.S)
			}
		}
		b.WriteString("\n  " + strings.Join(vals, ", "))
	}
	return b.String(), seen
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

// Look is the look action outside a translation, for a tool an agent calls (frontends/mcp, #393): name
// described in fe's terms when fe is a Prober, and otherwise as jaala's card describes it, whole. fe
// may be nil.
func Look(v *ns.Vocabulary, fe FrontEnd, name string) (string, error) {
	if p, ok := fe.(Prober); ok {
		return p.Look(v, name)
	}
	return look(v, name)
}

// Probe is the values action's draft outside a translation: a program, in fe's language when fe is a
// Prober and jaala's otherwise, answering up to limit of target's distinct values. fe may be nil.
func Probe(v *ns.Vocabulary, fe FrontEnd, target string, limit int) (string, error) {
	if p, ok := fe.(Prober); ok {
		return p.Probe(v, target, limit)
	}
	return valuesProgram(v, target, limit)
}

// lookMatches caps the members a look for a plain word lists.
const lookMatches = 10

// lookFor answers a look whose name is no member, module or label (#512): a model takes look for a
// search, and sends look interface. Under jaala's own language it lists the members whose name or
// first doc line holds the word, names first; with nothing listed and a Grounder, it answers as find
// does; and otherwise it says what look takes, after err. A Prober's front end lists nothing, since
// jaala's names aren't its language.
func lookFor(ctx context.Context, v *ns.Vocabulary, o Options, word string, err error) (string, []string) {
	pr := o.Pack.prompts()
	if _, ok := o.FrontEnd.(Prober); !ok {
		if lines := membersMentioning(v, word); len(lines) > 0 {
			return fmt.Sprintf(pr.LookFound, word) + "\n" + strings.TrimRight(strings.Join(lines, ""), "\n"), nil
		}
	}
	if o.grounder != nil {
		if text, seen, ferr := find(ctx, o, word); ferr == nil && text != "" {
			return text, seen
		}
	}
	return err.Error() + "\n" + pr.LookNothing, nil
}

// find asks the Grounder what name may stand for: the text that goes back, "" when it found nothing,
// and the values it showed.
func find(ctx context.Context, o Options, name string) (string, []string, error) {
	cands, err := o.grounder.Ground(ctx, name)
	if err != nil {
		return "", nil, err
	}
	var seen []string
	for _, c := range cands {
		seen = append(seen, c.Values...)
	}
	text := strings.TrimSpace(grounded(o.Pack.prompts().Grounded, cands))
	if text == "" {
		seen = nil
	}
	return text, seen, nil
}

// membersMentioning are the card lines of v's members whose path or first doc line holds word, any
// case, the path matches first, at most lookMatches.
func membersMentioning(v *ns.Vocabulary, word string) []string {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return nil
	}
	var byName, byDoc []string
	var walk func(module string)
	walk = func(module string) {
		members, err := v.Members(module)
		if err != nil {
			return
		}
		for _, m := range members {
			if m.Kind == ns.EntryModule {
				walk(m.Path)
				continue
			}
			switch {
			case strings.Contains(strings.ToLower(m.Path), word):
				byName = append(byName, cardLine(v, m))
			case strings.Contains(strings.ToLower(firstLine(m.Doc)), word):
				byDoc = append(byDoc, cardLine(v, m))
			}
		}
	}
	walk("")
	out := append(byName, byDoc...)
	if len(out) > lookMatches {
		out = out[:lookMatches]
	}
	return out
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

// actionGrammar wraps a draft's grammar so a reply is an action or a draft: the draft grammar's root
// becomes program, a NAME or TARGET is a dotted name of nameMax characters at most, which look and
// values check, and with find a phrase of words, which the Grounder reads.
func actionGrammar(grammar string, find, build bool) string {
	if grammar == "" {
		return ""
	}
	root := gbnf.Alt{gbnf.Seq{gbnf.Lit("look "), gbnf.Ref("target")}, gbnf.Seq{gbnf.Lit("values "), gbnf.Ref("target")},
		gbnf.Seq{gbnf.Lit("try\n"), gbnf.Ref("program")}}
	if build {
		root = append(root, gbnf.Seq{gbnf.Lit("add "), gbnf.Ref("literal")}, gbnf.Lit("drop"))
	}
	rules := []gbnf.Rule{
		{Name: "target", E: gbnf.Seq{gbnf.Class{Spec: "A-Za-z_", Sample: "aPx_"}, gbnf.UpTo(gbnf.Class{Spec: "A-Za-z0-9_.", Sample: "ae1._"}, nameMax-1)}},
	}
	if find {
		root = append(root, gbnf.Seq{gbnf.Lit("find "), gbnf.Ref("phrase")})
		rules = append(rules, gbnf.Rule{Name: "phrase", E: gbnf.Seq{gbnf.Class{Spec: "A-Za-z0-9_", Sample: "aP1_"}, gbnf.UpTo(gbnf.Class{Spec: "A-Za-z0-9_. /-", Sample: "a1. /"}, 2*nameMax-1)}})
	}
	root = append(root, gbnf.Ref("program"))
	return gbnf.Render(append([]gbnf.Rule{{Name: "root", E: root}}, rules...)) + rootRule.ReplaceAllString(grammar, "program ::=")
}

// finalGrammar is a draft's grammar for the reply after the last step (#512): the draft, with the
// try line before it allowed and dropped when it is judged (Session.reply). A model whose history
// holds its own try replies writes one more, and under the draft's grammar alone the word ran into
// the first name (tryagainlater(), trygraphreach(...)).
func finalGrammar(grammar string) string {
	if grammar == "" {
		return ""
	}
	root := gbnf.Seq{gbnf.Opt(gbnf.Lit("try\n")), gbnf.Ref("program")}
	return gbnf.Render([]gbnf.Rule{{Name: "root", E: root}}) + rootRule.ReplaceAllString(grammar, "program ::=")
}

// withoutTry is text with a first line of try alone taken off, the rest unchanged.
func withoutTry(text string) string {
	first, rest, ok := strings.Cut(text, "\n")
	if ok && strings.TrimSpace(first) == ActionTry && strings.TrimSpace(rest) != "" {
		return strings.TrimSpace(rest)
	}
	return text
}

var rootRule = regexp.MustCompile(`(?m)^root ::=`)

// buildGrammar is a draft's grammar under Actions.Build (#514): a whole program, or a goal's tail
// alone (=> ?p), which Conversation.Judge puts after the body the adds built. It reads the draft
// grammar's own column and tail rules, so the tail is one a goal could end with.
func buildGrammar(grammar string) string {
	if grammar == "" {
		return ""
	}
	item := gbnf.Alt{gbnf.Ref("column"), gbnf.Ref("string"), gbnf.Ref("unumber")}
	tail := gbnf.Seq{gbnf.Lit("=> "), item, gbnf.Star(gbnf.Seq{gbnf.Lit(", "), item}), gbnf.Ref("tail")}
	root := gbnf.Alt{gbnf.Ref("whole"), tail}
	return gbnf.Render([]gbnf.Rule{{Name: "root", E: root}}) + rootRule.ReplaceAllString(grammar, "whole ::=")
}

// actionsPrefix is actionInstructions with a values target written as the model's language writes one,
// and find's line when there is a Grounder to find with.
func actionsPrefix(fe FrontEnd, find, build bool, pr Prompts) string {
	target := "relation.argument"
	if p, ok := fe.(Prober); ok {
		target = p.ValuesTarget()
	}
	line := ""
	if find {
		line = pr.Find
	}
	if build {
		line += pr.Build
	}
	return fmt.Sprintf(pr.Actions, target, line)
}
