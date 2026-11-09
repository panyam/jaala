package nl

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/panyam/jaala/datalog"
)

// wouldPass runs the draft an abstention offered over b and says whether it answers the case's rows.
// A draft that already ran (a low-agreement answer) is scored on its rows. Otherwise only a draft in
// jaala's language is parsed and run: a front end's draft carries no compiled program. nil when the
// case didn't abstain, there is no draft, or the draft doesn't parse or run.
func wouldPass(ctx context.Context, r Result, b *datalog.Base) *bool {
	var ab *Abstained
	if !errors.As(r.Err, &ab) || ab.Draft.Text == "" {
		return nil
	}
	rows := ab.Draft.Rows
	if rows == nil {
		if ab.Draft.Language != "" {
			return nil
		}
		q, err := datalog.Parse(ab.Draft.Text)
		if err != nil {
			return nil
		}
		if rows, err = (datalog.SemiNaive{}).Eval(ctx, q, b); err != nil {
			return nil
		}
	}
	pass := sameRows(rowsText(rows), r.Want, r.Ordered)
	return &pass
}

// Relations are the relations the accepted program reads, sorted: every atom of its goal and rules,
// negated, optional or inside an aggregate's braces too, less the relations its own rules define.
func (r Result) Relations() []string { return relationsRead(r.Program.Query) }

func relationsRead(q datalog.Query) []string {
	own := map[string]bool{}
	for _, rl := range q.Rules {
		own[rl.Head.Relation] = true
	}
	seen := map[string]bool{}
	var walk func(datalog.Body)
	walk = func(b datalog.Body) {
		for _, l := range b.Literals {
			for _, a := range []*datalog.Atom{l.Pos, l.Neg} {
				if a != nil && !own[a.Relation] {
					seen[a.Relation] = true
				}
			}
			if l.Opt != nil {
				walk(*l.Opt)
			}
			if l.Agg != nil {
				walk(l.Agg.Body)
			}
		}
	}
	walk(q.Goal)
	for _, rl := range q.Rules {
		walk(rl.Body)
	}
	var out []string
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// A Rate is how many of Of cases showed a skill.
type Rate struct {
	Hit, Of int
}

// Share is Hit over Of; ok is false when no case could show the skill.
func (r Rate) Share() (share float64, ok bool) {
	if r.Of == 0 {
		return 0, false
	}
	return float64(r.Hit) / float64(r.Of), true
}

func (r *Rate) count(hit bool) {
	r.Of++
	if hit {
		r.Hit++
	}
}

// A ModelProfile is how one model did across a set of run records (#537): its outcomes per tag, what
// they cost, and five skills read from the cases' tries.
type ModelProfile struct {
	// Model is the records' Model: a profile is per arm's model, so a fallback's tries count toward
	// the model whose run asked it.
	Model string
	// Price is the model's from the Prices given, and Priced says it had one.
	Price  Price
	Priced bool
	// Summary adds up the records' Summaries: outcomes, outcomes per tag, and cost.
	Summary Summary
	// Syntax: the first try parsed, compiled and checked. A case whose first try was a model's
	// failure is left out.
	Syntax Rate
	// Grounding: the first try that ran answered something, rather than nothing from a guessed value,
	// kind or id (ClassGrounding).
	Grounding Rate
	// Schema: the accepted program reads every relation of the case's Reads. Cases without Reads,
	// or without an accepted program, are left out.
	Schema Rate
	// Repair: a refused try was followed by an accepted one the model wrote (not the harness's
	// repair). A model's failure isn't a refusal it could repair.
	Repair Rate
	// Calibration: an abstention whose draft Eval ran would have been wrong (CaseRecord.WouldPass).
	Calibration Rate
}

// Profile reads recs into a profile per Model, sorted by name, each priced by prices.
func Profile(recs []Record, prices Prices) []ModelProfile {
	byModel := map[string]*ModelProfile{}
	for _, rec := range recs {
		p := byModel[rec.Model]
		if p == nil {
			p = &ModelProfile{Model: rec.Model}
			p.Price, p.Priced = prices[rec.Model]
			byModel[rec.Model] = p
		}
		p.Summary.merge(rec.Summary)
		for _, c := range rec.Cases {
			p.read(c)
		}
	}
	var out []ModelProfile
	for _, p := range byModel {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b ModelProfile) int { return strings.Compare(a.Model, b.Model) })
	return out
}

