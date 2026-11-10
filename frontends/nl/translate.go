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

// A Turn is one message of a conversation, as Conversation.Compact writes one.
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

// A Grounder finds the values a question's names stand for (#325), the way Declaire's name.find turns
// a short name into ranked ids, so a model is handed real values instead of spelling them. It is the
// host's: nl can't tell which words of a question are names. An error fails the translator.
type Grounder interface {
	Ground(ctx context.Context, question string) ([]Candidate, error)
}

// A Candidate is a name from the question and the values it may stand for, likeliest first.
type Candidate struct {
	Name   string
	Values []string
}

// Options tune the translator. The zero value tries the model three times and only checks the program.
type Options struct {
	// MaxTries is how many drafts each model gets; 3 when 0.
	MaxTries int
	// Base, when set, evaluates a program that checks. An evaluation error is fed back as a check
	// error is, and an empty answer goes to Fallback, or back to the model when no fallback is left.
	// Without it, a program that checks is accepted.
	Base *datalog.Base
	// Eval are the options Base is evaluated with, such as a Budget.
	Eval []datalog.Option
	// Grammar replaces the grammar every answer is held to, GrammarFor(v) by default, such as with
	// GrammarFor(v, GoalOnly()) for a small model. NoGrammar sends none.
	Grammar   string
	NoGrammar bool
	// Pack, when set, is the host's domain (#344): its part of the prefix follows the card, its
	// examples are checked before any model is asked, its Grounder grounds the question when the
	// translator has none, and its Check judges each answer.
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
	// nothing better, the translator returns the first such answer rather than failing. It needs a Vote:
	// without one every answer agrees with itself.
	Escalate float64
	// Budget caps the tokens and time the translator spends on the question.
	Budget Budget
	// Abstain, when set, says when the translator returns an *Abstained, "not confident" with the closest
	// draft, in place of an answer its vote disputes or a failure to answer (#482). Off by default.
	Abstain *Abstain
	// Tiers, when its Policy is set, says which models answer and in what order (#538): the local
	// model alone, the frontier model alone, or the local one and then the frontier one, which starts
	// from the local one's closest draft. It replaces the translator's own choice of models.
	Tiers *Tiers

	grounder Grounder // the Grounder a find action asks
}

// A Program is what the translator accepted: its text, as the model wrote it less any Markdown fence, the
// parsed Query, its rows when Options.Base evaluated it, and the model that wrote it.
type Program struct {
	Text  string
	Query datalog.Query
	Rows  []datalog.Row
	Model string
	// Language is the front end's the model wrote Text in, "" for jaala's, and Compiled the jaala
	// program Text compiled to, which Query parses.
	Language, Compiled string
	// Confidence is what the translator knows about how far to trust it.
	Confidence Confidence
}

// A Trace is every try the translator made, in order, whether or not one was accepted.
type Trace struct {
	Tries []Try
	// Steps are the actions the models asked for before answering (Options.Actions), in order.
	Steps []Step
	// Votes are the extra drafts Options.Vote drew, in order, each judged as an answer is.
	Votes []Try
	// Escalations are the answers moved on from for agreeing too little (Options.Escalate).
	Escalations []Escalation
	// Tier is the tier that answered under Options.Tiers (TierLocal, TierFrontier), "" when none did
	// or no tiers were set.
	Tier string
	// Offered are the values the Grounder offered for the question's names (Session.Offered): what
	// grounding found before any model was asked, then what each find action found.
	Offered []Candidate
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
	// Calls is how many model calls the try's tokens came from: its reply's, and a guided call that
	// rewrote it. A loop that leaves it 0 has a try with tokens counted as one call (Cost).
	Calls int
	// Tier is the tier whose model wrote it under Options.Tiers, "" without tiers.
	Tier string
	// Truncated says a model call behind it wrote as many tokens as its cap allowed, so the reply was
	// likely cut off (#549): Text is then what came before the cap, and its refusal says little about
	// the model. A reasoning model's hidden reasoning counts toward the cap too.
	Truncated bool
	// Report is what evaluating the draft over Options.Base did (datalog.Explain), when it was
	// evaluated: its work per rule and literal, and for an empty answer, Report.Empty.
	Report *datalog.Report

	explain func(error) string // how Err goes back to the model, in the draft's language
	// Mode is ModeCall for a call the model picked from Pack.Callables (#484), and "" for a drafted
	// program.
	Mode string

	// Reasked marks a repeat of a draft that answered nothing which, being the model's first repeat of
	// its turn, was sent back with the reason restated rather than ending the turn (#483). A host
	// counts them to tell whether asking again helped.
	Reasked bool

	// Repair, for a draft the harness rewrote itself (Options.Actions), says what it changed: a
	// quoted value the data holds in another spelling the conversation had seen. Text is the
	// rewritten draft, and the try before it the model's own.
	Repair string
}

