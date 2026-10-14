package datalog

import (
	"math"
	"reflect"
	"testing"

	"github.com/panyam/jaala/ns"
)

func issue216Evaluators() []struct {
	name string
	ev   Evaluator
} {
	return []struct {
		name string
		ev   Evaluator
	}{
		{"naive", Naive{}},
		{"written", SemiNaive{WrittenOrder: true}},
		{"planned", SemiNaive{}},
	}
}

func issue216Voltages(annotated bool) *ns.MemSource {
	src := ns.NewMemSource().DeclareSchema("net.max_voltage", ns.Schema{
		Arity:  2,
		Labels: []string{"net", "voltage"},
		Types:  []ns.ArgType{{}, {Type: ns.TypeNumber, Unit: "V"}},
	})
	for _, row := range []struct {
		name string
		v    float64
	}{
		{"PMIC_CORE_3V3", 3.3},
		{"PMIC_IO_1V8", 1.8},
		{"PMIC_MAIN_12V0", 12},
	} {
		value := ns.NU(row.v, "V")
		if annotated && row.name == "PMIC_CORE_3V3" {
			value.S = "3.3000 V"
		}
		src.Add("net.max_voltage", ns.Tuple{Vals: []ns.Value{ns.S(row.name), value}})
	}
	return src
}

func issue216Eval(t *testing.T, ev Evaluator, src ns.Source, text string, opts ...Option) []Row {
	t.Helper()
	var report Report
	opts = append(opts, Explain(&report), Budget(100_000))
	rows, err := ev.Eval(bg, mustParse(t, text), baseFor(std(src)), opts...)
	if err != nil {
		t.Fatalf("Eval(%q): %v", text, err)
	}
	if report.Goal != nil {
		t.Logf("actual goal: %s", report.Goal.Ran)
		for _, literal := range report.Goal.Literals {
			t.Logf("actual literal: %s; access=%v reached=%d passed=%d", literal.Literal, literal.Access, literal.Reached, literal.Passed)
		}
	}
	return rows
}

func TestIssue216ComputedDisplay(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			rows := issue216Eval(t, evaluator.ev, issue216Voltages(false), `net.max_voltage(?n, ?v), ?h = 5V - ?v => ?n, ?h`)
			want := []string{"PMIC_CORE_3V3 1.7[V]", "PMIC_IO_1V8 3.2[V]", "PMIC_MAIN_12V0 -7[V]"}
			if got := withUnits(rows, "n", "h"); !reflect.DeepEqual(got, want) {
				t.Errorf("computed display = %v, want %v", got, want)
			}
		})
	}
}

func TestIssue216ComputedEquality(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			rows := issue216Eval(t, evaluator.ev, issue216Voltages(false), `net.max_voltage(?n, ?v), 5V - ?v = 1.7V => ?n`)
			if got := col(rows, "n"); got != "PMIC_CORE_3V3" {
				t.Errorf("equality filter = %q, want PMIC_CORE_3V3", got)
			}
		})
	}
}

func TestIssue216UncomputedTextIsPreserved(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			rows := issue216Eval(t, evaluator.ev, issue216Voltages(true), `net.max_voltage("PMIC_CORE_3V3", ?v) => ?v`)
			if len(rows) != 1 {
				t.Fatalf("uncomputed rows = %d, want 1", len(rows))
			}
			value := rows[0].Bind["v"]
			if value.S != "3.3000 V" || value.BaseUnit != "V" || value.Num == nil || *value.Num != 3.3 {
				t.Errorf("uncomputed value = text %q, unit %q, numeric %v; want unchanged 3.3000 V", value.S, value.BaseUnit, value.Num != nil)
			}
		})
	}
}

