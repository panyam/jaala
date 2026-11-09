package nl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A Case is a question and the rows a right program answers it with (#326). Each row of Want is that
// row's values as text, in any order: the columns' names and order are the program's to choose, so a
// row matches by its values alone. An absent value is "absent". Rows match as a multiset, in any
// order unless Ordered is set.
type Case struct {
	Name     string
	Question string
	Want     [][]string
	Ordered  bool
	// Tags name the kinds of query the question needs (#536), such as "one hop", "closure", "join
	// across kinds", "aggregation" or "negation". They are the host's words: Summary counts each tag's
	// outcomes apart, so a run says which kinds a model gets wrong.
	Tags []string
	// Reads, when set, are the relations a right program reads (#537), the vocabulary's names as
	// written. A model profile's schema choice checks that the accepted program reads each of them.
	Reads []string
}

// A Result is how one Case went: the program the translator accepted and its Trace, the rows it answered
// as text, whether they were Want's, and the error when the translator or the evaluation failed.
type Result struct {
	Case
	Program Program
	Trace   Trace
	Got     [][]string
	Pass    bool
	Err     error
	// Took is the case's wall time: the translator's run and the evaluation over Eval's Base.
	Took time.Duration
	// WouldPass, for a case that abstained with a draft Eval could run, says whether that draft would
	// have answered Want's rows (#537). A model profile's calibration reads it: an abstention is well
	// placed when its draft was wrong. It is nil when there was no draft to run.
	WouldPass *bool
}

// The outcomes a Result has (Result.Outcome).
const (
	OutcomePass    = "pass"    // it answered Want's rows
	OutcomeWrong   = "wrong"   // it answered other rows, or failed
	OutcomeAbstain = "abstain" // it said it had no answer it was confident of
)

// Outcome is pass, wrong or abstain (#536). An abstention is its own outcome, not a wrong answer: a
// caller acting on the answer loses less to none than to a wrong one. A case abstains when the
// translator's error is an *Abstained, Options.Abstain's or a host's own agent's (AbstainAgent). A
// model that failed, a spent budget without Abstain.Budget and a wrong program are all wrong.
func (r Result) Outcome() string {
	var ab *Abstained
	switch {
	case r.Pass:
		return OutcomePass
	case errors.As(r.Err, &ab):
		return OutcomeAbstain
	}
	return OutcomeWrong
}

// AbstainReason is the Abstained's Reason for a case that abstained, and "" otherwise.
func (r Result) AbstainReason() string {
	var ab *Abstained
	if !r.Pass && errors.As(r.Err, &ab) {
		return ab.Reason
	}
	return ""
}

// FirstTry reports whether the case passed with the first draft.
func (r Result) FirstTry() bool { return r.Pass && len(r.Trace.Tries) == 1 }

// Class sorts a failed case by what went wrong (ClassGrounding and the rest), "" for one that
// passed: abstained when the translator abstained (Options.Abstain), the budget when it ran out, the last
// try's class when the translator failed, and semantics for a program that answered the wrong rows.
func (r Result) Class() string {
	var bs *BudgetSpent
	var ab *Abstained
	switch {
	case r.Pass:
		return ""
	case errors.As(r.Err, &ab):
		return ClassAbstained
	case errors.As(r.Err, &bs):
		return ClassBudget
	case len(r.Trace.Tries) == 0:
		return ClassModel
	}
	if c := r.Trace.Tries[len(r.Trace.Tries)-1].Class(); c != "" {
		return c
	}
	if r.Err != nil {
		return ClassConstruct // the program Translate accepted failed evaluating over the case's Base
	}
	return ClassSemantics
}

// A Report scores a translator over a set of Cases: how many passed, on the first try and after
// retries, what the tries cost, and what the refused tries were refused for.
type Report struct {
	Cases                  []Result
	Passed, PassedFirstTry int
	Tries                  int
	// Steps counts the actions the models asked for (Options.Actions), whose tokens and latency are
	// in the totals beside the tries'.
	Steps int
	// Votes counts the extra drafts Options.Vote drew, whose tokens and latency are in the totals
	// too, and Escalations the answers moved on from for agreeing too little.
	Votes, Escalations                int
	TokensIn, TokensOut, TokensCached int
	Latency                           time.Duration
	// Refused counts the refused tries by Kind. Checked counts those refused with a
	// datalog.CheckError, and Fixed those whose CheckError carried a Fix, so Fixed/Checked is the share
	// of check errors a model was told how to fix.
	Refused        map[string]int
	Checked, Fixed int
	// Failures counts the cases that failed by Result.Class.
	Failures map[string]int
	// Repairs counts the drafts the harness repaired itself and then accepted (Try.Repair).
	Repairs int
	// Wrong and Abstained count the cases by Result.Outcome, beside Passed: an abstention isn't a
	// wrong answer.
	Wrong, Abstained int
}

// Eval runs each case's question through translate and scores its answer over b (#326). translate is
// usually the translator closed over a vocabulary, a model and options; a host may wrap its own fallback or
// tool around it. A program the translator already ran over Options.Base keeps those rows; any other is
// evaluated over b with the planned SemiNaive. A case that fails, in translate or evaluating, fails
// alone: Eval always scores every case, and stops early only when ctx is done.
func Eval(ctx context.Context, cases []Case, b *datalog.Base, translate func(ctx context.Context, question string) (Program, Trace, error)) Report {
	rep := Report{Refused: map[string]int{}, Failures: map[string]int{}}
	for _, c := range cases {
		if ctx.Err() != nil {
			break
		}
		res := Result{Case: c}
		start := time.Now()
		res.Program, res.Trace, res.Err = translate(ctx, c.Question)
		if res.Err == nil {
			rows := res.Program.Rows
			if rows == nil {
				rows, res.Err = datalog.SemiNaive{}.Eval(ctx, res.Program.Query, b)
			}
			res.Got = rowsText(rows)
			res.Pass = res.Err == nil && sameRows(res.Got, c.Want, c.Ordered)
		}
		res.Took = time.Since(start)
		res.WouldPass = wouldPass(ctx, res, b)
		rep.add(res)
	}
	return rep
}

func (rep *Report) add(res Result) {
	rep.Cases = append(rep.Cases, res)
	switch res.Outcome() {
	case OutcomePass:
		rep.Passed++
		if res.FirstTry() {
			rep.PassedFirstTry++
		}
	case OutcomeWrong:
		rep.Wrong++
	case OutcomeAbstain:
		rep.Abstained++
	}
	if c := res.Class(); c != "" {
		if rep.Failures == nil {
			rep.Failures = map[string]int{}
		}
		rep.Failures[c]++
	}
	rep.Escalations += len(res.Trace.Escalations)
	for _, t := range res.Trace.Votes {
		rep.Votes++
		rep.TokensIn += t.TokensIn
		rep.TokensOut += t.TokensOut
		rep.TokensCached += t.TokensCached
		rep.Latency += t.Latency
	}
	for _, s := range res.Trace.Steps {
		rep.Steps++
		rep.TokensIn += s.TokensIn
		rep.TokensOut += s.TokensOut
		rep.TokensCached += s.TokensCached
		rep.Latency += s.Latency
	}
	for _, t := range res.Trace.Tries {
		rep.Tries++
		rep.TokensIn += t.TokensIn
		rep.TokensOut += t.TokensOut
		rep.TokensCached += t.TokensCached
		rep.Latency += t.Latency
		if t.Err == nil {
			if t.Repair != "" {
				rep.Repairs++
			}
			continue
		}
		rep.Refused[t.Kind]++
		var ce *datalog.CheckError
		if errors.As(t.Err, &ce) {
			rep.Checked++
			if ce.Fix != "" {
				rep.Fixed++
			}
		}
	}
}

// Language is the front end every try was written in (#354), so two runs' reports say which language
// each scored: "" for jaala's, and "mixed" when tries differ.
func (rep Report) Language() string {
	lang, seen := "", false
	for _, r := range rep.Cases {
		for _, t := range r.Trace.Tries {
			if !seen {
				lang, seen = t.Language, true
			} else if t.Language != lang {
				return "mixed"
			}
		}
	}
	return lang
}

// A Draft is one try of one case, as Costliest ranks them.
type Draft struct {
	Case string
	N    int // the try's place in the case's Trace, from 1
	Try  Try
}

// Costliest is the n tries whose evaluation over Options.Base did the most work, most first, a tie in
// case and try order (#343). Work rather than time ranks them, since it is the same on every run; a
// Try's Report says which rule and literal spent it. Tries never evaluated (no Base, or refused
// before) aren't ranked.
func (rep Report) Costliest(n int) []Draft {
	var out []Draft
	for _, r := range rep.Cases {
		for i, t := range r.Trace.Tries {
			if t.Report != nil {
				out = append(out, Draft{Case: r.Name, N: i + 1, Try: t})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Try.Report.Work > out[j].Try.Report.Work })
	return out[:min(n, len(out))]
}

// costliestLiteral is the literal of r's goal or rules that did the most work, the first among equals.
func costliestLiteral(r *datalog.Report) *datalog.LiteralReport {
	var top *datalog.LiteralReport
	for _, br := range append([]*datalog.BodyReport{r.Goal}, r.Rules...) {
		if br == nil {
			continue
		}
		for _, l := range br.Literals {
			if top == nil || l.Work > top.Work {
				top = l
			}
		}
	}
	return top
}

// String writes the report as a model's run is read: a line per case (pass, tries, the work of the
// program it answered with when the translator evaluated it, and the kinds its tries were refused for), the
// totals, then the three costliest drafts and where their work went.
func (rep Report) String() string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	works := make([]string, len(rep.Cases))
	worked := false
	for i, r := range rep.Cases {
		if n := len(r.Trace.Tries); r.Err == nil && n > 0 && r.Trace.Tries[n-1].Report != nil {
			works[i], worked = fmt.Sprint(r.Trace.Tries[n-1].Report.Work), true
		}
	}
	if worked {
		fmt.Fprintln(w, "case\tpass\ttries\twork\trefused")
	} else {
		fmt.Fprintln(w, "case\tpass\ttries\trefused")
	}
	for i, r := range rep.Cases {
		var kinds []string
		for _, t := range r.Trace.Tries {
			if t.Err != nil {
				kinds = append(kinds, t.Kind)
			}
		}
		pass := "no"
		if r.Pass {
			pass = "yes"
		}
		if worked {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.Name, pass, len(r.Trace.Tries), works[i], strings.Join(kinds, " "))
		} else {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", r.Name, pass, len(r.Trace.Tries), strings.Join(kinds, " "))
		}
	}
	w.Flush()
	table := b.String()
	b.Reset()
	for _, line := range strings.SplitAfter(table, "\n") {
		b.WriteString(strings.TrimRight(line, " \n"))
		if strings.HasSuffix(line, "\n") {
			b.WriteString("\n")
		}
	}
	n := len(rep.Cases)
	in := ""
	if l := rep.Language(); l != "" {
		in = " in " + l
	}
	steps := ""
	if rep.Steps > 0 {
		steps = fmt.Sprintf(" and %d steps", rep.Steps)
	}
	if rep.Votes > 0 {
		steps += fmt.Sprintf(", %d votes", rep.Votes)
	}
	if rep.Escalations > 0 {
		steps += fmt.Sprintf(", %d escalated", rep.Escalations)
	}
	fmt.Fprintf(&b, "passed %d/%d%s, %d on the first try; %d tries%s, tokens %d in (%d cached), %d out\n",
		rep.Passed, n, in, rep.PassedFirstTry, rep.Tries, steps, rep.TokensIn, rep.TokensCached, rep.TokensOut)
	var kinds []string
	for k := range rep.Refused {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var parts []string
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, rep.Refused[k]))
	}
	fmt.Fprintf(&b, "refused: %s; check errors with a fix %d/%d\n", strings.Join(parts, ", "), rep.Fixed, rep.Checked)
	if top := rep.Costliest(3); len(top) > 0 {
		b.WriteString("costliest drafts:\n")
		for _, d := range top {
			kind := d.Try.Kind
			if kind == "" {
				kind = "accepted"
			}
			fmt.Fprintf(&b, "  %s try %d (%s): work %d", d.Case, d.N, kind, d.Try.Report.Work)
			if l := costliestLiteral(d.Try.Report); l != nil && l.Work > 0 {
				fmt.Fprintf(&b, ", most in %s (%d)", l.Literal, l.Work)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// rowsText is each row's values as text, in its columns' name order so a row reads the same however
// it was built.
func rowsText(rows []datalog.Row) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		cols := make([]string, 0, len(r.Bind))
		for c := range r.Bind {
			cols = append(cols, string(c))
		}
		sort.Strings(cols)
		vals := make([]string, len(cols))
		for j, c := range cols {
			vals[j] = valueText(r.Bind[datalog.Var(c)])
		}
		out[i] = vals
	}
	return out
}

// valueText is a value as a Case writes it: its text, or "absent".
func valueText(v ns.Value) string {
	if v.Absent {
		return "absent"
	}
	return v.S
}

// sameRows reports whether got holds want's rows, each matched by its values as a multiset, and in
// want's order when ordered.
func sameRows(got, want [][]string, ordered bool) bool {
	if len(got) != len(want) {
		return false
	}
	g, w := make([]string, len(got)), make([]string, len(want))
	for i := range got {
		g[i], w[i] = rowKey(got[i]), rowKey(want[i])
	}
	if !ordered {
		slices.Sort(g)
		slices.Sort(w)
	}
	return slices.Equal(g, w)
}

// rowKey is a row's values sorted and joined, so two rows with the same values in another order match.
func rowKey(vals []string) string {
	s := slices.Clone(vals)
	slices.Sort(s)
	return strings.Join(s, "\x00")
}
