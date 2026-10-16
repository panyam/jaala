package datalog

import (
	"reflect"
	"testing"

	"github.com/panyam/jaala/ns"
)

// probes is a board's readings with some served in millivolts (#205): TP1's 1000 mV is TP2's 1 V,
// TP3's 3300 mV is TP4's 3.3 V, and TP5's 5 mV carries text saying more than its number.
func probes() *ns.MemSource {
	src := ns.NewMemSource().DeclareSchema("reading", ns.Schema{Arity: 2, Labels: []string{"probe", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	five := 5.0
	for _, r := range []struct {
		probe string
		v     ns.Value
	}{
		{"TP1", ns.NU(1000, "mV")}, {"TP2", ns.NU(1, "V")}, {"TP3", ns.NU(3300, "mV")}, {"TP4", ns.NU(3.3, "V")},
		{"TP5", ns.Value{S: "5 mV", Num: &five, BaseUnit: "mV"}},
	} {
		src.Add("reading", ns.Tuple{Vals: []ns.Value{ns.S(r.probe), r.v}})
	}
	return src
}

func TestAPrefixedSourceValueEqualsItsBaseValue(t *testing.T) {
	for _, src := range []struct {
		name string
		src  func() ns.Source
	}{{"held", func() ns.Source { return probes() }}, {"looked up", func() ns.Source { return lookingAt(probes()) }}} {
		t.Run(src.name, func(t *testing.T) {
			for _, c := range []struct {
				query string
				cols  []string
				want  []string
			}{
				{`reading(?a, ?v), reading(?b, ?v), ?a < ?b => ?a, ?b`, []string{"a", "b"}, []string{"TP1 TP2", "TP3 TP4"}},
				{`reading(_, ?v) => ?v`, []string{"v"}, []string{"5 mV[V]", "1[V]", "3.3[V]"}},
				{`reading(?p, 1V) => ?p`, []string{"p"}, []string{"TP1", "TP2"}},
				{`reading(?p, 3300mV) => ?p`, []string{"p"}, []string{"TP3", "TP4"}},
				{`reading(?p, ?v) => ?v, count(?p)`, []string{"v", "count(p)"}, []string{"5 mV[V] 1", "1[V] 2", "3.3[V] 2"}},
				{`reading(?p, ?v), ?v > 0.5V => ?p`, []string{"p"}, []string{"TP1", "TP2", "TP3", "TP4"}},
				{`reading(?p, ?v) => max(?v)`, []string{"max(v)"}, []string{"3.3[V]"}},
			} {
				got := withUnits(evalFolding(t, src.src(), c.query), c.cols...)
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("%s = %q, want %q", c.query, got, c.want)
				}
			}
		})
	}
}