// The Try kinds that aren't a CheckError's.
const (
	KindParse  = "parse"
	KindEval   = "eval"
	KindEmpty  = "empty"
	KindRepeat = "repeat"
	KindModel  = "model"
	KindAnswer = "answer"
	// KindDeclined is a reply saying the data can't answer the question (Abstain.Declined), whose Err
	// is a *Declined.
	KindDeclined = "declined"
)

// instructions follow the card in every Session's Prefix.
const instructions = `
# Write one program answering the question, using only the members above: rules, if any, then
# body => columns. Variables are ?name, strings are "quoted". Reply with the program alone.
`

// reask is what a model is told when it resends a draft that answered nothing (#483): that it did, what
// it was told the first time (told, in the draft's language), the call to change when the empty report
// names one in jaala's terms, and the grounded values again, since by now they are far back.
func reask(pr Prompts, what, told string, err error, frontEnd bool, cands []Candidate) string {
	change := "the part the report above names"
	var empty *EmptyAnswer
	if errors.As(err, &empty) && empty.Why != nil && !frontEnd {
		call := empty.Why.Called
		if call == "" {
			call = empty.Why.Literal
		}
		change = "`" + call + "`, which matched nothing,"
	}
	return fmt.Sprintf(pr.Reask, what, told, grounded(pr.Grounded, cands), change)
}

// promptPrefix is every question's Prefix in two parts, with the text actions' instructions between
// them for a model that writes its actions: the card and instructions, jaala's or the front end's,
// then the pack's part, with the front end's examples in place of the pack's. Examples are checked
// first, so a broken one fails the translator before any model is asked.
func promptPrefix(v *ns.Vocabulary, o Options) (head, actions, tail string, err error) {
	fe := o.FrontEnd
	if fe == nil {
		card, err := Card(v)
		if err != nil {
			return "", "", "", err
		}
		head = card + o.Pack.prompts().Instructions + o.declineLine("program")
		if o.Actions != nil {
			actions = actionsPrefix(nil, o.grounder != nil, o.Actions.builds(nil), o.offersPaths(), o.Pack.prompts())
		}
		if o.Pack != nil {
			if err := o.Pack.Validate(v); err != nil {
				return "", "", "", err
			}
			tail = o.Pack.Prefix()
		}
		return head, actions, tail, nil
	}
	card, err := fe.Card(v)
	if err != nil {
		return "", "", "", err
	}
	examples := fe.Examples()
	for i, e := range examples {
		c, err := fe.Compile(v, e.Program)
		if err == nil {
			err = datalog.Validate(c.Query, v)
		}
		if err != nil {
			return "", "", "", fmt.Errorf("nl: the front end's example %d (%q): %w", i+1, e.Question, err)
		}
	}
	instr := fe.Instructions()
	if s, ok := o.Pack.prompts().FrontEndInstructions[fe.Language()]; ok {
		instr = s
	}
	head = card + instr + o.declineLine("query")
	if o.Actions != nil {
		actions = actionsPrefix(fe, o.grounder != nil, false, false, o.Pack.prompts())
	}
	if o.Pack != nil {
		p := *o.Pack
		p.Examples = examples
		tail = p.Prefix()
	} else if len(examples) > 0 {
		tail = (&Pack{Examples: examples}).Prefix()
	}
	return head, actions, tail, nil
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
	// Without is what the rest of the goal gives the culprit's variables when the culprit is left
	// out (withoutCulprit), "" when it couldn't be run or gave nothing.
	Without string
}

func (e *EmptyAnswer) Error() string {
	if e.Why == nil {
		return errEmpty.Error()
	}
	s := errEmpty.Error() + ":\n" + e.Why.String()
	if e.Hint != "" {
		s += "\n" + strings.TrimRight(e.Hint, "\n")
	}
	if e.Without != "" {
		s += "\n" + e.Without
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

// Is makes an EmptyAnswer the empty answer the translator moves to the fallback on.
func (e *EmptyAnswer) Is(target error) bool { return target == errEmpty }

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
		why := try.Report.Empty
		return refuse(&EmptyAnswer{Why: why, Hint: hintFor(ctx, o.Pack, why), Without: withoutCulprit(ctx, q, why, o)}, KindEmpty)
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

// offersPaths says the model may ask for paths: the pack has a Shape, there is a Base to run them
// on, and the language is jaala's own, since a path is a jaala program.
func (o Options) offersPaths() bool {
	return o.Actions != nil && o.Pack != nil && o.Pack.Shape != nil && o.Base != nil && o.FrontEnd == nil
}

// grounded lists the candidates after the question under head (Prompts.Grounded), for the model to use
// their values as written.
func grounded(head string, cands []Candidate) string {
	if len(cands) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + head)
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
