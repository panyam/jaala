package nl

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/panyam/jaala/datalog"
)

// A Vote draws more answers from the model that answered, once it has, and keeps the program whose
// rows most of them answer (#393's self-consistency): a small model's guess and its second guess
// rarely agree on the same wrong rows. Each draft is judged as an answer is, and one that is refused
// agrees with nothing. Over Options.Base drafts agree when their rows match as a multiset, by value,
// as Eval matches them; without a Base, when they compile to the same program.
//
// The drafts differ only if the model samples: the translator seeds each draw when the provider
// takes a seed, and a model decoding greedily (temperature 0) writes the same draft every time, so
// its vote agrees by construction.
type Vote struct {
	// Drafts is how many answers are drawn in all, the first included; 3 when 0.
	Drafts int
	// Parallel asks for the other drafts at once, for a Model that is safe to call concurrently.
	Parallel bool
}

// Total is how many answers the vote counts, the first included: Drafts, or 3 when it is 0.
func (v *Vote) Total() int {
	if v.Drafts > 0 {
		return v.Drafts
	}
	return 3
}

// Confidence is what the translator knows about an answer it accepted, for a host to judge it by
// (Options.Escalate reads Agreement).
type Confidence struct {
	// Drawn is how many answers a Vote drew (1 without one) and Agreed how many of them answered as
	// this program does.
	Drawn, Agreed int
	// Ran says the program ran over Options.Base and answered rows, and Judged that the pack's Check
	// passed it.
	Ran, Judged bool
	// Steps is how many actions the model asked before it answered.
	Steps int
}

// Agreement is the share of a vote's drafts that agreed with the answer, 1 without a vote.
func (c Confidence) Agreement() float64 {
	if c.Drawn == 0 {
		return 0
	}
	return float64(c.Agreed) / float64(c.Drawn)
}

// An Escalation is an answer the translator moved on from because its drafts agreed less than
// Options.Escalate asks.
type Escalation struct {
	Model      string
	Text       string
	Confidence Confidence
}

// A Budget caps what the translator spends on one question, across every action, try and vote (#393,
// declaire#300). A zero field is no cap.
type Budget struct {
	// Tokens caps the prompt and reply tokens of every model call together.
	Tokens int
	// Time caps how long the translator runs. A deadline on its context stops it too, with the context's
	// error rather than a BudgetSpent.
	Time time.Duration
	// Calls caps the model calls (#538): every reply, a guided call rewriting one, and a vote's draws.
	Calls int
}

// A BudgetSpent is the translator's error when its Budget ran out before an answer was accepted. The
// Trace still holds every call made.
type BudgetSpent struct {
	Budget  Budget
	Tokens  int
	Elapsed time.Duration
}

func (e *BudgetSpent) Error() string {
	return fmt.Sprintf("nl: budget spent before an answer: %d tokens in %v (budget %d tokens, %v)", e.Tokens, e.Elapsed.Round(time.Millisecond), e.Budget.Tokens, e.Budget.Time)
}

// The classes a refused try, or a case that failed, falls in (Try.Class, Result.Class): what kind of
// mistake it was, so a host can tell which of the loop's tools would have helped.
const (
	ClassGrounding = "grounding" // it ran and answered nothing: a guessed value, kind or id
	ClassConstruct = "construct" // it didn't parse, compile or check, or failed evaluating
	ClassSemantics = "semantics" // it answered, but the pack's Check, or the case's rows, said wrong
	ClassBudget    = "budget"    // the Budget, or the evaluation's, ran out
	ClassModel     = "model"     // the Model failed
	ClassAbstained = "abstained" // Translate abstained (Options.Abstain): not a wrong answer, and no answer
)

// Class sorts a refused try by what went wrong, "" for one accepted. A repeated draft is classed by
// the refusal it repeated.
func (t Try) Class() string {
	var ce *datalog.CheckError
	var be *datalog.BudgetExceeded
	var bs *BudgetSpent
	switch {
	case t.Err == nil:
		return ""
	case t.Kind == KindModel:
		return ClassModel
	case errors.Is(t.Err, errEmpty):
		return ClassGrounding
	case errors.As(t.Err, &bs), errors.As(t.Err, &be):
		return ClassBudget
	case errors.As(t.Err, &ce):
		return ClassConstruct
	case t.Kind == KindAnswer:
		return ClassSemantics
	}
	return ClassConstruct
}

// voteKey is what two drafts agree on: their rows as a multiset of values when they ran, or else the
// program they compiled to.
func voteKey(p Program, ran bool) string {
	if !ran {
		if p.Compiled != "" {
			return "program\x00" + p.Compiled
		}
		return "program\x00" + p.Text
	}
	rows := rowsText(p.Rows)
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = rowKey(r)
	}
	slices.Sort(keys)
	return "rows\x00" + strings.Join(keys, "\x01")
}
