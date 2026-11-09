package nl

import (
	"errors"
	"fmt"
)

// Tiers is a per-question policy over two tiers of model (#538): a local one, cheap and often small,
// and a frontier one, dearer and stronger. A host picks the policy per call to trade cost against how
// often a question gets answered, and caps each tier on its own.
type Tiers struct {
	// Policy is TierLocal, TierLocalThenFrontier or TierFrontier.
	Policy string
	// Local and Frontier cap each tier's tokens, model calls and time. A zero Budget takes
	// Options.Budget, so each tier gets that much again.
	Local, Frontier Budget
}

// The policies Tiers.Policy takes, and the tiers a Try, a Step and a Trace name.
const (
	// TierLocal asks the local model alone, and returns what it returns, an abstention included.
	TierLocal = "local"
	// TierLocalThenFrontier asks the local model, and the frontier model when the local one doesn't
	// answer: it abstained, failed or ran out of its budget. The frontier model starts a fresh
	// conversation from the local one's closest draft and why it wasn't taken (Prompts.Handoff).
	TierLocalThenFrontier = "local-then-frontier"
	// TierFrontier asks the frontier model alone.
	TierFrontier = "frontier"
)

// Check refuses a policy that isn't one of the three.
func (t Tiers) Check() error {
	switch t.Policy {
	case TierLocal, TierLocalThenFrontier, TierFrontier:
		return nil
	}
	return fmt.Errorf("nl: tier policy %q: want %s, %s or %s", t.Policy, TierLocal, TierLocalThenFrontier, TierFrontier)
}

// For is the Budget tier runs under: its own cap when set, else base.
func (t Tiers) For(tier string, base Budget) Budget {
	b := t.Local
	if tier == TierFrontier {
		b = t.Frontier
	}
	if b == (Budget{}) {
		return base
	}
	return b
}

// Stamp marks every try, step and vote in tr with tier, for a loop that runs one tier at a time.
func (tr *Trace) Stamp(tier string) {
	for i := range tr.Tries {
		tr.Tries[i].Tier = tier
	}
	for i := range tr.Steps {
		tr.Steps[i].Tier = tier
	}
	for i := range tr.Votes {
		tr.Votes[i].Tier = tier
	}
}

// Then appends next, a later tier's trace, to tr: its steps count the tries before them across both.
func (tr *Trace) Then(next Trace) {
	for _, s := range next.Steps {
		s.After += len(tr.Tries)
		tr.Steps = append(tr.Steps, s)
	}
	tr.Tries = append(tr.Tries, next.Tries...)
	tr.Votes = append(tr.Votes, next.Votes...)
	tr.Escalations = append(tr.Escalations, next.Escalations...)
	tr.Offered = append(tr.Offered, next.Offered...)
	if next.Tier != "" {
		tr.Tier = next.Tier
	}
}

// HandoffDraft is the draft a later tier starts from after an earlier one returned err over tr: the
// Abstained's closest draft, or, for a failure that isn't an abstention, the refused try an Abstain
// with NoAnswer and Budget would have offered. It is the try itself, so Conversation.Handoff tells
// the model what that try was told. For an answer a vote disputed, it is the answer with the
// diagnosis as its error. ok is false when there's no draft to start from.
func HandoffDraft(tr Trace, err error) (Try, bool) {
	var ab *Abstained
	isAb := errors.As(err, &ab)
	if !isAb {
		_, _, err = (&Abstain{NoAnswer: true, Budget: true}).Judge(Program{}, tr, err)
		isAb = errors.As(err, &ab)
	}
	if !isAb || ab.Draft.Text == "" {
		return Try{}, false
	}
	for i := len(tr.Tries) - 1; i >= 0; i-- {
		if t := tr.Tries[i]; t.Err != nil && t.Text == ab.Draft.Text && t.Kind != KindRepeat {
			return t, true
		}
	}
	return Try{Model: ab.Draft.Model, Text: ab.Draft.Text, Language: ab.Draft.Language, Kind: KindAnswer, Err: fmt.Errorf("%s", ab.Diagnosis)}, true
}
