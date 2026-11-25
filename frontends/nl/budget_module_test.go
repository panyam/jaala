package nl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// When the work went into a module's rule, the budget's refusal names the program's own literal
// that reaches it (#569): on Declaire's x03 the line named a literal inside http.arg_path, which
// the model can't change, so it sent the same draft again. control: work in the program's own
// goal names no caller.
func TestABudgetRefusalNamesTheProgramsLiteralIntoAModule(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	// two rules each, so neither is inlined
	rules := "triple(?a, ?b) :- pkg(?a, ?l), pkg(?b, ?m), pkg(?c, ?n);\ntriple(?a, ?b) :- pkg(?a, \"MIT\"), pkg(?b, \"MIT\");\n" +
		"wide(?a) :- triple(?a, ?b);\nwide(?a) :- pkg(?a, \"none\");"
	if err := v.AddModule("lib", datalog.LanguageName, rules, ""); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, b.Source())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Eval: []datalog.Option{datalog.Budget(40)}})
	_, _, err = s.Draw(ctx, reply(`pkg(?a, "MIT"), lib.wide(?a) => ?a`))
	var bs *nl.BudgetStopped
	if !errors.As(err, &bs) {
		t.Fatalf("the draft's error %v", err)
	}
	if strings.Contains(bs.Where, "No literal before it binds") {
		t.Errorf("?a is bound by pkg(?a, \"MIT\") before lib.wide(?a), but the line says %q", bs.Where)
	}
	if bs.Through != "lib.wide(?a)" || !strings.HasPrefix(bs.Rule, "lib.triple(") ||
		!strings.HasPrefix(bs.Where, "Most of the work went to lib.wide(?a), through "+bs.Literal+" in the rule "+bs.Rule+": ") {
		t.Errorf("through %q, literal %q, rule %q, told %q", bs.Through, bs.Literal, bs.Rule, bs.Where)
	}
	_, _, err = s.Draw(ctx, reply(`lib.wide(?a) => ?a`))
	if !errors.As(err, &bs) || bs.Through != "lib.wide(?a)" || !strings.Contains(bs.Where, "\nNo literal before it binds ?a, so ") {
		t.Errorf("a call into the module first: through %q, told %q", bs.Through, bs.Where)
	}
	// two rules, so mine isn't inlined; the call into the module is in mine's body
	_, _, err = s.Draw(ctx, reply(`mine(?a) :- lib.wide(?a); mine(?a) :- pkg(?a, "none"); pkg(?b, "MIT"), mine(?b) => ?b`))
	if !errors.As(err, &bs) || bs.Through != "lib.wide(?a)" {
		t.Errorf("through an own rule: through %q, told %q", bs.Through, bs.Where)
	}
	_, _, err = s.Draw(ctx, reply(`pkg(?a, ?l), pkg(?b, ?m), pkg(?c, ?n) => ?a`))
	if !errors.As(err, &bs) || bs.Through != "" || strings.Contains(bs.Where, ", through ") || !strings.Contains(bs.Where, "\nNo literal before it binds ?c, ?n, so ") {
		t.Errorf("control: the goal's own work, through %q, told %q", bs.Through, bs.Where)
	}
}
