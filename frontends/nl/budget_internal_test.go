package nl

import (
	"errors"
	"testing"

	"github.com/panyam/jaala/datalog"
)

// budgetWhere names the literal that did the most work, which needn't be the one solving when the
// budget ran out, and on a tie the one solving. control: a report with no work leaves the error as
// it was.
func TestBudgetWhereNamesTheCostliestLiteral(t *testing.T) {
	be := &datalog.BudgetExceeded{Work: 101, Limit: 100}
	for _, c := range []struct {
		name string
		lits []*datalog.LiteralReport
		want string
	}{
		{"most work", []*datalog.LiteralReport{{Literal: "a(?x)", Work: 90}, {Literal: "b(?x)", Work: 11, Stopped: true}}, "a(?x)"},
		{"tie", []*datalog.LiteralReport{{Literal: "a(?x)", Work: 50}, {Literal: "b(?x)", Work: 50, Stopped: true}}, "b(?x)"},
	} {
		err := budgetWhere(be, &datalog.Report{Goal: &datalog.BodyReport{Literals: c.lits}}, datalog.Query{}, nil, DefaultPrompts())
		var bs *BudgetStopped
		if !errors.As(err, &bs) || bs.Literal != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	idle := &datalog.Report{Goal: &datalog.BodyReport{Literals: []*datalog.LiteralReport{{Literal: "a(?x)"}}}}
	if err := budgetWhere(be, idle, datalog.Query{}, nil, DefaultPrompts()); err != be {
		t.Errorf("control: with no work, %v", err)
	}
}
