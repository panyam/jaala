package nl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A Model writes text from a prompt (#325). nl runs no model: a host wraps whichever it uses, a local
// one through a client such as nlmodels' (#330) or a hosted one through its SDK, and Translate calls it once
// per try. Complete must honour ctx's cancellation. An error fails that try and moves Translate on to
// the fallback model, since retrying the same call rarely helps.
type Model interface {
	// Name says which model answered, for the Trace and Program.Model.
	Name() string
	Complete(ctx context.Context, p Prompt) (Completion, error)
}

// A Prompt is what a Model is asked: a Prefix that is the same for every question over one vocabulary,
// so a client can send it as a cached prefix or system prompt, then the conversation so far.
type Prompt struct {
	Prefix string
	// Turns start with the question (and any grounded names), then alternate the model's drafts and
	// what was wrong with each.
	Turns []Turn
	// Grammar is GBNF holding the reply to a program over the vocabulary (GrammarFor, #324), for a
	// Model whose decoder takes one, as nlmodels' llama.cpp client does. A Model that can't use it
	// ignores it: every draft is checked anyway, so the grammar only saves tries. "" when
	// Options.NoGrammar is set.
	Grammar string
}

// A Turn is one message of a Prompt's conversation.
type Turn struct {
	Role Role
	Text string
}

// A Role says who wrote a Turn.
type Role string

// The roles of a Turn.
const (
	RoleUser  Role = "user"  // the question, or what was wrong with the last draft
	RoleModel Role = "model" // a draft the model wrote
)

// A Completion is a Model's reply and what it cost. TokensCached counts the prompt tokens the model's
// server answered from its cache, 0 when it doesn't say.
type Completion struct {
	Text                              string
	TokensIn, TokensOut, TokensCached int
}

// A Grounder finds the values a question's names stand for (#325), the way Declaire's name.find turns
// a short name into ranked ids, so a model is handed real values instead of spelling them. It is the
// host's: nl can't tell which words of a question are names. An error fails Translate.
type Grounder interface {
	Ground(ctx context.Context, question string) ([]Candidate, error)
}

// A Candidate is a name from the question and the values it may stand for, likeliest first.
type Candidate struct {
	Name   string
	Values []string
}

// Options tune Translate. The zero value tries the model three times and only checks the program.
type Options struct {
	// MaxTries is how many drafts each model gets; 3 when 0.
	MaxTries int
	// Fallback, when set, is asked once Model runs out of tries, repeats a draft it was told was wrong,
	// or writes a program that checks and answers nothing over Base. It starts a fresh conversation
	// rather than reading Model's drafts.
	Fallback Model
	// Base, when set, evaluates a program that checks. An evaluation error is fed back as a check
	// error is, and an empty answer goes to Fallback, or back to the model when no fallback is left.
	// Without it, a program that checks is accepted.
	Base *datalog.Base
	// Eval are the options Base is evaluated with, such as a Budget.
	Eval []datalog.Option
	// Grammar replaces the grammar every Prompt carries, GrammarFor(v) by default, such as with
	// GrammarFor(v, GoalOnly()) for a small model. NoGrammar sends none.
	Grammar   string
	NoGrammar bool
}

// A Program is what Translate accepted: its text, as the model wrote it less any Markdown fence, the
// parsed Query, its rows when Options.Base evaluated it, and the model that wrote it.
type Program struct {
	Text  string
	Query datalog.Query
	Rows  []datalog.Row
	Model string
}

// A Trace is every try Translate made, in order, whether or not one was accepted.
type Trace struct {
	Tries []Try
}

// A Try is one draft: the model, its text, why it was refused (nil and "" when accepted), what it cost
// and how long the model took.
type Try struct {
	Model string
	Text  string
	Err   error
	// Kind sorts Err: a CheckError's Kind ("unknown", "arity", ...), or "parse" for a parse error,
	// "eval" for an evaluation error that isn't a CheckError, "empty" for an answer with no rows,
	// "repeat" for a draft the model already wrote in this conversation (Err wraps the first one's),
	// and "model" for a Model that failed.
	Kind                              string
	TokensIn, TokensOut, TokensCached int
	Latency                           time.Duration
	// Report is what evaluating the draft over Options.Base did (datalog.Explain), when it was
	// evaluated: its work per rule and literal, and for an empty answer, Report.Empty.
	Report *datalog.Report
}

// The Try kinds that aren't a CheckError's.
const (
	KindParse  = "parse"
	KindEval   = "eval"
	KindEmpty  = "empty"
	KindRepeat = "repeat"
	KindModel  = "model"
)

// instructions follow the card in every Prompt's Prefix.
const instructions = `
# Write one program answering the question, using only the members above: rules, if any, then
# body => columns. Variables are ?name, strings are "quoted". Reply with the program alone.
`

// Translate asks m for a program answering question over v (#325): it drafts, parses and checks the
// draft (datalog.Validate), and feeds an error back with Explain's text for m to fix, up to
// o.MaxTries times, then hands the question to o.Fallback the same way. A draft the model already
// wrote is refused unchecked and ends its turn (#340: a small model resending one draft spent every
// retry on it). With o.Base, a program that checks is evaluated too, and one answering nothing goes to
// the fallback, or, when there is none left, back to the model to try other members or values. g,
// when not nil, grounds the question's names first. The Trace holds every try, also when Translate fails, and the error
// then wraps the last try's.
func Translate(ctx context.Context, question string, v *ns.Vocabulary, g Grounder, m Model, o Options) (Program, Trace, error) {
	var tr Trace
	card, err := Card(v)
	if err != nil {
		return Program{}, tr, err
	}
	ask := question
	if g != nil {
		cands, err := g.Ground(ctx, question)
		if err != nil {
			return Program{}, tr, fmt.Errorf("nl: grounding the question: %w", err)
		}
		ask += grounded(cands)
	}
	tries := o.MaxTries
	if tries <= 0 {
		tries = 3
	}
	prefix := card + instructions
	grammar := o.Grammar
	if o.NoGrammar {
		grammar = ""
	} else if grammar == "" {
		if grammar, err = GrammarFor(v); err != nil {
			return Program{}, tr, err
		}
	}
	models := []Model{m}
	if o.Fallback != nil {
		models = append(models, o.Fallback)
	}
	var last error
	for k, model := range models {
		turns := []Turn{{RoleUser, ask}}
		if k > 0 && errors.Is(last, errEmpty) {
			turns[0].Text += "\n\nA program written for this answered nothing; the question may need other members or values."
		}
		lastModel := k == len(models)-1
		refused := map[string]error{}
		for range tries {
			if err := ctx.Err(); err != nil {
				return Program{}, tr, err
			}
			try, p, err := draft(ctx, model, Prompt{Prefix: prefix, Turns: turns, Grammar: grammar}, v, o, refused)
			tr.Tries = append(tr.Tries, try)
			if err == nil {
				return p, tr, nil
			}
			last = err
			if try.Kind == KindModel || try.Kind == KindRepeat || try.Kind == KindEmpty && !lastModel {
				break
			}
			refused[try.Text] = err
			fix := "Write the whole program again, fixed."
			if try.Kind == KindEmpty {
				fix = "Write the whole program again, with other members or values."
			}
			turns = append(turns, Turn{RoleModel, try.Text}, Turn{RoleUser, Explain(try.Text, err) + "\n\n" + fix})
		}
	}
	return Program{}, tr, fmt.Errorf("nl: no program checked after %d tries: %w", len(tr.Tries), last)
}

// errEmpty is a program that checked and answered nothing.
var errEmpty = errors.New("the program answered no rows")

// An EmptyAnswer is the error of a draft that checked and answered nothing over Options.Base, with
// where it lost its last binding (#343), which its message carries for the model to retry from.
// errors.As finds it in a Try's Err.
type EmptyAnswer struct {
	Why *datalog.EmptyReport // nil when every literal passed something on
}

func (e *EmptyAnswer) Error() string {
	if e.Why == nil {
		return errEmpty.Error()
	}
	return errEmpty.Error() + ":\n" + e.Why.String()
}

// Is makes an EmptyAnswer the empty answer Translate moves to the fallback on.
func (e *EmptyAnswer) Is(target error) bool { return target == errEmpty }

// draft asks model once, then parses, checks and, over o.Base, evaluates what it wrote, unless it is
// a draft refused already, whose error it returns again.
func draft(ctx context.Context, model Model, p Prompt, v *ns.Vocabulary, o Options, refused map[string]error) (Try, Program, error) {
	try := Try{Model: model.Name()}
	start := time.Now()
	c, err := model.Complete(ctx, p)
	try.Latency = time.Since(start)
	if err != nil {
		try.Err, try.Kind = err, KindModel
		return try, Program{}, fmt.Errorf("model %s: %w", try.Model, err)
	}
	try.TokensIn, try.TokensOut, try.TokensCached = c.TokensIn, c.TokensOut, c.TokensCached
	try.Text = unfence(c.Text)
	refuse := func(err error, kind string) (Try, Program, error) {
		try.Err, try.Kind = err, kind
		return try, Program{}, err
	}
	if err, ok := refused[try.Text]; ok {
		return refuse(fmt.Errorf("the same draft as before: %w", err), KindRepeat)
	}
	q, err := datalog.Parse(try.Text)
	if err != nil {
		return refuse(err, KindParse)
	}
	if err := datalog.Validate(q, v); err != nil {
		return refuse(err, kindOf(err, KindEval))
	}
	prog := Program{Text: try.Text, Query: q, Model: try.Model}
	if o.Base == nil {
		return try, prog, nil
	}
	try.Report = &datalog.Report{}
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, o.Base, append(o.Eval[:len(o.Eval):len(o.Eval)], datalog.Explain(try.Report))...)
	if err != nil {
		return refuse(err, kindOf(err, KindEval))
	}
	if len(rows) == 0 {
		return refuse(&EmptyAnswer{Why: try.Report.Empty}, KindEmpty)
	}
	prog.Rows = rows
	return try, prog, nil
}

// kindOf is err's CheckError Kind, or otherwise when it isn't one.
func kindOf(err error, otherwise string) string {
	var ce *datalog.CheckError
	if errors.As(err, &ce) {
		return string(ce.Kind)
	}
	return otherwise
}

// grounded lists the candidates after the question, for the model to use their values as written.
func grounded(cands []Candidate) string {
	if len(cands) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nValues the question's names may stand for, likeliest first (use them exactly as written):")
	for _, c := range cands {
		quoted := make([]string, len(c.Values))
		for i, v := range c.Values {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, "\n%s: %s", c.Name, strings.Join(quoted, ", "))
	}
	return b.String()
}

// unfence is the first Markdown code block in text, or text itself when it holds none, trimmed.
func unfence(text string) string {
	start := strings.Index(text, "```")
	if start < 0 {
		return strings.TrimSpace(text)
	}
	body := text[start+3:]
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	if nl := strings.IndexByte(body, '\n'); nl >= 0 && !strings.ContainsAny(body[:nl], "(=") {
		body = body[nl+1:] // the fence's language tag
	}
	return strings.TrimSpace(body)
}