func TestIssue216UncomputedCanonicalNumberIsPreserved(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			src := ns.NewMemSource().DeclareSchema("supply", ns.Schema{
				Arity:  1,
				Labels: []string{"voltage"},
				Types:  []ns.ArgType{{Type: ns.TypeNumber, Unit: "V"}},
			})
			src.Add("supply", ns.Tuple{Vals: []ns.Value{ns.NU(1.7000000000000002, "V")}})
			rows := issue216Eval(t, evaluator.ev, src, `supply(?v) => ?v`)
			if len(rows) != 1 || rows[0].Bind["v"].Num == nil {
				t.Fatalf("uncomputed rows = %d, want one numeric voltage", len(rows))
			}
			value := rows[0].Bind["v"]
			if value.S != "1.7000000000000002" || *value.Num != 1.7000000000000002 || value.BaseUnit != "V" {
				t.Errorf("uncomputed canonical value = text %q, number %.17g, unit %q; want unchanged 1.7000000000000002 V", value.S, *value.Num, value.BaseUnit)
			}
		})
	}
}

func TestIssue216ArithmeticInverseControls(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			for _, c := range []struct {
				name  string
				query string
				want  float64
			}{
				{"large_integer_add", `num.add(1, ?b, 1000000000000001)`, 1000000000000000},
				{"one_third_mul", `num.mul(3, ?b, 1)`, 1.0 / 3},
			} {
				t.Run(c.name, func(t *testing.T) {
					rows := issue216Eval(t, evaluator.ev, ns.NewMemSource(), c.query)
					if len(rows) != 1 || rows[0].Bind["b"].Num == nil {
						t.Fatalf("inverse rows = %d, want one numeric b", len(rows))
					}
					b := rows[0].Bind["b"]
					t.Logf("inverse b: text=%q number=%.17g unit=%q", b.S, *b.Num, b.BaseUnit)
					if *b.Num != c.want || b.BaseUnit != "" {
						t.Errorf("inverse b = %.17g [%s], want %.17g without unit", *b.Num, b.BaseUnit, c.want)
					}
					fullyBound := issue216Eval(t, evaluator.ev, ns.NewMemSource(), c.query, Bind(map[Var][]ns.Value{"b": {b}}))
					if len(fullyBound) != 1 {
						t.Errorf("fully bound inverse result answers %d rows, want 1", len(fullyBound))
					}
				})
			}
		})
	}
}

func TestIssue216NonFiniteInverseRows(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			for _, c := range []struct {
				name  string
				query string
				a, c  float64
			}{
				{"infinite_subtraction", `num.sub(?a, ?b, ?c) => ?b`, math.Inf(1), math.Inf(1)},
				{"nan_addition", `num.add(?a, ?b, ?c) => ?b`, math.NaN(), math.NaN()},
			} {
				t.Run(c.name, func(t *testing.T) {
					rows := issue216Eval(t, evaluator.ev, ns.NewMemSource(), c.query,
						Bind(map[Var][]ns.Value{"a": {ns.N(c.a)}, "c": {ns.N(c.c)}}))
					if len(rows) != 1 || rows[0].Bind["b"].Num == nil || !math.IsNaN(*rows[0].Bind["b"].Num) {
						t.Fatalf("non-finite inverse rows = %v, want one numeric NaN", rows)
					}
				})
			}
		})
	}
}

func TestIssue216InverseZeroFactorKeepsExistingRows(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			rows := issue216Eval(t, evaluator.ev, ns.NewMemSource(), `num.div(?a, 0, 1) => ?a`)
			if len(rows) != 1 || rows[0].Bind["a"].Num == nil || *rows[0].Bind["a"].Num != 0 {
				t.Fatalf("inverse zero-factor rows = %v, want one numeric zero", rows)
			}
		})
	}
}