func (s *Summary) merge(o Summary) {
	s.Pass += o.Pass
	s.Wrong += o.Wrong
	s.Abstain += o.Abstain
	for tag, t := range o.Tags {
		if s.Tags == nil {
			s.Tags = map[string]Tally{}
		}
		m := s.Tags[tag]
		m.Pass += t.Pass
		m.Wrong += t.Wrong
		m.Abstain += t.Abstain
		s.Tags[tag] = m
	}
	s.Cost.add(o.Cost)
}

func (p *ModelProfile) read(c CaseRecord) {
	if len(c.Tries) > 0 && c.Tries[0].Class != ClassModel {
		p.Syntax.count(c.Tries[0].Class != ClassConstruct)
	}
	for _, t := range c.Tries {
		if t.Class == ClassConstruct || t.Class == ClassModel || t.Kind == KindRepeat {
			continue // didn't run
		}
		p.Grounding.count(t.Class != ClassGrounding)
		break
	}
	if len(c.Reads) > 0 && c.Program != "" {
		p.Schema.count(readsAll(c.Relations, c.Reads))
	}
	for i, t := range c.Tries[:max(len(c.Tries)-1, 0)] {
		if t.Err == "" || t.Kind == KindModel {
			continue
		}
		next := c.Tries[i+1]
		p.Repair.count(next.Err == "" && next.Repair == "")
	}
	if c.Outcome == OutcomeAbstain && c.WouldPass != nil {
		p.Calibration.count(!*c.WouldPass)
	}
}

// readsAll says every name of want is in have.
func readsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

var profileColumns = []string{"model", "usd_per_mtok_in", "usd_per_mtok_cached", "usd_per_mtok_out", "pass", "wrong", "abstain", "of",
	"usd_per_correct", "seconds_per_correct", "syntax", "grounding", "schema", "repair", "calibration", "tags"}

// ProfileTable writes a row per profile as "csv" or "md": its price per million tokens (empty when
// unpriced), its outcomes, its cost and time per correct answer, each skill as hit/of, and each tag's
// pass/wrong/abstain. Cost per correct answer is marked "+" when some model went unpriced, since it
// is then a lower bound.
func ProfileTable(w io.Writer, ps []ModelProfile, format string) error {
	var rows [][]string
	for _, p := range ps {
		in, cached, out := "", "", ""
		if p.Priced {
			in, cached, out = fmt.Sprint(p.Price.In), fmt.Sprint(p.Price.Cached), fmt.Sprint(p.Price.Out)
		}
		s := p.Summary
		perUSD, perSec := "", ""
		if d, sec, ok := s.PerCorrect(); ok {
			perUSD, perSec = fmt.Sprintf("%.4f", d), fmt.Sprintf("%.1f", sec)
			if len(s.Cost.Unpriced) > 0 {
				perUSD += "+"
			}
		}
		var tags []string
		for tag, t := range s.Tags {
			tags = append(tags, fmt.Sprintf("%s %d/%d/%d", tag, t.Pass, t.Wrong, t.Abstain))
		}
		sort.Strings(tags)
		rows = append(rows, []string{p.Model, in, cached, out, fmt.Sprint(s.Pass), fmt.Sprint(s.Wrong), fmt.Sprint(s.Abstain), fmt.Sprint(s.Cases()),
			perUSD, perSec, p.Syntax.String(), p.Grounding.String(), p.Schema.String(), p.Repair.String(), p.Calibration.String(), strings.Join(tags, "; ")})
	}
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		cw.Write(profileColumns)
		cw.WriteAll(rows)
		return cw.Error()
	case "md":
		fmt.Fprintf(w, "| %s |\n|%s\n", strings.Join(profileColumns, " | "), strings.Repeat(" --- |", len(profileColumns)))
		for _, row := range rows {
			fmt.Fprintf(w, "| %s |\n", strings.Join(row, " | "))
		}
		return nil
	}
	return fmt.Errorf("nl: no profile format %q (csv or md)", format)
}

// String is hit/of, or "" when no case could show the skill.
func (r Rate) String() string {
	if r.Of == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", r.Hit, r.Of)
}
