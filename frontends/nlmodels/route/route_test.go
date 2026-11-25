package route_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/panyam/chakra/connection"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/route"
)

// profile is a model's profile on F3 and F4, priced at a dollar per million tokens in when priced,
// with dollars spent per tag.
func profile(model string, priced bool, f3, f4 nl.Tally, f3usd, f4usd float64) nl.ModelProfile {
	s := nl.Summary{Tags: map[string]nl.Tally{"F3": f3, "F4": f4},
		TagCost: map[string]nl.Cost{"F3": {Dollars: f3usd, TokensIn: int(f3usd * 1e6)}, "F4": {Dollars: f4usd, TokensIn: int(f4usd * 1e6)}}}
	s.Pass, s.Wrong, s.Abstain = f3.Pass+f4.Pass, f3.Wrong+f4.Wrong, f3.Abstain+f4.Abstain
	s.Cost = nl.Cost{Dollars: f3usd + f4usd, TokensIn: int((f3usd + f4usd) * 1e6)}
	p := nl.ModelProfile{Model: model, Summary: s}
	if priced {
		p.Price, p.Priced = nl.Price{In: 1}, true
	} else {
		// Cost.Dollars leaves out unpriced tokens, so an unpriced model's tags cost $0
		for tag, c := range s.TagCost {
			c.Dollars, c.Unpriced = 0, []string{model}
			s.TagCost[tag] = c
		}
		p.Summary.Cost.Dollars, p.Summary.Cost.Unpriced = 0, []string{model}
	}
	return p
}

func order(t *testing.T, ps []nl.ModelProfile, tags []string, lambda float64) []string {
	t.Helper()
	got, err := route.Router(ps, "translate", 3).Route(context.Background(), connection.Request{Tags: tags, Lambda: lambda})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An unpriced model goes after a priced one when cost counts, though both pass as often and its
// tags record $0 (#579). control: with lambda 0 cost doesn't count, and the two order by name.
func TestAnUnpricedModelGoesAfterAPricedOne(t *testing.T) {
	ps := []nl.ModelProfile{
		profile("frontier", false, nl.Tally{Pass: 15, Wrong: 4}, nl.Tally{Pass: 17}, 2, 2),
		profile("x14b", true, nl.Tally{Pass: 15, Wrong: 4}, nl.Tally{Pass: 17}, 0, 0),
	}
	if got := order(t, ps, []string{"F3"}, 1); !reflect.DeepEqual(got, []string{"x14b", "frontier"}) {
		t.Errorf("lambda 1: %v", got)
	}
	if got := order(t, ps, []string{"F3"}, 0); !reflect.DeepEqual(got, []string{"frontier", "x14b"}) {
		t.Errorf("control, lambda 0: %v", got)
	}
}

// The router orders by pass share on the question's tags less lambda times dollars per case, as
// #574's RouteFor did, and a tag with too few cases reads the model's whole run.
func TestTheRouterOrdersByTheQuestionsTags(t *testing.T) {
	ps := []nl.ModelProfile{
		profile("x14b", true, nl.Tally{Wrong: 19}, nl.Tally{Pass: 17}, 0, 0),
		profile("sonnet", true, nl.Tally{Pass: 15, Wrong: 4}, nl.Tally{Pass: 17}, 1.9, 1.7),
	}
	// on the one rare case x14b passed and sonnet didn't, the opposite of their whole runs
	rare := []nl.ModelProfile{ps[0], ps[1]}
	rare[0].Summary.Tags = map[string]nl.Tally{"F3": {Wrong: 19}, "F4": {Pass: 17}, "rare": {Pass: 1}}
	rare[1].Summary.Tags = map[string]nl.Tally{"F3": {Pass: 15, Wrong: 4}, "F4": {Pass: 17}, "rare": {Wrong: 1}}
	for _, c := range []struct {
		name   string
		ps     []nl.ModelProfile
		tags   []string
		lambda float64
		want   []string
	}{
		{"F3 by passes", ps, []string{"F3"}, 0, []string{"sonnet", "x14b"}},
		{"F3 when dollars weigh", ps, []string{"F3"}, 10, []string{"x14b", "sonnet"}},
		{"F4, equals by name", ps, []string{"F4"}, 0, []string{"sonnet", "x14b"}},
		// under 3 cases each reads its whole run: sonnet 32 of 36, x14b 17 of 36
		{"too few cases back off", rare, []string{"rare"}, 0, []string{"sonnet", "x14b"}},
	} {
		if got := order(t, c.ps, c.tags, c.lambda); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Rows writes a whole-run row and one per tag, with Dollars only for a priced profile whose cost
// names no unpriced model.
func TestRowsLeaveAnUnpricedCostUnknown(t *testing.T) {
	rows := route.Rows([]nl.ModelProfile{
		profile("frontier", false, nl.Tally{Pass: 1}, nl.Tally{Wrong: 1}, 2, 2),
		profile("x14b", true, nl.Tally{Pass: 1}, nl.Tally{Wrong: 1}, 0.5, 0.25),
	}, "translate")
	var got []string
	for _, r := range rows {
		d := "unknown"
		if r.Dollars != nil {
			d = "known"
		}
		got = append(got, r.Connection+"/"+r.Tag+"/"+d)
		if r.Arm != "translate" {
			t.Errorf("row %+v has arm %q", r, r.Arm)
		}
	}
	want := []string{"frontier//unknown", "frontier/F3/unknown", "frontier/F4/unknown", "x14b//known", "x14b/F3/known", "x14b/F4/known"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
	if f3 := rows[4]; *f3.Dollars != 0.5 || f3.Pass != 1 || f3.TokensIn != 500_000 {
		t.Errorf("x14b's F3 row %+v", f3)
	}
}

// A priced model whose run also spent an unpriced model's tokens (a fallback with no Price) leaves
// its rows' Dollars unknown, since Cost.Dollars left those tokens out, and its connection carries
// its Price for chakra to price the tokens. control: an unpriced profile's connection has no Price.
func TestAPricedRunWithAnUnpricedModelPricesItsTokens(t *testing.T) {
	mixed := profile("mixed", true, nl.Tally{Pass: 1}, nl.Tally{Wrong: 1}, 0.5, 0)
	for tag, c := range mixed.Summary.TagCost {
		c.Unpriced = []string{"fallback"}
		mixed.Summary.TagCost[tag] = c
	}
	mixed.Summary.Cost.Unpriced = []string{"fallback"}
	r := route.Router([]nl.ModelProfile{mixed, profile("frontier", false, nl.Tally{Pass: 1}, nl.Tally{}, 1, 0)}, "translate", 0)
	for _, row := range r.Rows {
		if row.Connection == "mixed" && row.Dollars != nil {
			t.Errorf("mixed's %q row has Dollars %v", row.Tag, *row.Dollars)
		}
	}
	if p := r.Connections["mixed"].Price; p == nil || p.Input != 1 || p.Output != 0 || p.CachedInput == nil || *p.CachedInput != 0 {
		t.Errorf("mixed's price %+v", p)
	}
	if p := r.Connections["frontier"].Price; p != nil {
		t.Errorf("control: frontier's price %+v", p)
	}
}
