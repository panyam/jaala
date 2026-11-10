package nl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Session is one question's half of a translation that is jaala's (#496): the prompt, the grammar,
// the actions, how a reply is judged and how two answers are told apart. It runs no model and keeps
// no loop: nlmodels' translator drives one on chakra Runners, and any agent framework's loop can
// drive one the same way, asking the model itself and handing each reply to a Conversation.
//
// A Session is not safe for concurrent use, except Draw and Key, which read nothing it changes.
type Session struct {
	v     *ns.Vocabulary
	o     Options
	pr    Prompts
	cands []Candidate
	found []Candidate // what each find action offered, in order
	mem   *memory     // what the actions have shown, across models, since it is about the data

	prefix, ask, grammar, answerGrammar string
	textPrefix, textGrammar             string // for a model that writes its actions (TextActions)
	finalGrammar                        string // the draft's, a try line allowed before it (FinalGrammar)
	tries                               int
	pick                                bool

	built      []string // the body of the last conversation to answer, for a vote's tail-only draws (Build)
	links      []Link   // Pack.Links, or KindGraph's once read (linksRead)
	linksRead  bool
	linksErr   error
	question   string   // as the host asked it, for Pack.Preview
	previewed  bool     // Preview has read the targets
	preview    []Sample // what Preview read
	previewErr error    // the targets Preview couldn't read
}

// NewSession prepares question over v, as the translator does before asking any model: it checks the
// pack's prompt templates and examples, writes the prefix, grounds the question (with g, or the
// pack's Grounder when g is nil) and picks the grammar. An error is one the translator would fail with.
func NewSession(ctx context.Context, question string, v *ns.Vocabulary, g Grounder, o Options) (*Session, error) {
	if o.Pack != nil && g == nil {
		g = o.Pack.Grounder
	}
	o.grounder = g
	pr := o.Pack.prompts()
	if err := pr.checkTemplates(); err != nil {
		return nil, err
	}
	head, actions, tail, err := promptPrefix(v, o)
	if err != nil {
		return nil, err
	}
	prefix := head + tail
	if o.Actions.replies() {
		prefix = head + actions + tail
	}
	s := &Session{v: v, o: o, pr: pr, prefix: prefix, textPrefix: head + actions + tail, ask: question, question: question,
		mem: &memory{found: pr.CompactFound, refused: pr.CompactRefused}}
	if g != nil {
		if s.cands, err = g.Ground(ctx, question); err != nil {
			return nil, fmt.Errorf("nl: grounding the question: %w", err)
		}
		s.ask += grounded(pr.Grounded, s.cands)
	}
	s.tries = o.MaxTries
	if s.tries <= 0 {
		s.tries = 3
	}
	switch {
	case o.NoGrammar:
	case o.Grammar != "":
		s.answerGrammar = o.Grammar
	case o.FrontEnd != nil:
		s.answerGrammar, err = o.FrontEnd.Grammar(v)
	default:
		s.answerGrammar, err = GrammarFor(v)
	}
	if err != nil {
		return nil, err
	}
	if o.Actions.builds(o.FrontEnd) {
		s.answerGrammar = buildGrammar(s.answerGrammar)
	}
	if o.declines() {
		s.answerGrammar = declineGrammar(s.answerGrammar)
	}
	s.grammar, s.textGrammar, s.finalGrammar = s.answerGrammar, s.answerGrammar, s.answerGrammar
	if o.Actions != nil && s.answerGrammar != "" {
		s.textGrammar = actionGrammar(s.answerGrammar, g != nil, o.Actions.builds(o.FrontEnd), o.offersPaths())
		s.finalGrammar = finalGrammar(s.answerGrammar)
	}
	if o.Actions.replies() {
		s.grammar = s.textGrammar
	}
	return s, nil
}

// Pick is the session's first step when the pack lists Callables (#484): the model picks one call
// that answers the question alone, or replies ActionNone. Its prompt, grammar and judging are a
// pick's, its replies are jaala's text whatever the FrontEnd, and it takes no actions. It is nil when
// the pack lists no Callables.
func (s *Session) Pick() (*Session, error) {
	if s.o.Pack == nil || len(s.o.Pack.Callables) == 0 {
		return nil, nil
	}
	cs := s.o.Pack.Callables
	prefix, err := pickPrefix(cs, s.v, s.o.Pack)
	if err != nil {
		return nil, err
	}
	grammar := ""
	if !s.o.NoGrammar {
		if grammar, err = pickGrammar(cs, s.v); err != nil {
			return nil, err
		}
	}
	p := *s
	p.o.FrontEnd, p.o.Actions = nil, nil
	p.prefix, p.grammar, p.answerGrammar = prefix, grammar, grammar
	p.tries, p.pick = pickTries, true
	return &p, nil
}

// Prefix is what every prompt of the session starts with, the same for every question over one
// vocabulary and options, so a client can cache it.
func (s *Session) Prefix() string { return s.prefix }

// Ask is the question as the model is first asked it, with the values its names were grounded to.
func (s *Session) Ask() string { return s.ask }

// Candidates are what grounding found for the question's names.
func (s *Session) Candidates() []Candidate { return s.cands }

// Grammar is the GBNF an answer is held to, "" when Options.NoGrammar is set.
func (s *Session) Grammar() string { return s.answerGrammar }

// ActionGrammar is Grammar with the text actions around it (Options.Actions), for a model that writes
// an action as its reply rather than calling it as a tool; Grammar when there are no actions.
func (s *Session) ActionGrammar() string { return s.grammar }

// TextActions is the prefix and grammar for a model that writes its actions as its reply, where
// Prefix and ActionGrammar are for one that calls them as tools under Actions.AsTools (#509): the
// prefix lists the actions, and the grammar takes an action or a draft. A loop offers such a model
// no tools, reads each reply with ReadAction, and holds it to Grammar once its steps run out.
// Without Options.Actions they are Prefix and Grammar.
func (s *Session) TextActions() (prefix, grammar string) { return s.textPrefix, s.textGrammar }

// FinalGrammar is what a model that writes its actions is held to once its steps run out (#512):
// Grammar with a try line allowed before the draft, which Judge drops. Grammar without
// Options.Actions.
func (s *Session) FinalGrammar() string { return s.finalGrammar }

// StuckNote is Prompts.Stuck over what, for a loop telling a model it repeats its actions (#512).
func (s *Session) StuckNote(what string) string { return fmt.Sprintf(s.pr.Stuck, what) }

// MaxTries is how many answers a model gets in one Conversation before it ends.
func (s *Session) MaxTries() int { return s.tries }

// MaxSteps is how many actions a model may take before it must answer, 0 without Options.Actions.
func (s *Session) MaxSteps() int {
	if s.o.Actions == nil {
		return 0
	}
	return s.o.Actions.steps()
}

// Act runs one action (ActionLook, ActionValues, ActionTry or ActionFind) and returns what goes back
// to the model. The text values its result shows are kept, so a later answer guessing one's spelling
// can be repaired. A bad argument is answered in the text, never as an error, since the model reads
// it. It needs Options.Actions.
func (s *Session) Act(ctx context.Context, action, arg string) string {
	if action == ActionPaths {
		return s.paths(ctx, arg)
	}
	result, seen := act(ctx, action, arg, s.v, s.o)
	s.mem.see(seen)
	if action == ActionFind && len(seen) > 0 {
		s.found = append(s.found, Candidate{Name: arg, Values: seen})
	}
	return result
}

// Offered are the values the Grounder offered for the question's names: the grounding before any
// model was asked, then each find action's, in order. A model profile's grounding reads them against
// the accepted program's constants (#537).
func (s *Session) Offered() []Candidate {
	return append(append([]Candidate(nil), s.cands...), s.found...)
}

// Draw judges one more answer to the question outside any conversation, as a vote draws one: the
// try's Text is checked, compiled and run as an answer is, and the try comes back with Err and Kind
// set when it was refused. try carries the model's reply: Model, Text and what it cost.
func (s *Session) Draw(ctx context.Context, try Try) (Try, Program, error) {
	try = withBody(s.reply(try), s.built)
	return check(ctx, try, s.v, s.o, 0)
}

// Key is what two accepted answers agree on: their rows as a multiset of values when Options.Base ran
// them, or else the program they compiled to.
func (s *Session) Key(p Program) string { return voteKey(p, s.o.Base != nil) }

// reply is try with the model's reply unfenced and the session's language and explainer set.
func (s *Session) reply(try Try) Try {
	if s.o.FrontEnd != nil {
		try.Language = s.o.FrontEnd.Language()
	}
	try.Text = unfence(try.Text)
	if s.o.Actions != nil {
		try.Text = withoutTry(try.Text)
	}
	text := try.Text
	try.explain = func(err error) string { return Explain(text, err) }
	return try
}

// A Conversation is one model's turn at the question: it judges each reply against what that model
// was told before. A fallback model starts a new one, since it doesn't read the first model's drafts.
type Conversation struct {
	s       *Session
	ask     string
	last    bool // no model is left after this one
	refused map[string]error
	why     map[string]string // what each refused draft was told, in its own language
	reasked bool              // a repeated empty draft was asked again (#483)
	tries   int
	steps   int               // actions taken
	repeats int               // actions asked again, which took no step
	asked   map[string]string // what each action answered, keyed by action and argument
	body    []string          // the literals add built (Actions.Build), in order
}

// MaxRepeats is how many repeated actions a Conversation answers before it ends the model's actions
// (#517). A repeat isn't run and takes no step, so a greedy model cycling through actions it took
// already would otherwise never reach its answer. nlmodels' translator gives chakra's
// RunnerConfig.MaxFreeRefusals the same number, so a repeat costs the Runner no step either.
const MaxRepeats = 3

// NewConversation starts a model's turn. after is why the previous model's turn ended, nil for the
// first, and last says no model follows this one: an empty answer then goes back to this model
// rather than ending its turn.
func (s *Session) NewConversation(after error, last bool) *Conversation {
	c := &Conversation{s: s, ask: s.ask, last: last, refused: map[string]error{}, why: map[string]string{}, asked: map[string]string{}}
	if !s.pick {
		s.mem.facts, s.mem.drafts, s.mem.last = nil, nil, ""
		if after != nil && errors.Is(after, errEmpty) {
			c.ask += "\n\n" + s.pr.EmptyFallback
		}
	}
	return c
}

// From starts the conversation from draft, a reply another Session's conversation refused: the
// one-shot draft of Actions.DraftFirst (#521). The draft counts as refused here too, so the model
// sending it again is a repeat, and Ask carries it with its refusal (Prompts.DraftFirst). It counts
// no try: the loop keeps all of its own. A draft with no error, or whose model failed, is ignored.
func (c *Conversation) From(draft Try) { c.from(draft, c.s.pr.DraftFirst) }

// Handoff starts the conversation from draft, the closest draft of the local tier, for the frontier
// tier's model (Options.Tiers, #538), with Prompts.Handoff. Like From, the draft counts as refused, so
// the frontier model sending it back is a repeat, and a draft with no error or text is ignored. For an
// answer the local tier's vote disputed, Err carries the diagnosis.
func (c *Conversation) Handoff(draft Try) { c.from(draft, c.s.pr.Handoff) }

func (c *Conversation) from(draft Try, prompt string) {
	if draft.Err == nil || draft.Text == "" || draft.Kind == KindModel {
		return
	}
	s := c.s
	told := Explain(draft.Text, draft.Err)
	if draft.explain != nil {
		told = draft.explain(draft.Err)
	}
	told = strings.TrimRight(told, "\n")
	what := "program"
	if s.o.FrontEnd != nil {
		what = "query"
	}
	c.refused[draft.Text] = draft.Err
	c.why[draft.Text] = told
	s.mem.add(true, fact(draft.Text, "refused ("+draft.Kind+"): "+strings.Join(strings.Fields(told), " ")))
	s.mem.last = draft.Text + "\n" + told
	c.ask += "\n\n" + fmt.Sprintf(prompt, draft.Text, told, what)
}

// Ask is the conversation's first message: the session's, and after an empty answer ended the last
// model's turn, a line saying so.
func (c *Conversation) Ask() string { return c.ask }

// Compact is the one message Actions.Compact sends in place of the conversation so far: the question,
// what the actions found, each refused answer on a line, and the last refusal whole. With
// Actions.Ledger it leaves the found and refused lines to Ledger.
func (c *Conversation) Compact() []Turn {
	return c.s.mem.compact(c.ask, c.s.o.Actions == nil || !c.s.o.Actions.Ledger)
}

// Ledger is what Actions.Ledger shows the model on every call, after the conversation (#514): what
// its look, values and find actions found, under Prompts.LedgerFound, then each draft it tried or
// answered with and what that did, under Prompts.LedgerTried. It is "" before anything happened, and
// a list with nothing in it is left out. A loop shows it whether or not Actions.Ledger is set; the
// option only says the translator should.
func (c *Conversation) Ledger() string {
	var out []string
	for _, p := range c.LedgerParts() {
		if p.Lines != "" {
			out = append(out, p.Heading+"\n"+p.Lines)
		}
	}
	return strings.Join(out, "\n\n")
}

// A LedgerPart is one of the Ledger's two lists: its heading, and its lines, "" while it is empty.
type LedgerPart struct {
	Heading, Lines string
}

// LedgerParts are the Ledger's two lists, findings then drafts, for a loop that shows each its own
// way: nlmodels' translator writes each to a chakra slot, under LedgerHeader.
func (c *Conversation) LedgerParts() []LedgerPart { return c.s.mem.ledger(c.s.pr) }

// LedgerHeader is Prompts.LedgerHeader, the line a loop opens the ledger's lists with when it shows
// them apart (LedgerParts).
func (s *Session) LedgerHeader() string { return s.pr.LedgerHeader }

// Act runs an action this conversation's model asked for and returns what goes back to it, ended
// with Prompts.LastLook when it was the last MaxSteps allows. It is kept for Compact and the Ledger as
// the reply that would have asked for it ("values pkg.license"). Steps counts it, and the caller stops
// offering actions once Steps reaches MaxSteps.
//
// An action the conversation took before isn't run again and takes no step (#512, #517): it gets
// Prompts.TriedAgain (a try) or Prompts.AskedAgain (any other) and what it answered then. A greedy
// model cycled through try, look, try, find, spending a step on every repeat until its steps ran out.
// The MaxRepeats-th repeat ends the actions, and its result ends with Prompts.LastLook. Repeats
// says, before Act, whether an action is one.
func (c *Conversation) Act(ctx context.Context, action, arg string) string {
	s := c.s
	key := action + "\x00" + arg
	if (action == ActionAdd || action == ActionDrop) && !c.Repeats(action, arg) {
		result := c.build(ctx, action, arg)
		c.asked[key] = result
		c.steps++
		s.mem.add(true, fact(ActionText(action, arg), result))
		if c.steps == s.MaxSteps() {
			result += "\n\n" + s.pr.LastLook
		}
		return result
	}
	if before, ok := c.asked[key]; ok {
		c.repeats++
		result := s.pr.AskedAgain + " " + before
		if action == ActionTry {
			result = s.pr.TriedAgain + "\n" + before
		}
		if c.repeats >= MaxRepeats && c.steps < s.MaxSteps() {
			c.EndActions()
			result += "\n\n" + s.pr.LastLook
		}
		return result
	}
	result := s.Act(ctx, action, arg)
	c.asked[key] = result
	c.steps++
	s.mem.add(action == ActionTry, fact(ActionText(action, arg), result))
	if c.steps == s.MaxSteps() {
		result += "\n\n" + s.pr.LastLook
	}
	return result
}

// Repeats says the conversation took action with arg before, so Act would answer it from then,
// without running it or taking a step. A loop marks such a reply free (chakra's DenyToolFree, or a
// TurnEndVerdict with Free).
func (c *Conversation) Repeats(action, arg string) bool {
	switch action {
	case ActionAdd:
		return slices.Contains(c.body, arg)
	case ActionDrop:
		return false
	}
	_, ok := c.asked[action+"\x00"+arg]
	return ok
}

// Body is the program add has built so far (Actions.Build), its literals joined as a goal's are, ""
// before the first.
func (c *Conversation) Body() string { return strings.Join(c.body, ", ") }

// build runs an add or a drop (Actions.Build, #514) and says what came of it. An add whose body then
// checks keeps the literal and answers with the body's rows, or why it has none, as try does; one
// that doesn't check is refused with the reason and leaves the body as it was. A drop takes the last
// literal off.
func (c *Conversation) build(ctx context.Context, action, arg string) string {
	s := c.s
	if !s.o.Actions.builds(s.o.FrontEnd) {
		return fmt.Sprintf("%s needs Actions.Build in jaala's language", action)
	}
	if action == ActionDrop {
		if len(c.body) == 0 {
			return s.pr.DropNothing
		}
		last := c.body[len(c.body)-1]
		c.body = c.body[:len(c.body)-1]
		body := c.Body()
		if body == "" {
			body = "(empty)"
		}
		return fmt.Sprintf(s.pr.Dropped, last, body)
	}
	body := strings.Join(append(slices.Clip(c.body), arg), ", ")
	o := s.o
	o.Pack = nil // a look at the data isn't an answer the pack's Check judges
	try, prog, err := check(ctx, Try{Text: body, explain: func(err error) string { return Explain(body, err) }}, s.v, o, o.Actions.sample())
	switch {
	case err != nil && try.Kind != KindEmpty:
		return strings.TrimRight(try.explain(err), "\n") + "\n\n" + s.pr.NotAdded
	case err != nil:
		c.body = append(c.body, arg)
		return strings.TrimRight(try.explain(err), "\n")
	}
	c.body = append(c.body, arg)
	if s.o.Base == nil {
		return "it checks; with no data to run on, there are no rows to show"
	}
	text, seen := answeredText(body, prog, o.Actions.sample())
	s.mem.see(seen)
	return text
}

// withBody is try with a reply that is a goal's tail alone (=> ?p) put after body, the program the
// adds built (Actions.Build); try as it was when body is empty or the reply is a whole program.
func withBody(try Try, body []string) Try {
	if len(body) == 0 || !strings.HasPrefix(strings.TrimSpace(try.Text), "=>") {
		return try
	}
	text := strings.Join(body, ", ") + " " + strings.TrimSpace(try.Text)
	try.Text = text
	try.explain = func(err error) string { return Explain(text, err) }
	return try
}

// EndActions spends the steps left, so the caller stops offering actions and the model's next reply
// is its answer. The translator calls it when the model stays stuck after a note (#512).
func (c *Conversation) EndActions() { c.steps = max(c.steps, c.s.MaxSteps()) }

// Steps is how many actions the conversation's model has taken.
func (c *Conversation) Steps() int { return c.steps }

// ActionText is the reply that asks for an action, as a model writes one when actions come in its
// reply (look NAME, values TARGET, find NAME, or try then the draft on the next lines).
func ActionText(action, arg string) string {
	switch action {
	case ActionTry:
		return ActionTry + "\n" + arg
	case ActionDrop:
		return ActionDrop
	}
	return action + " " + arg
}

// A Tool is one action as a tool an agent framework offers the model (Actions.AsTools): the action
// is its name, and it takes one text argument.
type Tool struct {
	Action, Arg, Description string
}

// Tools are the session's actions as tools, nil without Options.Actions: look, values and try, and
// find when the question has a Grounder. Their descriptions are the pack's Prompts (ToolLook,
// ToolValues, ToolTry, ToolFind), values naming a target as the model's language writes one.
func (s *Session) Tools() []Tool {
	if s.o.Actions == nil {
		return nil
	}
	target := "relation.argument"
	if p, ok := s.o.FrontEnd.(Prober); ok {
		target = p.ValuesTarget()
	}
	ts := []Tool{
		{ActionLook, "name", s.pr.ToolLook},
		{ActionValues, "target", fmt.Sprintf(s.pr.ToolValues, target)},
		{ActionTry, "draft", s.pr.ToolTry},
	}
	if s.o.grounder != nil {
		ts = append(ts, Tool{ActionFind, "name", s.pr.ToolFind})
	}
	if s.o.Actions.builds(s.o.FrontEnd) {
		ts = append(ts, Tool{ActionAdd, "literal", s.pr.ToolAdd}, Tool{ActionDrop, "", s.pr.ToolDrop})
	}
	if s.o.offersPaths() {
		ts = append(ts, Tool{ActionPaths, "ends", s.pr.ToolPaths})
	}
	return ts
}

// A Verdict is what a Conversation makes of one reply. Exactly one of Program, Continue and Stop is
// set.
type Verdict struct {
	// Program is the accepted answer.
	Program *Program
	// Continue is what the model is told next, as the user's message, when the answer was refused and
	// the model has tries left.
	Continue string
	// Stop ends the model's turn without an answer: it repeated a draft it was told was wrong, its
	// answer was empty while another model is left, it ran out of tries, or, for a pick, it replied
	// ActionNone (ErrNoCall). It wraps the last refusal.
	Stop error
	// Tries are the tries to record, in order: the reply's, and after it a repair of it.
	Tries []Try
}

// ErrNoCall is a pick's Stop when the model replied ActionNone: no callable answers the question.
var ErrNoCall = errors.New("nl: no callable answers the question")

// Judge judges a reply that is an answer, not an action. try carries the model's reply: Model, Text
// (a Markdown fence is taken off) and what it cost.
func (c *Conversation) Judge(ctx context.Context, try Try) Verdict {
	s := c.s
	try = s.reply(try)
	if !s.pick {
		try = withBody(try, c.body)
	}
	if s.pick {
		return c.judgePick(ctx, try)
	}
	c.tries++
	if why, ok := declining(try.Text); ok && s.o.declines() {
		try.Kind, try.Err = KindDeclined, &Declined{Model: try.Model, Why: why}
		return Verdict{Stop: try.Err, Tries: []Try{try}}
	}
	var p Program
	var err error
	if prev, ok := c.refused[try.Text]; ok {
		err = fmt.Errorf("the same draft as before: %w", prev)
		try.Err, try.Kind = err, KindRepeat
	} else {
		try, p, err = check(ctx, try, s.v, s.o, 0)
	}
	var tries []Try
	if err != nil && s.o.Actions != nil && repairable(try) {
		// a guessed spelling of a value the conversation has seen: fixed here, not by the model
		if text, what, ok := s.mem.repair(try.Text, domainOf(try.Err, s.v)); ok {
			rtry, rp, rerr := check(ctx, Try{Model: try.Model, Language: try.Language, Text: text, Repair: what,
				explain: func(err error) string { return Explain(text, err) }}, s.v, s.o, 0)
			if rerr == nil {
				tries = append(tries, try)
				try, p, err = rtry, rp, nil
			}
		}
	}
	if err == nil {
		s.built = slices.Clone(c.body)
		return Verdict{Program: &p, Tries: append(tries, try)}
	}
	what := "program"
	if s.o.FrontEnd != nil {
		what = "query"
	}
	if try.Kind == KindRepeat && !c.reasked && errors.Is(err, errEmpty) {
		// a small model resends the draft it just wrote after an empty answer: once per turn it is told
		// again, more pointedly, rather than losing its remaining tries (#483)
		c.reasked = true
		try.Reasked = true
		msg := reask(s.pr, what, c.why[try.Text], err, s.o.FrontEnd != nil, s.cands)
		s.mem.add(true, fact(try.Text, "repeated, and answered nothing again"))
		s.mem.last = try.Text + "\n" + msg
		return c.next(append(tries, try), msg, err)
	}
	if try.Kind == KindRepeat || try.Kind == KindEmpty && !c.last {
		return Verdict{Stop: err, Tries: append(tries, try)}
	}
	c.refused[try.Text] = err
	fix := fmt.Sprintf(s.pr.Fix, what)
	if try.Kind == KindEmpty {
		fix = fmt.Sprintf(s.pr.FixEmpty, what)
	}
	told := strings.TrimRight(try.explain(err), "\n")
	c.why[try.Text] = told
	s.mem.add(true, fact(try.Text, "refused ("+try.Kind+"): "+strings.Join(strings.Fields(told), " ")))
	s.mem.last = try.Text + "\n" + told + "\n\n" + fix
	return c.next(append(tries, try), told+"\n\n"+fix, err)
}

// next continues with msg, or stops on err once the model has used its tries.
func (c *Conversation) next(tries []Try, msg string, err error) Verdict {
	if c.tries >= c.s.tries {
		return Verdict{Stop: err, Tries: tries}
	}
	return Verdict{Continue: msg, Tries: tries}
}

// judgePick judges a pick's reply: ActionNone stops with ErrNoCall and no try, and a refused call,
// a repeated one included, goes back with why and PickRetry while tries are left.
func (c *Conversation) judgePick(ctx context.Context, try Try) Verdict {
	try.Mode = ModeCall
	if try.Text == ActionNone {
		return Verdict{Stop: ErrNoCall}
	}
	c.tries++
	var p Program
	var err error
	if prev, ok := c.refused[try.Text]; ok {
		err = fmt.Errorf("the same draft as before: %w", prev)
		try.Err, try.Kind = err, KindRepeat
	} else {
		try, p, err = check(ctx, try, c.s.v, c.s.o, 0)
	}
	if err == nil {
		return Verdict{Program: &p, Tries: []Try{try}}
	}
	c.refused[try.Text] = err
	why := strings.TrimRight(try.explain(err), "\n")
	return c.next([]Try{try}, why+"\n\n"+c.s.pr.PickRetry, err)
}
