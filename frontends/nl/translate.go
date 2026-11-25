package nl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	// Note, when set, says what Name is in the data when that isn't a value to quote, such as Declaire's
	// attr keys: `an attr key, attr(?id, "load_errors", ?v)`. It follows the values, or stands alone
	// when there are none. Values a model was shown don't include it.
	Note string
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

// A BudgetStopped is the error of a draft whose evaluation passed its work budget, with where the
// work went (#544). The budget's numbers alone gave a model nothing to change, so it sent the same
// draft again. errors.As still finds the *datalog.BudgetExceeded in it.
type BudgetStopped struct {
	Err *datalog.BudgetExceeded
	// Literal is the literal that did the most work, as it ran, and Rule the rule as written whose
	// body holds it, "" for the goal's.
	Literal, Rule string
	// Through is the program's own literal that reaches Rule when Rule is a module's (#569), "" when
	// Literal is the program's. A model can't change a module's rule, but it can bind what it calls.
	Through string
	// Where says so in the pack's words (Prompts.BudgetWhere, then Prompts.BudgetFix).
	Where string
}

func (e *BudgetStopped) Error() string { return e.Err.Error() + "\n" + e.Where }

// Unwrap is the budget's own error.
func (e *BudgetStopped) Unwrap() error { return e.Err }

// budgetWhere is err with where the work went when the budget stopped it and r shows a literal that
// did any, and err as it was otherwise.
func budgetWhere(err error, r *datalog.Report, q datalog.Query, v *ns.Vocabulary, pr Prompts) error {
	var be *datalog.BudgetExceeded
	if r == nil || !errors.As(err, &be) {
		return err
	}
	var hot *datalog.LiteralReport
	rule, rel := "", ""
	for _, body := range append([]*datalog.BodyReport{r.Goal}, r.Rules...) {
		if body == nil {
			continue
		}
		for _, l := range body.Literals {
			if hot == nil || l.Work > hot.Work || l.Work == hot.Work && l.Stopped && !hot.Stopped {
				hot, rule, rel = l, body.Rule, body.Relation
			}
		}
	}
	if hot == nil || hot.Work == 0 {
		return err
	}
	lit := hot.Literal
	if rule != "" {
		lit = fmt.Sprintf(pr.BudgetInRule, lit, rule)
	}
	through := callerOf(q, v, rel)
	if through != "" {
		lit = fmt.Sprintf(pr.BudgetThrough, through, lit)
	}
	access := strings.Join(hot.Access, ", ")
	if access == "" {
		access = "-"
	}
	where := fmt.Sprintf(pr.BudgetWhere, lit, strconv.FormatInt(hot.Reached, 10), strconv.FormatInt(hot.Work, 10),
		strconv.FormatInt(hot.Passed, 10), access)
	named := hot.Literal
	if through != "" {
		named = through
	}
	if free := unboundAt(q, named); len(free) > 0 {
		where += "\n" + fmt.Sprintf(pr.BudgetUnbound, strings.Join(free, ", "))
	}
	where += "\n" + pr.BudgetFix
	return &BudgetStopped{Err: be, Literal: hot.Literal, Rule: rule, Through: through, Where: where}
}

// callerOf is the first literal of q's own, its goal's then its rules', that reaches rel through
// the modules' rules, "" when rel is q's own relation or none reaches it.
func callerOf(q datalog.Query, v *ns.Vocabulary, rel string) string {
	own := map[string]bool{}
	for _, r := range q.Rules {
		own[r.Head.Relation] = true
	}
	if rel == "" || v == nil {
		return ""
	}
	linked, err := datalog.Link(q, v)
	if err != nil {
		return ""
	}
	reads := map[string][]string{}
	for _, r := range linked.Rules {
		reads[r.Head.Relation] = append(reads[r.Head.Relation], bodyRelations(r.Body)...)
	}
	reaches := func(from string) bool {
		seen := map[string]bool{}
		next := []string{from}
		for len(next) > 0 {
			n := next[len(next)-1]
			next = next[:len(next)-1]
			if n == rel {
				return true
			}
			if !seen[n] {
				seen[n] = true
				next = append(next, reads[n]...)
			}
		}
		return false
	}
	bodies := []datalog.Body{q.Goal}
	for _, r := range q.Rules {
		bodies = append(bodies, r.Body)
	}
	for _, b := range bodies {
		for _, l := range b.Literals {
			for _, name := range bodyRelations(datalog.Body{Literals: []datalog.Literal{l}}) {
				if !own[name] && reaches(name) {
					return l.String()
				}
			}
		}
	}
	return ""
}

// unboundAt are the variables of q's literal written lit that no positive atom written before it in
// its body binds (#571), in its order, each once. On Declaire's x04 the hot literal came first, so
// both its variables were free and the model didn't know which to bind. nil when q holds no such
// literal.
func unboundAt(q datalog.Query, lit string) []string {
	bodies := []datalog.Body{q.Goal}
	for _, r := range q.Rules {
		bodies = append(bodies, r.Body)
	}
	for _, b := range bodies {
		bound := map[datalog.Var]bool{}
		for _, l := range b.Literals {
			if l.String() == lit && l.Pos != nil {
				var free []string
				for _, t := range l.Pos.Args {
					if t.Var != "" && t.Var != "_" && !bound[t.Var] {
						bound[t.Var] = true // each once
						free = append(free, "?"+string(t.Var))
					}
				}
				return free
			}
			if l.Pos != nil {
				for _, t := range l.Pos.Args {
					bound[t.Var] = true
				}
			}
		}
	}
	return nil
}

// bodyRelations are the relations b's atoms call, those inside its aggregates and optionals too.
func bodyRelations(b datalog.Body) []string {
	var out []string
	for _, l := range b.Literals {
		for _, a := range []*datalog.Atom{l.Pos, l.Neg} {
			if a != nil {
				out = append(out, a.Relation)
			}
		}
		if l.Agg != nil {
			out = append(out, bodyRelations(l.Agg.Body)...)
		}
		if l.Opt != nil {
			out = append(out, bodyRelations(*l.Opt)...)
		}
	}
	return out
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
		return refuse(budgetWhere(err, try.Report, q, v, o.Pack.prompts()), kindOf(err, KindEval))
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
		line := strings.Join(quoted, ", ")
		if c.Note != "" && line != "" {
			line += "; " + c.Note
		} else if c.Note != "" {
			line = c.Note
		}
		fmt.Fprintf(&b, "\n%s: %s", c.Name, line)
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
