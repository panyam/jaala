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
	// Draw is which of a Vote's extra drafts this prompt asks for, from 1, and 0 for every other
	// prompt. A Model that samples should vary its seed by it: asked the same prompt alike, a greedy
	// decoder writes the same draft, and the vote agrees by construction.
	Draw int
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
	// Pack, when set, is the host's domain (#344): its part of the prefix follows the card, its
	// examples are checked before any model is asked, its Grounder grounds the question when
	// Translate's is nil, and its Check judges each answer.
	Pack *Pack
	// FrontEnd, when set, is the language the model writes in place of jaala's (#354): the prompt is
	// its card, instructions and examples (the pack's About and Card still follow, its jaala examples
	// don't), each draft is compiled before it is checked, and every refusal goes back in its terms.
	// Grammar defaults to its Grammar.
	FrontEnd FrontEnd
	// Actions, when set, let a model look at the vocabulary and the data, and try drafts, before it
	// answers (#393). The grammar sent becomes the actions' around the draft's.
	Actions *Actions
	// Vote, when set, draws more answers once a model has answered, and keeps the one most of them
	// agree with (#393). Program.Confidence says how many agreed.
	Vote *Vote
	// Escalate, when above 0, moves an answer whose drafts agree less than this share
	// (Confidence.Agreement) on to Fallback, recorded in Trace.Escalations. When the fallback answers
	// nothing better, Translate returns the first such answer rather than failing. It needs a Vote:
	// without one every answer agrees with itself.
	Escalate float64
	// Budget caps the tokens and time Translate spends on the question.
	Budget Budget

	grounder Grounder // the Grounder a find action asks
}

// A Program is what Translate accepted: its text, as the model wrote it less any Markdown fence, the
// parsed Query, its rows when Options.Base evaluated it, and the model that wrote it.
type Program struct {
	Text  string
	Query datalog.Query
	Rows  []datalog.Row
	Model string
	// Language is the front end's the model wrote Text in, "" for jaala's, and Compiled the jaala
	// program Text compiled to, which Query parses.
	Language, Compiled string
	// Confidence is what Translate knows about how far to trust it.
	Confidence Confidence
}

// A Trace is every try Translate made, in order, whether or not one was accepted.
type Trace struct {
	Tries []Try
	// Steps are the actions the models asked for before answering (Options.Actions), in order.
	Steps []Step
	// Votes are the extra drafts Options.Vote drew, in order, each judged as an answer is.
	Votes []Try
	// Escalations are the answers moved on from for agreeing too little (Options.Escalate).
	Escalations []Escalation
}

// A Try is one draft: the model, its text, why it was refused (nil and "" when accepted), what it cost
// and how long the model took.
type Try struct {
	Model string
	Text  string
	// Language is the front end's the model wrote Text in, "" for jaala's.
	Language string
	Err      error
	// Kind sorts Err: a CheckError's Kind ("unknown", "arity", ...), or "parse" for a parse error,
	// "eval" for an evaluation error that isn't a CheckError, "empty" for an answer with no rows,
	// "repeat" for a draft the model already wrote in this conversation (Err wraps the first one's),
	// "answer" for one the Pack's Check refused, "model" for a Model that failed, and, with a
	// FrontEnd, "compile" for a draft it refused to compile, or the kind it names (Cypher's "gap").
	Kind                              string
	TokensIn, TokensOut, TokensCached int
	Latency                           time.Duration
	// Report is what evaluating the draft over Options.Base did (datalog.Explain), when it was
	// evaluated: its work per rule and literal, and for an empty answer, Report.Empty.
	Report *datalog.Report

	explain func(error) string // how Err goes back to the model, in the draft's language
	// Repair, for a draft the harness rewrote itself (Options.Actions), says what it changed: a
	// quoted value the data holds in another spelling the conversation had seen. Text is the
	// rewritten draft, and the try before it the model's own.
	Repair string

	// action and result are set for a reply that was an action (Options.Actions), which Translate
	// records as a Step rather than a Try, and seen holds the text values its result showed.
	action, result string
	seen           []string
}

