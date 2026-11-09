package datalog

import (
	"fmt"
	"slices"
)

// A Warning is something a program likely got wrong although it checks and runs (#477). It changes no
// answer, and a host shows it beside the query's other diagnostics.
type Warning struct {
	// Rule is the rule the warning is about, as written, or "" for the goal.
	Rule string `json:"rule,omitempty"`
	// Vars are the two variables that may bind one value, without "?".
	Vars [2]Var `json:"vars"`
	// Atoms are the two atoms, as written, that differ only in those variables.
	Atoms [2]string `json:"atoms"`
	// Message says all of the above in one line.
	Message string `json:"message"`
}

// Lint reports the likely mistakes in q's rules and goal that no check refuses. Today that is one
// kind (#477): two positive atoms of one relation that are the same but for one position, holding
// two different variables there (parallelPair, NonInjectiveRules' test), with no comparison between
// the two anywhere in the body and no other literal reading both. Any operator counts: ?a < ?b and ?a <= ?b both say the author thought
// about the pair, where NonInjectiveRules, an agni-shaped check of rules alone, takes only !=. A body
// matches by homomorphism, so both variables may bind the same value: term(?y, ?b), term(?y, ?c)
// with no ?b != ?c matches each term of ?y as both. That is sometimes meant (pairs of siblings,
// each with itself), so it is a warning rather than an error, and Lint needs no vocabulary: it reads
// the program as written. Rules come first, in order, then the goal.
func Lint(q Query) []Warning {
	var out []Warning
	for _, r := range q.Rules {
		out = append(out, lintBody(r.Body, r.String())...)
	}
	return append(out, lintBody(q.Goal, "")...)
}

// lintBody is Lint over one body, each pair of variables reported once, at the first pair of atoms
// showing it.
func lintBody(b Body, rule string) []Warning {
	var atoms []*Atom
	for _, l := range b.Literals {
		if l.Pos != nil {
			atoms = append(atoms, l.Pos)
		}
	}
	var out []Warning
	seen := map[[2]Var]bool{}
	for i, a := range atoms {
		for _, c := range atoms[i+1:] {
			if a.Fact != "" || c.Fact != "" {
				continue // two facts bound by id are two facts
			}
			x, y, ok := parallelPair(a, c)
			if !ok {
				continue
			}
			if y < x {
				x, y = y, x
			}
			if seen[[2]Var{x, y}] || related(b, x, y, a, c) {
				continue
			}
			seen[[2]Var{x, y}] = true
			as, cs := a.String(), c.String()
			msg := fmt.Sprintf("?%s and ?%s may bind the same value: %s and %s differ only there, and nothing compares them (write ?%s != ?%s if they must differ)", x, y, as, cs, x, y)
			if rule != "" {
				msg = fmt.Sprintf("in rule %q: %s", rule, msg)
			}
			out = append(out, Warning{Rule: rule, Vars: [2]Var{x, y}, Atoms: [2]string{as, cs}, Message: msg})
		}
	}
	return out
}

// related reports whether something else in b reads both x and y: a comparison, under any operator,
// or a third literal. probed_both(?r) :- tt(?r, ?a, ?b), has_tp(?a), has_tp(?b) is about the pair
// tt names, which tt's own rule may keep apart.
func related(b Body, x, y Var, a, c *Atom) bool {
	for _, l := range b.Literals {
		var vs []Var
		switch {
		case l.Compare != nil:
			vs = append(termVars(l.Compare.Left), termVars(l.Compare.Right)...)
		case l.Pos != nil && l.Pos != a && l.Pos != c:
			vs = atomVars(l.Pos)
		case l.Neg != nil:
			vs = atomVars(l.Neg)
		}
		if slices.Contains(vs, x) && slices.Contains(vs, y) {
			return true
		}
	}
	return false
}

// atomVars are the variables an atom's arguments read.
func atomVars(a *Atom) []Var {
	var vs []Var
	for _, t := range a.Args {
		vs = append(vs, termVars(t)...)
	}
	return vs
}
