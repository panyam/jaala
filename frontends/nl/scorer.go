package nl

import (
	"context"
	"errors"
)

// A Rung is one tier's go at a question under Options.Tiers, as a Scorer reads it (#572): what the
// tier answered, judged by Options.Abstain, and what it took.
type Rung struct {
	// Tier is the tier's name (TierLocal or TierFrontier).
	Tier string
	// Program is the tier's answer, and Err why it has none. Abstain has judged both, so a doubted
	// answer arrives as an *Abstained.
	Program Program
	Err     error
	// Trace is the question's trace so far, this tier's tries, steps and votes last.
	Trace Trace
	// Last says no tier follows this one, so an escalation abstains instead.
	Last bool
}

// The verdicts a Score gives.
const (
	// ScoreAccept takes the rung's result as the question's, an error included, and asks no more
	// tiers.
	ScoreAccept = "accept"
	// ScoreEscalate asks the next tier, from this rung's closest draft and the Score's reason. On the
	// last rung it abstains.
	ScoreEscalate = "escalate"
	// ScoreAbstain answers "not confident" (an *Abstained, AbstainScorer) with the rung's answer as
	// the draft, and asks no more tiers.
	ScoreAbstain = "abstain"
)

// A Score is a Scorer's verdict on a rung, and why. The reason goes to the next tier with the
// handoff, and into Trace.Scores.
type Score struct {
	Verdict, Reason string
}

// A Scorer decides, after each tier, whether the translator takes the tier's result or asks the
// next one (#572). Local-then-frontier used to escalate only a local tier that abstained, failed or
// ran out of budget, so a program that ran and answered wrongly never reached the frontier tier: on
// Declaire's bench/families 14b answered 86 of 191 questions wrongly, and only its 61 abstentions
// could. A host's Scorer runs the checks it knows, such as "every row is a package in the module the
// question names", and escalates an answer that fails them.
//
// A nil Scorer is DefaultScore.
type Scorer func(ctx context.Context, r Rung) Score

// AbstainScorer is the reason of an abstention a Scorer asked for, by ScoreAbstain or by escalating
// the last rung.
const AbstainScorer = "scorer"

// DefaultScore is the translator's verdict without a Scorer: accept an answer, escalate anything
// else while a tier is left, and accept what the last tier gives. It is local-then-frontier as #538
// wrote it. Its reason names why the tier didn't answer in a word, so Report.Escalated counts by it:
// the abstention's reason, "budget", or "no answer".
func DefaultScore(_ context.Context, r Rung) Score {
	if r.Err == nil || r.Last {
		return Score{Verdict: ScoreAccept}
	}
	var ab *Abstained
	var spent *BudgetSpent
	switch {
	case errors.As(r.Err, &ab):
		return Score{Verdict: ScoreEscalate, Reason: ab.Reason}
	case errors.As(r.Err, &spent):
		return Score{Verdict: ScoreEscalate, Reason: AbstainBudget}
	}
	return Score{Verdict: ScoreEscalate, Reason: AbstainNoAnswer}
}

// A RungScore is a Scorer's verdict on one tier, as Trace.Scores keeps it.
type RungScore struct {
	Tier, Verdict, Reason string
}

// Settle judges a tier's result for a loop over Options.Tiers (#572): Options.Abstain first, then the
// Scorer. It returns what the question answers when no tier follows, and next true when the next
// tier should be asked, with err then carrying the draft HandoffDraft reads. tr gains the verdict in
// Scores. A Score with no verdict accepts.
func Settle(ctx context.Context, o Options, tier string, p Program, tr Trace, err error, last bool) (Program, Trace, error, bool) {
	p, tr, err = o.Abstain.Judge(p, tr, err)
	score := o.Scorer
	if score == nil {
		score = DefaultScore
	}
	s := score(ctx, Rung{Tier: tier, Program: p, Err: err, Trace: tr, Last: last})
	if s.Verdict == "" {
		s.Verdict = ScoreAccept
	}
	tr.Scores = append(tr.Scores, RungScore{Tier: tier, Verdict: s.Verdict, Reason: s.Reason})
	switch {
	case s.Verdict == ScoreEscalate && !last:
		if err == nil {
			err = &Abstained{Reason: AbstainScorer, Draft: p, Diagnosis: s.Reason}
		}
		return Program{}, tr, err, true
	case s.Verdict == ScoreEscalate, s.Verdict == ScoreAbstain:
		var ab *Abstained
		if errors.As(err, &ab) {
			return Program{}, tr, err, false
		}
		if err != nil {
			a := closest(AbstainScorer, tr, err)
			a.Diagnosis = s.Reason
			return Program{}, tr, a, false
		}
		return Program{}, tr, &Abstained{Reason: AbstainScorer, Draft: p, Diagnosis: s.Reason}, false
	}
	return p, tr, err, false
}
