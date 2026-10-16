package datalog

import (
	"fmt"

	"github.com/panyam/jaala/ns"
)

// checkComparisonUnits refuses an ordering comparison whose two sides have known, different units
// (#190), in the goal, each rule and each having: volts are never less than amps, so the query is
// wrong, not its data empty. It is the static half of evalCompare's rule that unlike dimensions have
// no order, which still answers no match for what only the data shows. It runs on the linked
// program after bindGoal, so a value the host bound is a constant and its unit counts.
func checkComparisonUnits(q Query, reg *ns.Vocabulary) error {
	t := newTyper(reg, q.Rules)
	for _, r := range q.Rules {
		if err := t.checkUnits(whereRule(r), r.Head.Relation, r.Body); err != nil {
			return err
		}
	}
	if err := t.checkUnits("the query", "", q.Goal); err != nil {
		return err
	}
	for _, h := range q.Having {
		left := ""
		if a := h.Left.Agg; a != nil && (a.Func == "sum" || a.Func == "min" || a.Func == "max") {
			left = t.unitOf(Term{Var: a.Var}, q.Goal, nil)
		}
		if err := unlikeUnits("having", Compare{Left: h.Left, Op: h.Op, Right: h.Right}, left, t.unitOf(h.Right, q.Goal, nil)); err != nil {
			return err
		}
	}
	return nil
}

// checkUnits checks one body's comparisons. rel is the relation the body defines, which it can't
// type itself by.
func (t *typer) checkUnits(where, rel string, body Body) error {
	var seen map[string]bool
	if rel != "" {
		seen = map[string]bool{rel: true}
	}
	for _, l := range body.Literals {
		if c := l.Compare; c != nil {
			if err := unlikeUnits(where, *c, t.unitOf(c.Left, body, seen), t.unitOf(c.Right, body, seen)); err != nil {
				return err
			}
		}
	}
	return nil
}

func unlikeUnits(where string, c Compare, left, right string) error {
	if !orderingOps[c.Op] || left == "" || right == "" || left == right {
		return nil
	}
	return checkErr(KindType, fmt.Sprintf("query: %s %s %s in %s compares number[%s] with number[%s], which never holds", c.Left, c.Op, c.Right, where, left, right))
}

// unitOf is a term's unit when it is known: a constant's own (one the host bound can carry one), or
// a variable's when every positive atom binding it declares a number in that unit. A binder that
// declares no unit, or another one, leaves it unknown, since the value may come from there and a
// number without a unit compares with any.
func (t *typer) unitOf(term Term, body Body, seen map[string]bool) string {
	if term.Const != nil {
		if term.Const.Num != nil {
			return term.Const.BaseUnit
		}
		return ""
	}
	if term.Var == "" || term.Var == "_" {
		return ""
	}
	unit := ""
	for _, l := range body.Literals {
		a := l.Pos
		if a == nil {
			continue
		}
		for j, arg := range a.Args {
			if arg.Var != term.Var {
				continue
			}
			_, types, ok := t.argTypes(a.Relation, seen)
			if !ok || j >= len(types) || types[j].Type != ns.TypeNumber || types[j].Unit == "" {
				return ""
			}
			if unit != "" && unit != types[j].Unit {
				return ""
			}
			unit = types[j].Unit
		}
	}
	return unit
}
