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
}

// A Result is how one Case went: the program Translate accepted and its Trace, the rows it answered
// as text, whether they were Want's, and the error when Translate or the evaluation failed.
type Result struct {
	Case
	Program Program
	Trace   Trace
	Got     [][]string
	Pass    bool
	Err     error
}

// FirstTry reports whether the case passed with the first draft.
func (r Result) FirstTry() bool { return r.Pass && len(r.Trace.Tries) == 1 }

// A Report scores a translator over a set of Cases: how many passed, on the first try and after
// retries, what the tries cost, and what the refused tries were refused for.
type Report struct {
	Cases                             []Result
	Passed, PassedFirstTry            int
	Tries                             int
	TokensIn, TokensOut, TokensCached int
	Latency                           time.Duration
	// Refused counts the refused tries by Kind. Checked counts those refused with a
	// datalog.CheckError, and Fixed those whose CheckError carried a Fix, so Fixed/Checked is the share
	// of check errors a model was told how to fix.
	Refused        map[string]int
	Checked, Fixed int
}

// Eval runs each case's question through translate and scores its answer over b (#326). translate is
// usually Translate closed over a vocabulary, a model and options; a host may wrap its own fallback or
// tool around it. A program Translate already ran over Options.Base keeps those rows; any other is
// evaluated over b with the planned SemiNaive. A case that fails, in translate or evaluating, fails
// alone: Eval always scores every case, and stops early only when ctx is done.
func Eval(ctx context.Context, cases []Case, b *datalog.Base, translate func(ctx context.Context, question string) (Program, Trace, error)) Report {
	rep := Report{Refused: map[string]int{}}
	for _, c := range cases {
		if ctx.Err() != nil {
			break
		}
		res := Result{Case: c}
		res.Program, res.Trace, res.Err = translate(ctx, c.Question)
		if res.Err == nil {
			rows := res.Program.Rows
			if rows == nil {
				rows, res.Err = datalog.SemiNaive{}.Eval(ctx, res.Program.Query, b)
			}
			res.Got = rowsText(rows)
			res.Pass = res.Err == nil && sameRows(res.Got, c.Want, c.Ordered)
		}
		rep.add(res)
	}
	return rep
}

func (rep *Report) add(res Result) {
	rep.Cases = append(rep.Cases, res)
	if res.Pass {
		rep.Passed++
		if res.FirstTry() {
			rep.PassedFirstTry++
		}
	}
	for _, t := range res.Trace.Tries {
		rep.Tries++
		rep.TokensIn += t.TokensIn
		rep.TokensOut += t.TokensOut
		rep.TokensCached += t.TokensCached
		rep.Latency += t.Latency
		if t.Err == nil {
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

// String writes the report as a model's run is read: a line per case (pass, tries and the kinds they
// were refused for), then the totals.
func (rep Report) String() string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "case\tpass\ttries\trefused")
	for _, r := range rep.Cases {
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
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", r.Name, pass, len(r.Trace.Tries), strings.Join(kinds, " "))
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
	fmt.Fprintf(&b, "passed %d/%d, %d on the first try; %d tries, tokens %d in (%d cached), %d out\n",
		rep.Passed, n, rep.PassedFirstTry, rep.Tries, rep.TokensIn, rep.TokensCached, rep.TokensOut)
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