// The Try kinds that aren't a CheckError's.
const (
	KindParse  = "parse"
	KindEval   = "eval"
	KindEmpty  = "empty"
	KindRepeat = "repeat"
	KindModel  = "model"
	KindAnswer = "answer"
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
// when not nil, grounds the question's names first, and o.Pack's Grounder does when g is nil. A pack
// whose examples don't check fails Translate before any model is asked. The Trace holds every try, also when Translate fails, and the error
// then wraps the last try's.
func Translate(ctx context.Context, question string, v *ns.Vocabulary, g Grounder, m Model, o Options) (Program, Trace, error) {
	var tr Trace
	if o.Pack != nil && g == nil {
		g = o.Pack.Grounder
	}
	o.grounder = g
	prefix, err := promptPrefix(v, o)
	if err != nil {
		return Program{}, tr, err
	}
	spend := &spending{budget: o.Budget, start: time.Now()}
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
	grammar := o.Grammar
	switch {
	case o.NoGrammar:
		grammar = ""
	case grammar != "":
	case o.FrontEnd != nil:
		grammar, err = o.FrontEnd.Grammar(v)
	default:
		grammar, err = GrammarFor(v)
	}
	if err != nil {
		return Program{}, tr, err
	}
	answerGrammar := grammar
	if o.Actions != nil {
		grammar = actionGrammar(grammar, g != nil)
	}
	models := []Model{m}
	if o.Fallback != nil {
		models = append(models, o.Fallback)
	}
	var last error
	var held *Program // an answer escalated past, kept in case nothing better comes
	mem := &memory{}  // what the actions have shown, across models, since it is about the data
	compact := o.Actions != nil && o.Actions.Compact
	for k, model := range models {
		mem.facts, mem.last = nil, "" // a fallback starts a fresh conversation
		turns := []Turn{{RoleUser, ask}}
		if k > 0 && errors.Is(last, errEmpty) {
			turns[0].Text += "\n\nA program written for this answered nothing; the question may need other members or values."
		}
		lastModel := k == len(models)-1
		refused := map[string]error{}
		asked := map[string]bool{}
		steps := 0
		for t := 0; t < tries; {
			if err := ctx.Err(); err != nil {
				return Program{}, tr, err
			}
			if err := spend.check(); err != nil {
				return Program{}, tr, err
			}
			g, oo := grammar, o
			if o.Actions != nil && steps >= o.Actions.steps() {
				g, oo.Actions = answerGrammar, nil // out of steps: the next reply is the answer
			}
			sent := turns
			if compact {
				sent = mem.compact(turns[0].Text)
			}
			try, p, err := draft(ctx, model, Prompt{Prefix: prefix, Turns: sent, Grammar: g}, v, oo, refused)
			spend.add(try.TokensIn, try.TokensOut)
			if errors.Is(err, errAction) {
				result := try.result
				if asked[try.Text] {
					result = "You asked that already. " + result
				}
				asked[try.Text] = true
				steps++
				if steps == o.Actions.steps() {
					result += "\n\nThat was the last look: reply with your answer."
				}
				tr.Steps = append(tr.Steps, Step{Model: try.Model, Action: try.action, Text: try.Text, Result: result, After: len(tr.Tries),
					TokensIn: try.TokensIn, TokensOut: try.TokensOut, TokensCached: try.TokensCached, Latency: try.Latency})
				mem.see(try.seen)
				mem.facts = append(mem.facts, fact(try.Text, result))
				turns = append(turns, Turn{RoleModel, try.Text}, Turn{RoleUser, result})
				continue
			}
			t++
			if err != nil && o.Actions != nil && repairable(try) {
				// a guessed spelling of a value the conversation has seen: fixed here, not by the model
				if text, what, ok := mem.repair(try.Text, domainOf(try.Err, v)); ok {
					rtry, rp, rerr := check(ctx, Try{Model: try.Model, Language: try.Language, Text: text, Repair: what,
						explain: func(err error) string { return Explain(text, err) }}, v, o, 0)
					if rerr == nil {
						tr.Tries = append(tr.Tries, try)
						try, p, err = rtry, rp, nil
					}
				}
			}
			tr.Tries = append(tr.Tries, try)
			if err == nil {
				p.Confidence = Confidence{Drawn: 1, Agreed: 1, Ran: o.Base != nil, Judged: o.Pack != nil && o.Pack.Check != nil, Steps: steps}
				if o.Vote != nil {
					var votes []Try
					conf := p.Confidence
					p, votes = vote(ctx, model, Prompt{Prefix: prefix, Turns: sent, Grammar: answerGrammar}, p, v, o, spend)
					conf.Drawn, conf.Agreed = p.Confidence.Drawn, p.Confidence.Agreed
					p.Confidence = conf
					tr.Votes = append(tr.Votes, votes...)
				}
				if o.Escalate > 0 && p.Confidence.Agreement() < o.Escalate && k < len(models)-1 {
					tr.Escalations = append(tr.Escalations, Escalation{Model: model.Name(), Text: p.Text, Confidence: p.Confidence})
					if held == nil {
						held = &p
					}
					last = errLowAgreement
					break
				}
				return p, tr, nil
			}
			last = err
			if try.Kind == KindModel || try.Kind == KindRepeat || try.Kind == KindEmpty && !lastModel {
				break
			}
			refused[try.Text] = err
			what := "program"
			if o.FrontEnd != nil {
				what = "query"
			}
			fix := "Write the whole " + what + " again, fixed."
			if try.Kind == KindEmpty {
				fix = "Write the whole " + what + " again, with other members or values."
			}
			why := strings.TrimRight(try.explain(err), "\n")
			mem.facts = append(mem.facts, fact(try.Text, "refused ("+try.Kind+"): "+strings.Join(strings.Fields(why), " ")))
			mem.last = try.Text + "\n" + why + "\n\n" + fix
			turns = append(turns, Turn{RoleModel, try.Text}, Turn{RoleUser, why + "\n\n" + fix})
		}
	}
	if held != nil {
		return *held, tr, nil
	}
	return Program{}, tr, fmt.Errorf("nl: no program checked after %d tries: %w", len(tr.Tries), last)
}

// errLowAgreement is an answer whose vote agreed less than Options.Escalate asks.
var errLowAgreement = errors.New("the drafts agreed too little on an answer")

// promptPrefix is every question's Prefix: the card and instructions, jaala's or the front end's, then
// the pack's part, with the front end's examples in place of the pack's. Examples are checked first,
// so a broken one fails Translate before any model is asked.
func promptPrefix(v *ns.Vocabulary, o Options) (string, error) {
	fe := o.FrontEnd
	if fe == nil {
		card, err := Card(v)
		if err != nil {
			return "", err
		}
		prefix := card + instructions
		if o.Actions != nil {
			prefix += actionsPrefix(nil, o.grounder != nil)
		}
		if o.Pack != nil {
			if err := o.Pack.Validate(v); err != nil {
				return "", err
			}
			prefix += o.Pack.Prefix()
		}
		return prefix, nil
	}
	card, err := fe.Card(v)
	if err != nil {
		return "", err
	}
	examples := fe.Examples()
	for i, e := range examples {
		c, err := fe.Compile(v, e.Program)
		if err == nil {
			err = datalog.Validate(c.Query, v)
		}
		if err != nil {
			return "", fmt.Errorf("nl: the front end's example %d (%q): %w", i+1, e.Question, err)
		}
	}
	prefix := card + fe.Instructions()
	if o.Actions != nil {
		prefix += actionsPrefix(fe, o.grounder != nil)
	}
	if o.Pack != nil {
		p := *o.Pack
		p.Examples = examples
		prefix += p.Prefix()
	} else if len(examples) > 0 {
		prefix += (&Pack{Examples: examples}).Prefix()
	}
	return prefix, nil
}

// errEmpty is a program that checked and answered nothing.
var errEmpty = errors.New("the program answered no rows")

// An EmptyAnswer is the error of a draft that checked and answered nothing over Options.Base, with
// where it lost its last binding (#343), which its message carries for the model to retry from.
// errors.As finds it in a Try's Err.
type EmptyAnswer struct {
	Why *datalog.EmptyReport // nil when every literal passed something on
	// Hint is the Pack's Hint for Why: what the data holds where it matched nothing, "" for none.
	Hint string
}

func (e *EmptyAnswer) Error() string {
	if e.Why == nil {
		return errEmpty.Error()
	}
	s := errEmpty.Error() + ":\n" + e.Why.String()
	if e.Hint != "" {
		s += "\n" + strings.TrimRight(e.Hint, "\n")
	}
	return s
}

// hintFor is p's Hint for why, "" when there is no pack, no hint or nothing to explain.
func hintFor(ctx context.Context, p *Pack, why *datalog.EmptyReport) string {
	if p == nil || p.Hint == nil || why == nil {
		return ""
	}
	return p.Hint(ctx, why)
}

// Is makes an EmptyAnswer the empty answer Translate moves to the fallback on.
func (e *EmptyAnswer) Is(target error) bool { return target == errEmpty }

// draft asks model once, then parses, checks and, over o.Base, evaluates what it wrote, unless it is
// a draft refused already, whose error it returns again.
func draft(ctx context.Context, model Model, p Prompt, v *ns.Vocabulary, o Options, refused map[string]error) (Try, Program, error) {
	try := Try{Model: model.Name()}
	if o.FrontEnd != nil {
		try.Language = o.FrontEnd.Language()
	}
	start := time.Now()
	c, err := model.Complete(ctx, p)
	try.Latency = time.Since(start)
	if err != nil {
		try.Err, try.Kind = err, KindModel
		return try, Program{}, fmt.Errorf("model %s: %w", try.Model, err)
	}
	try.TokensIn, try.TokensOut, try.TokensCached = c.TokensIn, c.TokensOut, c.TokensCached
	try.Text = unfence(c.Text)
	try.explain = func(err error) string { return Explain(try.Text, err) }
	refuse := func(err error, kind string) (Try, Program, error) {
		try.Err, try.Kind = err, kind
		return try, Program{}, err
	}
	if o.Actions != nil {
		if action, arg, ok := readAction(try.Text); ok {
			try.action = action
			try.result, try.seen = act(ctx, action, arg, v, o)
			return try, Program{}, errAction
		}
	}
	if err, ok := refused[try.Text]; ok {
		return refuse(fmt.Errorf("the same draft as before: %w", err), KindRepeat)
	}
	return check(ctx, try, v, o, 0)
}

// check compiles or parses try's text, checks it and, over o.Base, evaluates it, with at most limit
// rows when limit isn't 0, and judges the answer by the pack's Check.
func check(ctx context.Context, try Try, v *ns.Vocabulary, o Options, limit int) (Try, Program, error) {
	refuse := func(err error, kind string) (Try, Program, error) {
		try.Err, try.Kind = err, kind
		return try, Program{}, err
	}
	prog := Program{Text: try.Text, Model: try.Model, Language: try.Language}
	if o.FrontEnd != nil {
		c, err := o.FrontEnd.Compile(v, try.Text)
		if c.Explain != nil {
			try.explain = c.Explain
		}
		if err != nil {
			kind := c.Kind
			if kind == "" {
				kind = KindCompile
			}
			return refuse(err, kind)
		}
		prog.Compiled, prog.Query = c.Program, c.Query
	} else {
		q, err := datalog.Parse(try.Text)
		if err != nil {
			return refuse(err, KindParse)
		}
		prog.Query = q
	}
	q := prog.Query
	if limit > 0 && (q.Limit == 0 || q.Limit > limit) {
		q.Limit = limit
	}
	if err := datalog.Validate(q, v); err != nil {
		return refuse(err, kindOf(err, KindEval))
	}
	if o.Base == nil {
		return judged(ctx, try, prog, o)
	}
	try.Report = &datalog.Report{}
	opts := append(o.Eval[:len(o.Eval):len(o.Eval)], datalog.Explain(try.Report))
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, o.Base, opts...)
	if err != nil {
		return refuse(err, kindOf(err, KindEval))
	}
	if len(rows) == 0 {
		return refuse(&EmptyAnswer{Why: try.Report.Empty, Hint: hintFor(ctx, o.Pack, try.Report.Empty)}, KindEmpty)
	}
	prog.Rows = rows
	return judged(ctx, try, prog, o)
}

// judged is try accepted, unless the Pack's Check refuses prog's answer.
func judged(ctx context.Context, try Try, prog Program, o Options) (Try, Program, error) {
	if o.Pack == nil || o.Pack.Check == nil {
		return try, prog, nil
	}
	if err := o.Pack.Check(ctx, prog); err != nil {
		try.Err, try.Kind = err, KindAnswer
		return try, Program{}, err
	}
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
