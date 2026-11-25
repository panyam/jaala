// Package route orders the models nl.Profile read from run records for a question, best first, on
// chakra's connection.TableRouter (#579). jaala keeps no routing logic of its own: this package only
// turns profiles into chakra's rows and connections.
package route

import (
	"sort"

	"github.com/panyam/chakra/connection"
	"github.com/panyam/jaala/frontends/nl"
)

// Rows are each profile's rows for arm: one over its whole run (Tag ""), and one per tag of
// Summary.Tags, with its tokens from Summary.TagCost. A row carries Dollars only when the profile
// had a Price and its cost names no unpriced model. Otherwise its cost is left to the connection's
// Price, or unknown, rather than zero: Cost.Dollars leaves out a model's unpriced tokens, so a
// frontier model with no Price counted as free and won at any lambda (#579).
func Rows(ps []nl.ModelProfile, arm string) []connection.Row {
	var out []connection.Row
	for _, p := range ps {
		s := p.Summary
		out = append(out, row(p, arm, "", s.Tally, s.Cost))
		tags := make([]string, 0, len(s.Tags))
		for tag := range s.Tags {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			out = append(out, row(p, arm, tag, s.Tags[tag], s.TagCost[tag]))
		}
	}
	return out
}

func row(p nl.ModelProfile, arm, tag string, t nl.Tally, c nl.Cost) connection.Row {
	r := connection.Row{Connection: p.Model, Arm: arm, Tag: tag, Pass: t.Pass, Wrong: t.Wrong, Abstain: t.Abstain,
		TokensIn: c.TokensIn, TokensCached: c.TokensCached, TokensOut: c.TokensOut, Seconds: c.Seconds}
	if p.Priced && len(c.Unpriced) == 0 {
		d := c.Dollars
		r.Dollars = &d
	}
	return r
}

// Router is a TableRouter over ps's rows for arm, with a connection per profile priced by its
// Price, or none for a profile without one. minCases is how many cases a tag needs before the
// router trusts its row over the model's whole run; 0 is chakra's default. A caller routes a
// question with Route(ctx, connection.Request{Tags: tags, Lambda: lambda}), which returns the
// models best first. With Lambda above zero, a model whose cost is unknown goes after every model
// whose cost is known.
func Router(ps []nl.ModelProfile, arm string, minCases int) connection.TableRouter {
	conns := make(map[string]connection.Config, len(ps))
	for _, p := range ps {
		var c connection.Config
		if p.Priced {
			cached := p.Price.Cached
			c.Price = &connection.Price{Input: p.Price.In, CachedInput: &cached, Output: p.Price.Out}
		}
		conns[p.Model] = c
	}
	return connection.TableRouter{Rows: Rows(ps, arm), Arm: arm, Connections: conns, MinCases: minCases}
}
