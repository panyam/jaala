package nl

import (
	"slices"
	"strings"
)

// A Route is one model's place in a router's order for a question (#574): what its profiles say it
// passes and costs on the question's tags.
type Route struct {
	Model string
	// Pass and Cases count the profile's cases on the question's tags, and Dollars what they cost. A
	// case with two of the question's tags counts in each.
	Pass, Cases int
	Dollars     float64
	// Score is Pass over Cases less lambda times Dollars over Cases: the share it answers, less what
	// a case costs at the host's exchange rate.
	Score float64
	// Backoff says the question's tags held fewer than the router's minimum cases for this model, so
	// its whole run's numbers stood in.
	Backoff bool
}

// RouteFor orders the profiled models for a question with tags, best Score first, cheaper first among
// equals, then by name (#574). It is a lookup over Profile's per-tag tallies and costs, which a
// study of 21 routers found as good as a learned router (The Routing Plateau, 2026). It can't tell a
// hard question from an easy one inside a tag, so a Scorer (#572) still catches a wrong answer after
// the router picks. lambda is dollars' weight against passes: 0 orders by pass share alone. A model
// with fewer than minCases cases on the tags backs off to its whole run, and one with no cases at all
// is left out.
func RouteFor(ps []ModelProfile, tags []string, lambda float64, minCases int) []Route {
	var out []Route
	for _, p := range ps {
		r := Route{Model: p.Model}
		for _, tag := range tags {
			t := p.Summary.Tags[tag]
			r.Pass += t.Pass
			r.Cases += t.Cases()
			r.Dollars += p.Summary.TagCost[tag].Dollars
		}
		if r.Cases < minCases || r.Cases == 0 {
			r.Pass, r.Cases, r.Dollars, r.Backoff = p.Summary.Pass, p.Summary.Cases(), p.Summary.Cost.Dollars, true
		}
		if r.Cases == 0 {
			continue
		}
		r.Score = (float64(r.Pass) - lambda*r.Dollars) / float64(r.Cases)
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Route) int {
		switch {
		case a.Score != b.Score:
			if a.Score > b.Score {
				return -1
			}
			return 1
		case a.Dollars/float64(a.Cases) != b.Dollars/float64(b.Cases):
			if a.Dollars/float64(a.Cases) < b.Dollars/float64(b.Cases) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}
