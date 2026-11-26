package nl_test

import (
	"reflect"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
)

// costed is a report of three tagged cases, each one call of "big" at a dollar per million tokens in.
func costed() (nl.Report, nl.Prices) {
	call := func(tokens int) nl.Trace {
		return nl.Trace{Tries: []nl.Try{{Model: "big", TokensIn: tokens, Calls: 1}}}
	}
	return nl.Report{Cases: []nl.Result{
		{Case: nl.Case{Name: "a", Tags: []string{"F3", "join"}}, Pass: true, Trace: call(2_000_000)},
		{Case: nl.Case{Name: "b", Tags: []string{"F3"}}, Trace: call(1_000_000)},
		{Case: nl.Case{Name: "c"}, Pass: true, Trace: call(4_000_000)},
	}}, nl.Prices{"big": {In: 1}}
}

// Summary.TagCost costs each tag as Tags counts it, a case with two tags in each, and TagCosts reads
// the same from a record's cases (#574). control: the untagged case costs in no tag.
func TestTagCostCountsAsTagsDo(t *testing.T) {
	rep, prices := costed()
	s := rep.Summary(prices)
	if s.TagCost["F3"].Dollars != 3 || s.TagCost["join"].Dollars != 2 || len(s.TagCost) != 2 || s.Cost.Dollars != 7 {
		t.Errorf("tag cost %+v, cost %v", s.TagCost, s.Cost.Dollars)
	}
	rec := nl.NewRecord(rep, prices)
	if !reflect.DeepEqual(nl.TagCosts(rec.Cases), rec.Summary.TagCost) {
		t.Errorf("TagCosts %+v, Summary.TagCost %+v", nl.TagCosts(rec.Cases), rec.Summary.TagCost)
	}
	twice := nl.Profile([]nl.Record{rec, rec}, prices)
	if len(twice) != 1 || twice[0].Summary.TagCost["F3"].Dollars != 6 {
		t.Errorf("a profile of two records: %+v", twice)
	}
}

// A host that knows how much of its output a model spent thinking records it in TokensReasoning,
// and costs summed over cases keep it.
func TestCostsSumReasoningTokens(t *testing.T) {
	cases := []nl.CaseRecord{
		{Name: "a", Tags: []string{"agg"}, Cost: nl.Cost{TokensOut: 900, TokensReasoning: 840}},
		{Name: "b", Tags: []string{"agg"}, Cost: nl.Cost{TokensOut: 300, TokensReasoning: 120}},
	}
	if got := nl.TagCosts(cases)["agg"].TokensReasoning; got != 960 {
		t.Errorf("TokensReasoning %d, want 960", got)
	}
}
