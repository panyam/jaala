package datalog

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// walking registers walk(?n), a generator walking steps steps before it emits one row, charging each
// step to its Allowance when spend is set, and counts the steps it took and what Left said first.
type walking struct {
	steps, taken int
	spend        bool
	left         int64
	budgeted     bool
}

func (w *walking) register(t *testing.T, v *ns.Vocabulary) {
	t.Helper()
	err := v.AddPredicate("walk", ns.Builtin{Arity: 1, Modes: [][]bool{{false}}, Gen: func(ctx context.Context, _ ns.Source, _ []ns.Arg, emit func([]ns.Value, []string) error) error {
		a, ok := ns.AllowanceFrom(ctx)
		if !ok {
			return errors.New("no allowance on the context")
		}
		w.taken = 0
		w.left, w.budgeted = a.Left()
		for range w.steps {
			w.taken++
			if w.spend {
				if err := a.Spend(1); err != nil {
					return err
				}
			}
		}
		return emit([]ns.Value{ns.N(float64(w.steps))}, nil)
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// A generator charging its own steps stops at the Eval's budget, having walked about that far, with
// the budget's own error. control: without a budget it walks every step, and Left says there is none.
func TestAGeneratorSpendingItsAllowanceStopsAtTheBudget(t *testing.T) {
	w := &walking{steps: 10_000, spend: true}
	v := std(graph())
	w.register(t, v)
	b := baseFor(v)
	q := mustParse(t, `walk(?n) => ?n`)
	_, err := both(q, b, Budget(100))
	var be *BudgetExceeded
	if !errors.As(err, &be) || be.Limit != 100 || !strings.HasPrefix(err.Error(), "query: evaluation stopped: work passed its budget of 100") {
		t.Fatalf("budgeted walk: %v", err)
	}
	if w.taken > 101 {
		t.Errorf("walked %d steps under a budget of 100", w.taken)
	}
	if !w.budgeted || w.left > 100 {
		t.Errorf("Left said %d, %v under a budget of 100", w.left, w.budgeted)
	}
	rows, err := both(q, b)
	if err != nil || len(rows) != 1 || w.taken != 10_000 {
		t.Errorf("control: unbudgeted walk took %d steps: %v, %v", w.taken, rows, err)
	}
	if w.budgeted {
		t.Errorf("control: Left says there is a budget when the Eval has none")
	}
}

// What a host spends is reported apart from the engine's work, overall and on the literal that
// called it, so Work stays the engine's own. control: the same walk not spending does the same work.
func TestWhatAHostSpendsIsReportedApartFromWork(t *testing.T) {
	report := func(spend bool) Report {
		w := &walking{steps: 500, spend: spend}
		v := std(graph())
		w.register(t, v)
		var r Report
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `node(?x), walk(?n) => ?x, ?n`), baseFor(v), Explain(&r)); err != nil {
			t.Fatal(err)
		}
		return r
	}
	spent, plain := report(true), report(false)
	if spent.Spent != 5*500 || plain.Spent != 0 {
		t.Errorf("Spent %d, and %d not spending", spent.Spent, plain.Spent)
	}
	if spent.Work != plain.Work {
		t.Errorf("work %d spending, %d not: spending moved the engine's work", spent.Work, plain.Work)
	}
	var lit *LiteralReport
	for _, l := range spent.Goal.Literals {
		if strings.HasPrefix(l.Literal, "walk(") {
			lit = l
		}
	}
	if lit == nil || lit.Spent != 5*500 {
		t.Errorf("walk's literal %+v", lit)
	}
	if text := spent.String(); !strings.Contains(text, "spent 2500 by the source") || !strings.Contains(text, ", spent 2500") {
		t.Errorf("report text:\n%s", text)
	}
}

// spendingLookups is a looking Source charging each lookup cost units of its own work.
type spendingLookups struct {
	*looking
	cost int64
}

func (s spendingLookups) Lookup(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	if a, ok := ns.AllowanceFrom(ctx); ok {
		if err := a.Spend(s.cost); err != nil {
			return nil, err
		}
	}
	return s.looking.Lookup(ctx, rel, bound)
}

// A Source's lookup spending past the budget stops the Eval with the budget's error as it is, not
// wrapped in the lookup, and BudgetExceeded counts what it spent. control: a smaller cost answers.
func TestASourceSpendingPastTheBudgetStopsTheEval(t *testing.T) {
	q := mustParse(t, closure+`reach("a", ?b) => ?b`)
	run := func(cost int64) ([]Row, error) {
		src := spendingLookups{looking: lookingAt(graph()), cost: cost}
		return (SemiNaive{}).Eval(bg, q, MustBase(std(graph()), src), Budget(200))
	}
	_, err := run(100)
	var be *BudgetExceeded
	if !errors.As(err, &be) || be.Work <= 200 || strings.Contains(err.Error(), "looking up") {
		t.Errorf("a lookup spending past the budget: %v", err)
	}
	if rows, err := run(1); err != nil || col(rows, "b") != "b,c,d" {
		t.Errorf("control: %v, %v", col(rows, "b"), err)
	}
}

// spendingReads is a Source charging cost units for each relation it reads whole.
type spendingReads struct {
	ns.Source
	cost int64
}

func (s spendingReads) TuplesContext(ctx context.Context, rel string) ([]ns.Tuple, error) {
	if a, ok := ns.AllowanceFrom(ctx); ok {
		if err := a.Spend(s.cost); err != nil {
			return nil, err
		}
	}
	return s.Source.Tuples(rel), nil
}

// spendingPushes is a pushing store charging cost units for each conjunction it evaluates.
type spendingPushes struct {
	*pushing
	cost int64
}

func (s spendingPushes) Push(ctx context.Context, c ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	if a, ok := ns.AllowanceFrom(ctx); ok {
		if err := a.Spend(s.cost); err != nil {
			return func(yield func([]ns.Tuple, error) bool) { yield(nil, err) }
		}
	}
	return s.pushing.Push(ctx, c)
}

// A whole read and a push spending past the budget stop the Eval with the budget's error as it is,
// as a lookup does. control: a smaller cost answers.
func TestReadsAndPushesSpendingPastTheBudgetStopTheEval(t *testing.T) {
	for name, src := range map[string]func(cost int64) ns.Source{
		"read": func(cost int64) ns.Source { return spendingReads{Source: graph(), cost: cost} },
		"push": func(cost int64) ns.Source { return spendingPushes{pushing: pushingAt(graph()), cost: cost} },
	} {
		q := mustParse(t, `edge(?a, ?b), edge(?b, ?c) => ?a, ?c`)
		run := func(cost int64) ([]Row, error) {
			return (SemiNaive{}).Eval(bg, q, MustBase(std(graph()), src(cost)), Budget(200))
		}
		_, err := run(1000)
		var be *BudgetExceeded
		if !errors.As(err, &be) || strings.Contains(err.Error(), "reading") || strings.Contains(err.Error(), "pushing") {
			t.Errorf("%s spending past the budget: %v", name, err)
		}
		if rows, err := run(1); err != nil || len(rows) != 2 {
			t.Errorf("control: %s: %d rows, %v", name, len(rows), err)
		}
	}
}

func TestNoAllowanceOutsideAnEval(t *testing.T) {
	if _, ok := ns.AllowanceFrom(context.Background()); ok {
		t.Errorf("a plain context carries an Allowance")
	}
}
