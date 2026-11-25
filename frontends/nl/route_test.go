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

// profiled is two models over bench/families' F3 and F4 as #574 tells it: 14b answers every F4 and
// no F3, for nothing; the frontier model answers most of both, for dollars.
func profiled() []nl.ModelProfile {
	prof := func(model string, f3, f4 nl.Tally, f3usd, f4usd float64) nl.ModelProfile {
		s := nl.Summary{Tags: map[string]nl.Tally{"F3": f3, "F4": f4, "rare": {Pass: 1}},
			TagCost: map[string]nl.Cost{"F3": {Dollars: f3usd}, "F4": {Dollars: f4usd}}}
		s.Pass, s.Wrong = f3.Pass+f4.Pass+1, f3.Wrong+f4.Wrong
		s.Cost.Dollars = f3usd + f4usd
		return nl.ModelProfile{Model: model, Summary: s}
	}
	return []nl.ModelProfile{
		prof("x14b", nl.Tally{Wrong: 19}, nl.Tally{Pass: 17}, 0, 0),
		prof("sonnet", nl.Tally{Pass: 15, Wrong: 4}, nl.Tally{Pass: 17}, 1.9, 1.7),
		{Model: "unrun"},
	}
}

func order(rs []nl.Route) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Model)
	}
	return out
}

// RouteFor orders the models by pass share less lambda's weight of cost on the question's tags,
// cheaper first among equals, backing off to the whole run on a tag with too few cases, and leaving
// out a model with none (#574).
func TestRouteForOrdersModelsByTheirTags(t *testing.T) {
	ps := profiled()
	for _, c := range []struct {
		name    string
		tags    []string
		lambda  float64
		want    []string
		backoff bool
	}{
		{"F3 by passes", []string{"F3"}, 0, []string{"sonnet", "x14b"}, false},
		{"F4 ties, cheaper first", []string{"F4"}, 0, []string{"x14b", "sonnet"}, false},
		{"F3 when dollars weigh", []string{"F3"}, 10, []string{"x14b", "sonnet"}, false},
		{"too few cases back off", []string{"rare"}, 0, []string{"sonnet", "x14b"}, true},
	} {
		got := nl.RouteFor(ps, c.tags, c.lambda, 3)
		if !reflect.DeepEqual(order(got), c.want) || got[0].Backoff != c.backoff {
			t.Errorf("%s: %+v, want %v", c.name, got, c.want)
		}
	}
	f3 := nl.RouteFor(ps, []string{"F3"}, 0, 3)
	if f3[0] != (nl.Route{Model: "sonnet", Pass: 15, Cases: 19, Dollars: 1.9, Score: 15.0 / 19}) {
		t.Errorf("sonnet on F3: %+v", f3[0])
	}
}
