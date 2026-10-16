// Package demo runs the examples on jaala's documentation site. The site build renders each answer
// into its page, the docsite's tests check every example's pinned answer, and the browser runs edits
// through the same Run compiled to wasm (#107), so a page can't show an answer the engine doesn't give.
package demo

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// Spec is one example: facts, rules and a goal, and optionally what it must answer.
type Spec struct {
	// Fixture names a fact set in fixtures/, such as "graph". Facts are added to it.
	Fixture string `yaml:"fixture" json:"fixture,omitempty"`
	// Facts are ground atoms, one per line or separated by ";": edge("a", "b"), weight("a", 1).
	Facts string `yaml:"facts" json:"facts,omitempty"`
	// Program is the rules, and Query the goal with its projection: reach("a", ?x) => ?x.
	Program string `yaml:"program" json:"program,omitempty"`
	Query   string `yaml:"query" json:"query"`
	// Bind gives goal variables values from the host, as datalog.Bind does: {"n": "d"}.
	Bind map[string]string `yaml:"bind" json:"bind,omitempty"`
	// Cites shows each row's citations next to it.
	Cites bool `yaml:"cites" json:"cites,omitempty"`
	// FoldSourceUnits has the facts' units folded as they are read, as a host opts in with
	// ns.Vocabulary.FoldSourceUnits (jaala#303): 1000mV in the facts is then 1V.
	FoldSourceUnits bool `yaml:"fold_source_units" json:"foldSourceUnits,omitempty"`
	// Expect pins the answer's rows, in order, as the table shows them.
	Expect [][]string `yaml:"expect" json:"expect,omitempty"`
	// ExpectError pins the example to fail with an error containing this text.
	ExpectError string `yaml:"expect_error" json:"expectError,omitempty"`
}

// Table is an example's answer: the columns as the query names them, and each row's values and
// citations in the order jaala answers.
type Table struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Cites   [][]string `json:"cites"`
}

// Budget bounds an example's work, so a runaway edit fails instead of hanging the browser tab or
// using up its memory. It's also what bounds memory: a goal that aggregates holds every binding until
// it reduces them, about 3.6 KB per unit of work on a cross product, so 2M units came to 7 GB and ran
// a wasm build out of memory. The docs' own examples need at most a few hundred units.
const Budget = 50_000

//go:embed fixtures/*.facts
var fixtures embed.FS

// Fixture returns a named fixture's text, as a page shows it.
func Fixture(name string) (string, error) {
	b, err := fixtures.ReadFile("fixtures/" + name + ".facts")
	if err != nil {
		return "", fmt.Errorf("demo: no fixture %q", name)
	}
	return string(b), nil
}

// Run evaluates an example with the planned SemiNaive evaluator, the one hosts use. A query error is
// returned as is, with its "query:" prefix, since showing it is often the example's point.
func Run(s Spec) (Table, error) {
	text := s.Facts
	if s.Fixture != "" {
		f, err := Fixture(s.Fixture)
		if err != nil {
			return Table{}, err
		}
		text = f + "\n" + text
	}
	facts, err := ParseFacts(text)
	if err != nil {
		return Table{}, err
	}
	src := ns.NewMemSource()
	arity := map[string]int{}
	named := labels(text)
	for _, f := range facts {
		if n, ok := arity[f.Relation]; !ok {
			cols := named[f.Relation]
			if len(cols) != len(f.Args) {
				cols = make([]string, len(f.Args))
				for i := range cols {
					cols[i] = fmt.Sprintf("arg%d", i+1)
				}
			}
			src.Declare(f.Relation, cols...)
			arity[f.Relation] = len(f.Args)
		} else if n != len(f.Args) {
			return Table{}, fmt.Errorf("demo: %s has %d arguments here and %d before", f.Text, len(f.Args), n)
		}
		src.Add(f.Relation, ns.Tuple{Vals: f.Args, Cites: []string{f.Text}})
	}
	v, err := ns.NewVocabulary(src)
	if err != nil {
		return Table{}, err
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		return Table{}, err
	}
	if err := stdlib.Register(v); err != nil {
		return Table{}, err
	}
	if s.FoldSourceUnits {
		v.FoldSourceUnits()
	}
	base, err := datalog.NewBase(v, src)
	if err != nil {
		return Table{}, err
	}
	q, err := datalog.Parse(s.Program + "\n" + s.Query)
	if err != nil {
		return Table{}, err
	}
	opts := []datalog.Option{datalog.Budget(Budget)}
	if len(s.Bind) > 0 {
		bind := map[datalog.Var][]ns.Value{}
		for k, val := range s.Bind {
			bind[datalog.Var(strings.TrimPrefix(k, "?"))] = []ns.Value{ns.S(val)}
		}
		opts = append(opts, datalog.Bind(bind))
	}
	rows, err := datalog.SemiNaive{}.Eval(context.Background(), q, base, opts...)
	if err != nil {
		return Table{}, err
	}
	t := Table{Rows: [][]string{}, Cites: [][]string{}}
	cols := q.Columns()
	for _, c := range cols {
		t.Columns = append(t.Columns, columnName(c))
	}
	for _, r := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = display(r.Bind[c], r.Why[c])
		}
		t.Rows = append(t.Rows, vals)
		t.Cites = append(t.Cites, append([]string{}, r.Cites...))
	}
	return t, nil
}

// Check reports how an example's outcome differs from what its spec pins, or nil. An example that
// pins nothing must still run without error.
func Check(s Spec, t Table, runErr error) error {
	switch {
	case s.ExpectError != "" && runErr == nil:
		return fmt.Errorf("expected an error containing %q, and it answered %d rows", s.ExpectError, len(t.Rows))
	case s.ExpectError != "" && !strings.Contains(runErr.Error(), s.ExpectError):
		return fmt.Errorf("expected an error containing %q, got: %v", s.ExpectError, runErr)
	case s.ExpectError != "":
		return nil
	case runErr != nil:
		return runErr
	case s.Expect != nil && fmt.Sprint(s.Expect) != fmt.Sprint(t.Rows):
		return fmt.Errorf("expected rows %v, got %v", s.Expect, t.Rows)
	}
	return nil
}

// columnName is a column as the query wrote it: ?x, or count(?n) for an aggregate.
func columnName(c datalog.Var) string {
	s := string(c)
	if i := strings.Index(s, "("); i >= 0 {
		inner := s[i+1 : len(s)-1]
		if rest, ok := strings.CutPrefix(inner, "distinct "); ok {
			return s[:i] + "(distinct ?" + rest + ")"
		}
		return s[:i] + "(?" + inner + ")"
	}
	return "?" + s
}

// display writes a cell: a number with its unit, absent with the row's reason for it, if any.
func display(v ns.Value, why string) string {
	if v.Absent && why != "" {
		return "absent (" + why + ")"
	}
	if v.Absent {
		return "absent"
	}
	return v.S + v.BaseUnit
}
