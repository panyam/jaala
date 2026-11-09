package nl

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
)

// withoutRows caps the rows withoutCulprit shows.
const withoutRows = 5

// withoutCulprit shows what the rest of an empty answer's goal gives the culprit's variables.
// Imagine a draft whose str.prefix(?g, "github.com/panyam/goapplib/") matched nothing. The model
// was told which literal failed and the first value it was called with, and still sent the same
// draft again. So q runs once more with that literal left out, and the text lists up to withoutRows
// distinct values of the variables the literal shares with the rest, such as ?g's real ids. It
// returns "" when the culprit isn't in the goal (a rule's literal, Why.Rule set), the program isn't
// jaala's own (a front end would read jaala's text), the literal shares no variable with the rest,
// or the run fails or answers nothing. It runs under o.Eval, the answer's own options and budget.
func withoutCulprit(ctx context.Context, q datalog.Query, why *datalog.EmptyReport, o Options) string {
	if why == nil || why.Rule != "" || o.FrontEnd != nil || o.Base == nil {
		return ""
	}
	at := culpritAt(q.Goal.Literals, why)
	if at < 0 {
		return ""
	}
	// an equality to a constant over the culprit's variables, ?l = "mit", is folded into the culprit's
	// atom before it runs (#370), so the atom is named; leaving the equality out is what shows ?l
	for _, i := range append(equalitiesOn(q.Goal.Literals, at), at) {
		if text := runWithout(ctx, q, i, o); text != "" {
			return text
		}
	}
	return ""
}

// runWithout runs q's goal with literal i left out, selecting the variables i shares with the rest, and
// writes up to withoutRows of them; "" when there are none to select, or none come back.
func runWithout(ctx context.Context, q datalog.Query, i int, o Options) string {
	lits := q.Goal.Literals
	rest := make([]datalog.Literal, 0, len(lits)-1)
	rest = append(rest, lits[:i]...)
	rest = append(rest, lits[i+1:]...)
	bound, pinned := map[datalog.Var]bool{}, map[datalog.Var]bool{}
	for _, l := range rest {
		for _, v := range literalVars(l) {
			bound[v] = true
		}
		if c := l.Compare; c != nil && c.Op == "=" {
			if c.Right.Const != nil {
				pinned[c.Left.Var] = true
			}
			if c.Left.Const != nil {
				pinned[c.Right.Var] = true
			}
		}
	}
	var cols []datalog.Term
	var names []string
	for _, v := range literalVars(lits[i]) {
		if bound[v] && !pinned[v] { // a variable the rest pins to a constant only shows the constant back
			cols = append(cols, datalog.Term{Var: v})
			names = append(names, "?"+string(v))
		}
	}
	if len(cols) == 0 {
		return ""
	}
	w := datalog.Query{Rules: q.Rules, Goal: datalog.Body{Literals: rest}, Select: cols, Limit: withoutRows}
	if datalog.Validate(w, o.Base.Vocabulary()) != nil {
		return ""
	}
	rows, err := datalog.SemiNaive{}.Eval(ctx, w, o.Base, o.Eval...)
	if err != nil || len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, o.Pack.prompts().Without, lits[i].String(), strings.Join(names, ", "))
	for _, r := range rows {
		vals := make([]string, len(cols))
		for j, c := range cols {
			vals[j] = quotedValue(r.Bind[c.Var])
		}
		b.WriteString("\n  " + strings.Join(vals, ", "))
	}
	return b.String()
}

// equalitiesOn are the goal's comparisons of a variable of literal at to a constant with =.
func equalitiesOn(goal []datalog.Literal, at int) []int {
	vars := map[datalog.Var]bool{}
	for _, v := range literalVars(goal[at]) {
		vars[v] = true
	}
	var out []int
	for i, l := range goal {
		c := l.Compare
		if i == at || c == nil || c.Op != "=" {
			continue
		}
		if vars[c.Left.Var] && c.Right.Const != nil || vars[c.Right.Var] && c.Left.Const != nil {
			out = append(out, i)
		}
	}
	return out
}

// culpritAt is the goal literal printing as why's literal ran, -1 when none does: a literal a
// rewrite moved there (an inlined rule's) isn't the model's to leave out.
func culpritAt(goal []datalog.Literal, why *datalog.EmptyReport) int {
	for i, l := range goal {
		if l.String() == why.Literal {
			return i
		}
	}
	return -1
}

// literalVars are the named variables a literal reads, in order, each once: an atom's arguments,
// a negation's, and a comparison's two sides.
func literalVars(l datalog.Literal) []datalog.Var {
	var out []datalog.Var
	add := func(t datalog.Term) {
		if t.Var != "" && t.Var != "_" && !strings.HasPrefix(string(t.Var), "_") {
			for _, v := range out {
				if v == t.Var {
					return
				}
			}
			out = append(out, t.Var)
		}
	}
	for _, a := range []*datalog.Atom{l.Pos, l.Neg} {
		if a != nil {
			for _, t := range a.Args {
				add(t)
			}
		}
	}
	if c := l.Compare; c != nil {
		add(c.Left)
		add(c.Right)
	}
	return out
}