func TestIssue216CanonicalRelationModes(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			for _, c := range []struct {
				name, query, variable string
				want                  float64
				rows                  int
			}{
				{"to_b", `num.sub(5V, ?b, 1.7V) => ?b`, "b", 3.3, 1},
				{"to_a", `num.sub(?a, 3.3V, 1.7V) => ?a`, "a", 5, 1},
				{"all_bound", `num.sub(5V, 3.3V, 1.7V)`, "", 0, 1},
				{"old_result_to_b", `num.sub(5V, ?b, 1.7000000000000002V) => ?b`, "", 0, 0},
				{"old_result_to_a", `num.sub(?a, 3.3V, 1.7000000000000002V) => ?a`, "", 0, 0},
				{"old_result_all_bound", `num.sub(5V, 3.3V, 1.7000000000000002V)`, "", 0, 0},
				{"scaled_to_b", `num.sub(5000mV, ?b, 1.7V) => ?b`, "b", 3.3, 1},
				{"scaled_exact_to_a", `num.sub(?a, 5000mV, 1.7V) => ?a`, "a", 6.7, 1},
				// 3300mV normalizes to 3.3000000000000003V, a different float from 3.3V.
				// Its forward result cannot be simplified while preserving both operands.
				{"scaled_to_a", `num.sub(?a, 3300mV, 1.7V) => ?a`, "", 0, 0},
				{"scaled_all_bound", `num.sub(5V, 3300mV, 1.7V)`, "", 0, 0},
				{"scaled_forward", `num.sub(5V, 3300mV, ?c) => ?c`, "c", 1.6999999999999997, 1},
				{"scaled_inverse_own_result", `num.sub(?a, 3300mV, 1.6999999999999997V) => ?a`, "a", 5, 1},
				{"scaled_to_b_own_result", `num.sub(5V, ?b, 1.6999999999999997V) => ?b`, "b", 3.3000000000000003, 1},
				{"scaled_all_bound_own_result", `num.sub(5V, 3300mV, 1.6999999999999997V)`, "", 0, 1},
			} {
				t.Run(c.name, func(t *testing.T) {
					rows := issue216Eval(t, evaluator.ev, ns.NewMemSource(), c.query)
					if len(rows) != c.rows {
						t.Fatalf("relation rows = %d, want %d", len(rows), c.rows)
					}
					if c.variable != "" {
						value := rows[0].Bind[Var(c.variable)]
						if value.Num == nil || *value.Num != c.want || value.BaseUnit != "V" {
							t.Errorf("inverse value = %v, want %.17g V", value, c.want)
						}
					}
				})
			}
		})
	}
}

func TestIssue216ArithmeticBoundaries(t *testing.T) {
	for _, evaluator := range issue216Evaluators() {
		t.Run(evaluator.name, func(t *testing.T) {
			for _, c := range []struct {
				name, query string
				bind        map[Var][]ns.Value
				rows        int
				want        float64
			}{
				{"largest_finite", `num.add(?a, 0, ?c) => ?c`, map[Var][]ns.Value{"a": {ns.N(math.MaxFloat64)}}, 1, math.MaxFloat64},
				{"overflow", `num.mul(?a, 2, ?c) => ?c`, map[Var][]ns.Value{"a": {ns.N(math.MaxFloat64)}}, 1, math.Inf(1)},
				{"negative_zero", `num.mul(?a, 1, ?c) => ?c`, map[Var][]ns.Value{"a": {ns.N(math.Copysign(0, -1))}}, 1, math.Copysign(0, -1)},
				{"nan_all_bound", `num.add(?a, ?b, ?c) => ?c`, map[Var][]ns.Value{"a": {ns.N(math.NaN())}, "b": {ns.N(math.NaN())}, "c": {ns.N(math.NaN())}}, 1, math.NaN()},
				{"division_by_zero", `num.div(1, 0, ?c) => ?c`, nil, 0, 0},
				{"inverse_zero_result", `num.div(1, ?c, 0) => ?c`, nil, 0, 0},
				{"inverse_zero_multiplier", `num.mul(0, ?c, 1) => ?c`, nil, 0, 0},
				{"incompatible_units", `num.add(1V, 2A, ?c) => ?c`, nil, 0, 0},
			} {
				t.Run(c.name, func(t *testing.T) {
					rows := issue216Eval(t, evaluator.ev, ns.NewMemSource(), c.query, Bind(c.bind))
					if len(rows) != c.rows {
						t.Fatalf("boundary rows = %d, want %d", len(rows), c.rows)
					}
					if c.rows == 1 {
						value := rows[0].Bind["c"]
						if value.Num == nil || !(*value.Num == c.want || math.IsNaN(*value.Num) && math.IsNaN(c.want)) ||
							math.Signbit(*value.Num) != math.Signbit(c.want) || value.BaseUnit != "" {
							t.Errorf("boundary value = %v, want %.17g without unit and with original sign", value, c.want)
						}
					}
				})
			}
		})
	}
}
