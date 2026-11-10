package nl

import (
	"errors"
	"fmt"
)

// Abstain says when the translator answers "not confident", an *Abstained, in place of an answer it
// doubts or a bare failure (#482). For a caller who acts on the answer, a wrong one costs more than
// none, and a host that can tell "not confident" from "broke" can say so, or hand the closest draft to
// the caller to finish. A zero field never abstains on its ground. A Model that fails, a cancelled
// context and a Grounder's error stay failures whatever is set.
type Abstain struct {
	// MinAgreement abstains on an accepted answer whose Confidence.Agreement is below it. Like
	// Escalate it needs a Vote, since without one every answer agrees with itself. Escalate runs
	// first, so the answer judged is the one the translator would have returned, a held one included.
	MinAgreement float64
	// NoAnswer abstains when the tries ran out with no draft accepted, a loop that ended on a
	// repeated draft included.
	NoAnswer bool
	// Budget abstains when Options.Budget runs out.
	Budget bool
	// Declined lets the model answer that the data can't answer the question (#551), with a line
	// saying why (Prompts.Decline), in place of a program. Without it the model can only write a
	// program, and on a question nothing answers a frontier model wrote one that runs and answers
	// wrongly. A model that declines ends its turn; a fallback or frontier tier left is asked, and
	// when none answers the translator abstains with AbstainDeclined and the model's words.
	Declined bool
}

// The reasons an Abstained gives.
const (
	AbstainLowAgreement = "low agreement" // the vote agreed less than MinAgreement
	AbstainNoAnswer     = "no answer"     // every try was refused
	AbstainRepeat       = "repeat"        // every try was refused, the last for repeating one
	AbstainBudget       = "budget"        // Options.Budget ran out
	AbstainDeclined     = "declined"      // the model said the data can't answer it (Abstain.Declined)
	// AbstainAgent is a host's own agent saying it had no answer, for a host grading an agent through
	// Eval: its translate returns an *Abstained with this Reason.
	AbstainAgent = "agent"
)

// Abstained is the translator saying it has no answer it's confident of (Options.Abstain), with the draft
// that came closest for a host to show or a caller to finish.
type Abstained struct {
	// Reason is one of the Abstain* constants.
	Reason string
	// Draft is the closest draft. For low agreement it is the answer itself, Confidence and rows
	// included. Otherwise it is the refused try ranked first (one that ran and answered nothing,
	// then one the pack's Check refused, then any other, the later among equals), with its Text,
	// Model and Language and a zero Confidence, since nothing was accepted; it is zero when no try
	// was made.
	Draft Program
	// Diagnosis says why Draft wasn't taken, as the model was told: Explain's text in the draft's
	// language, or how many drafts agreed.
	Diagnosis string
	// Err is the error the translator would have returned without Abstain, nil for low agreement.
	// Abstained unwraps to it, so errors.As still finds a BudgetSpent or the last try's CheckError.
	Err error
}

func (a *Abstained) Error() string {
	msg := "nl: not confident (" + a.Reason + ")"
	if a.Err != nil {
		return msg + ": " + a.Err.Error()
	}
	return msg + ": " + a.Diagnosis
}

func (a *Abstained) Unwrap() error { return a.Err }

// A Declined is a model's reply that the data can't answer the question (Abstain.Declined): the model
// and the reason it gave. It ends the model's turn as Verdict.Stop, and a try of KindDeclined
// carries it.
type Declined struct {
	Model, Why string
}

func (d *Declined) Error() string { return "nl: the model declined: " + d.Why }

// ErrNoProgram is what a translation fails with when every model ran out of tries, or stopped,
// with no draft accepted: the translator's error wraps it, and so must another loop's for Abstain.Judge
// to read it as no answer.
var ErrNoProgram = errors.New("nl: no program checked")

// Judge turns what a translation would return into an abstention when a's policy says so: the translator
// passes its result through it, and another loop over a Session passes its own. A nil a returns
// them unchanged.
func (a *Abstain) Judge(p Program, tr Trace, err error) (Program, Trace, error) {
	if a == nil {
		return p, tr, err
	}
	var spent *BudgetSpent
	var declined *Declined
	switch {
	case err == nil:
		if a.MinAgreement > 0 && p.Confidence.Agreement() < a.MinAgreement {
			why := fmt.Sprintf("%d of %d drafts agreed on its rows", p.Confidence.Agreed, p.Confidence.Drawn)
			return Program{}, tr, &Abstained{Reason: AbstainLowAgreement, Draft: p, Diagnosis: why}
		}
	case errors.As(err, &spent):
		if a.Budget {
			return Program{}, tr, closest(AbstainBudget, tr, err)
		}
	case a.Declined && errors.As(err, &declined):
		return Program{}, tr, &Abstained{Reason: AbstainDeclined, Diagnosis: declined.Why, Err: err}
	case errors.Is(err, ErrNoProgram):
		n := len(tr.Tries)
		if !a.NoAnswer || n > 0 && tr.Tries[n-1].Kind == KindModel {
			break
		}
		reason := AbstainNoAnswer
		if n > 0 && tr.Tries[n-1].Kind == KindRepeat {
			reason = AbstainRepeat
		}
		return Program{}, tr, closest(reason, tr, err)
	}
	return p, tr, err
}

// closest is an abstention offering the refused try that came nearest an answer: a repeat is
// skipped for the draft it repeats, and a model's failure has no draft.
func closest(reason string, tr Trace, err error) *Abstained {
	a := &Abstained{Reason: reason, Err: err, Diagnosis: err.Error()}
	best, rank := -1, 0
	for i, t := range tr.Tries {
		if t.Err == nil || t.Kind == KindRepeat || t.Kind == KindModel || t.Kind == KindDeclined {
			continue
		}
		r := 1
		switch t.Class() {
		case ClassGrounding:
			r = 3
		case ClassSemantics:
			r = 2
		}
		if r >= rank {
			best, rank = i, r
		}
	}
	if best < 0 {
		return a
	}
	t := tr.Tries[best]
	a.Draft = Program{Text: t.Text, Model: t.Model, Language: t.Language}
	if t.explain != nil {
		a.Diagnosis = t.explain(t.Err)
	} else {
		a.Diagnosis = t.Err.Error()
	}
	return a
}
