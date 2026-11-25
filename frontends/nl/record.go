package nl

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// A Price is what a model charges, in US dollars per million tokens: prompt tokens read fresh,
// prompt tokens read from its cache, and reply tokens.
type Price struct {
	In, Cached, Out float64
}

// Prices are each model's Price, keyed by the label a Try or Step carries (Try.Model). A local model
// that costs nothing is priced at zero; a model left out is unpriced, and Cost says so rather than
// counting it as free.
type Prices map[string]Price

// A Cost is what some of a run spent (#536): tokens, model calls, wall time and dollars.
type Cost struct {
	TokensIn, TokensCached, TokensOut int
	// Calls counts model calls: each Try's and Step's Calls, or one for an entry with tokens that
	// leaves Calls 0, from a loop that doesn't count them.
	Calls int
	// Seconds is wall time: Result.Took, the translator's run and the evaluation together.
	Seconds float64
	// Untimed counts the cases summed whose time is unknown (Result.Untimed), such as a replay of
	// transcripts that kept none (#581). Seconds counts only the others, so a Cost with Untimed set
	// has no wall time to report, and the history and profile tables leave its seconds blank. A host
	// building a CaseRecord itself sets it to 1 for such a case.
	Untimed int `json:",omitempty"`
	// Dollars prices the tokens by Prices. Unpriced lists the models that spent tokens with no Price,
	// whose tokens Dollars leaves out, so a Cost with Unpriced set is a lower bound.
	Dollars  float64
	Unpriced []string `json:",omitempty"`
}

// Cost is what the case spent, priced by p. TokensIn counts the prompt tokens read fresh and from
// the cache together, as Try.TokensIn does, and TokensCached the cached part, which p.Cached prices.
func (r Result) Cost(p Prices) Cost { return r.cost(p, nil) }

// TierCost is what each tier spent under Options.Tiers (#538), keyed by Try.Tier: its tokens, calls
// and dollars as Cost counts them, and as Seconds the latency of its tries, steps and votes, since a
// case's wall time isn't split by tier. A case run without tiers has none.
func (r Result) TierCost(p Prices) map[string]Cost {
	out := map[string]Cost{}
	for _, tier := range r.tiers() {
		out[tier] = r.cost(p, &tier)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tiers are the tiers named in r's trace, in order of first appearance.
func (r Result) tiers() []string {
	var ts []string
	see := func(t string) {
		if t != "" && !slices.Contains(ts, t) {
			ts = append(ts, t)
		}
	}
	for _, t := range r.Trace.Tries {
		see(t.Tier)
	}
	for _, s := range r.Trace.Steps {
		see(s.Tier)
	}
	for _, t := range r.Trace.Votes {
		see(t.Tier)
	}
	return ts
}

// cost is Cost over the entries of one tier when tier is set, with their latency as Seconds.
func (r Result) cost(p Prices, tier *string) Cost {
	c := Cost{Seconds: r.Took.Seconds()}
	if tier != nil {
		c.Seconds = 0
	}
	unpriced := map[string]bool{}
	charge := func(model, of string, in, cached, out, calls int, took time.Duration) {
		if tier != nil {
			if of != *tier {
				return
			}
			c.Seconds += took.Seconds()
		}
		c.TokensIn += in
		c.TokensCached += cached
		c.TokensOut += out
		if calls == 0 && in+out > 0 {
			calls = 1
		}
		c.Calls += calls
		if in+out == 0 {
			return
		}
		pr, ok := p[model]
		if !ok {
			unpriced[model] = true
			return
		}
		c.Dollars += (float64(in-cached)*pr.In + float64(cached)*pr.Cached + float64(out)*pr.Out) / 1e6
	}
	for _, t := range r.Trace.Tries {
		charge(t.Model, t.Tier, t.TokensIn, t.TokensCached, t.TokensOut, t.Calls, t.Latency)
	}
	for _, s := range r.Trace.Steps {
		charge(s.Model, s.Tier, s.TokensIn, s.TokensCached, s.TokensOut, s.Calls, s.Latency)
	}
	for _, t := range r.Trace.Votes {
		charge(t.Model, t.Tier, t.TokensIn, t.TokensCached, t.TokensOut, t.Calls, t.Latency)
	}
	for m := range unpriced {
		c.Unpriced = append(c.Unpriced, m)
	}
	sort.Strings(c.Unpriced)
	if r.Untimed {
		c.Seconds, c.Untimed = 0, 1
	}
	return c
}

func (c *Cost) add(o Cost) {
	c.TokensIn += o.TokensIn
	c.TokensCached += o.TokensCached
	c.TokensOut += o.TokensOut
	c.Calls += o.Calls
	c.Seconds += o.Seconds
	c.Untimed += o.Untimed
	c.Dollars += o.Dollars
	for _, m := range o.Unpriced {
		if !slices.Contains(c.Unpriced, m) {
			c.Unpriced = append(c.Unpriced, m)
		}
	}
	sort.Strings(c.Unpriced)
}

// A Tally counts cases by Result.Outcome.
type Tally struct {
	Pass, Wrong, Abstain int
}

// Cases is how many cases the Tally counts.
func (t Tally) Cases() int { return t.Pass + t.Wrong + t.Abstain }

// Add counts one case by its outcome (OutcomePass, OutcomeWrong or OutcomeAbstain), as Summary
// does, so a host whose arms aren't nl.Result counts by jaala's rules (#556). Any other outcome
// counts nowhere.
func (t *Tally) Add(outcome string) {
	switch outcome {
	case OutcomePass:
		t.Pass++
	case OutcomeWrong:
		t.Wrong++
	case OutcomeAbstain:
		t.Abstain++
	}
}

// A Summary is a run's outcomes and what they cost (#536): the whole run's Tally, one per tag, and
// the total Cost.
type Summary struct {
	Tally
	// Tags is a Tally per Case tag. A case with several tags counts in each, and one with none in
	// the run's Tally alone.
	Tags map[string]Tally `json:",omitempty"`
	// TagCost is the Cost per Case tag, counted as Tags is (#574): a case with several tags costs in
	// each. A router reads it beside Tags to weigh a model's passes against what they cost.
	TagCost map[string]Cost `json:",omitempty"`
	Cost    Cost
	// Tiers is the Cost per tier under Options.Tiers (Result.TierCost), and Answered how many cases
	// each tier answered (Trace.Tier), so a local-then-frontier run says what each tier spent and
	// what it bought.
	Tiers    map[string]Cost `json:",omitempty"`
	Answered map[string]int  `json:",omitempty"`
}

// PerCorrect is the cost and time per passing case: Cost's Dollars and Seconds over Pass. ok is false
// when nothing passed, which has no cost per correct answer. A Cost with Unpriced models gives a
// lower bound on dollars, and one with Untimed cases has no time per correct answer: its seconds
// leave those cases out (#581).
func (s Summary) PerCorrect() (dollars, seconds float64, ok bool) {
	if s.Pass == 0 {
		return 0, 0, false
	}
	return s.Cost.Dollars / float64(s.Pass), s.Cost.Seconds / float64(s.Pass), true
}

// Summary is the report's outcomes per tag and its cost, priced by p.
func (rep Report) Summary(p Prices) Summary {
	var s Summary
	for _, r := range rep.Cases {
		o := r.Outcome()
		s.Tally.Add(o)
		for _, tag := range r.Tags {
			if s.Tags == nil {
				s.Tags = map[string]Tally{}
			}
			t := s.Tags[tag]
			t.Add(o)
			s.Tags[tag] = t
		}
		c := r.Cost(p)
		for _, tag := range r.Tags {
			if s.TagCost == nil {
				s.TagCost = map[string]Cost{}
			}
			tc := s.TagCost[tag]
			tc.add(c)
			s.TagCost[tag] = tc
		}
		s.Cost.add(c)
		for tier, c := range r.TierCost(p) {
			if s.Tiers == nil {
				s.Tiers = map[string]Cost{}
			}
			tc := s.Tiers[tier]
			tc.add(c)
			s.Tiers[tier] = tc
		}
		if r.Trace.Tier != "" {
			if s.Answered == nil {
				s.Answered = map[string]int{}
			}
			s.Answered[r.Trace.Tier]++
		}
	}
	return s
}

// A Record is one run of a question set, written as JSON (#536): what ran, each case's outcome and
// tries, and the Summary. A host writes one per run with Write and reads a directory of them back
// with ReadRecords, so every host's history has one shape. The fields up to Settings are the host's
// to fill; NewRecord fills the rest.
type Record struct {
	When time.Time
	// Commit is the host's build revision, and Bench the question set's name.
	Commit, Bench string
	// Agent names the arm (a translator, an agent and its tools), Model the model it ran and Version
	// the model's version, where the host knows it.
	Agent, Model, Version string `json:",omitempty"`
	// Run says which of several runs of one setting this is, from 1; 0 when the host runs one.
	Run int `json:",omitempty"`
	// Settings are what shaped the arm, as the host gave them (flags, options).
	Settings map[string]string `json:",omitempty"`
	Cases    []CaseRecord
	Summary  Summary
}

// A CaseRecord is one case of a Record: what Result says, as text.
type CaseRecord struct {
	Name string
	Tags []string `json:",omitempty"`
	// Outcome is Result.Outcome, Abstained the abstention's reason, and Class Result.Class for a case
	// that didn't pass.
	Outcome   string
	Abstained string `json:",omitempty"`
	Class     string `json:",omitempty"`
	// Program is the text of the program the translator answered with, in its language.
	Program string `json:",omitempty"`
	// Tier is the tier that answered (Trace.Tier), and Tiers what each tier spent (Result.TierCost).
	Tier  string          `json:",omitempty"`
	Tiers map[string]Cost `json:",omitempty"`
	// Scores are the Scorer's verdicts per tier (Trace.Scores, #572).
	Scores   []RungScore `json:",omitempty"`
	Language string      `json:",omitempty"`
	Err      string      `json:",omitempty"`
	Got      [][]string  `json:",omitempty"`
	Cost     Cost
	Tries    []TryRecord  `json:",omitempty"`
	Steps    []StepRecord `json:",omitempty"`
	Votes    []TryRecord  `json:",omitempty"`
	// Reads is the Case's, and Relations the accepted program's (Result.Relations), which a model
	// profile's schema choice compares (#537). WouldPass is the Result's.
	Reads     []string `json:",omitempty"`
	Relations []string `json:",omitempty"`
	WouldPass *bool    `json:",omitempty"`
	// Offered is the Trace's, the values the Grounder offered per name, and Constants the accepted
	// program's (Result.Constants), which a model profile's grounding compares.
	Offered   []Candidate `json:",omitempty"`
	Constants []string    `json:",omitempty"`
}

// A TryRecord is a Try as text: Err's message, and Class.
type TryRecord struct {
	Model                             string
	Text                              string `json:",omitempty"`
	Language                          string `json:",omitempty"`
	Kind                              string `json:",omitempty"`
	Class                             string `json:",omitempty"`
	Err                               string `json:",omitempty"`
	Mode                              string `json:",omitempty"`
	Repair                            string `json:",omitempty"`
	Reasked                           bool   `json:",omitempty"`
	Tier                              string `json:",omitempty"`
	Truncated                         bool   `json:",omitempty"`
	TokensIn, TokensCached, TokensOut int
	Calls                             int     `json:",omitempty"`
	Seconds                           float64 `json:",omitempty"`
}

// A StepRecord is a Step, less what the action showed the model.
type StepRecord struct {
	Model, Action, Text               string
	Tier                              string `json:",omitempty"`
	Truncated                         bool   `json:",omitempty"`
	After                             int
	TokensIn, TokensCached, TokensOut int
	Calls                             int     `json:",omitempty"`
	Seconds                           float64 `json:",omitempty"`
}

// NewRecord is rep's cases and Summary as a Record, priced by p. The caller fills When, Commit, Bench
// and the arm's fields before Write.
func NewRecord(rep Report, p Prices) Record {
	rec := Record{Summary: rep.Summary(p)}
	for _, r := range rep.Cases {
		cr := CaseRecord{Name: r.Name, Tags: r.Tags, Outcome: r.Outcome(), Abstained: r.AbstainReason(), Class: r.Class(),
			Program: r.Program.Text, Language: r.Program.Language, Got: r.Got, Cost: r.Cost(p),
			Tier: r.Trace.Tier, Tiers: r.TierCost(p), Scores: r.Trace.Scores}
		if r.Err != nil {
			cr.Err = r.Err.Error()
		}
		for _, t := range r.Trace.Tries {
			cr.Tries = append(cr.Tries, tryRecord(t))
		}
		for _, t := range r.Trace.Votes {
			cr.Votes = append(cr.Votes, tryRecord(t))
		}
		for _, s := range r.Trace.Steps {
			cr.Steps = append(cr.Steps, StepRecord{Model: s.Model, Action: s.Action, Text: s.Text, Tier: s.Tier, Truncated: s.Truncated, After: s.After,
				TokensIn: s.TokensIn, TokensCached: s.TokensCached, TokensOut: s.TokensOut, Calls: s.Calls, Seconds: s.Latency.Seconds()})
		}
		cr.Reads, cr.WouldPass = r.Reads, r.WouldPass
		cr.Relations = r.Relations()
		cr.Offered, cr.Constants = r.Trace.Offered, r.Constants()
		rec.Cases = append(rec.Cases, cr)
	}
	return rec
}

func tryRecord(t Try) TryRecord {
	tr := TryRecord{Model: t.Model, Text: t.Text, Language: t.Language, Kind: t.Kind, Class: t.Class(), Mode: t.Mode,
		Repair: t.Repair, Reasked: t.Reasked, Tier: t.Tier, Truncated: t.Truncated, TokensIn: t.TokensIn, TokensCached: t.TokensCached, TokensOut: t.TokensOut,
		Calls: t.Calls, Seconds: t.Latency.Seconds()}
	if t.Err != nil {
		tr.Err = t.Err.Error()
	}
	return tr
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

// Write writes r to a new file in dir, made if missing, and returns its path. Each run gets a file of
// its own, named for when it ran, its bench and its arm, never appended to one, so runs on two
// branches or machines never conflict.
func (r Record) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	stem := r.When.UTC().Format("20060102T150405Z") + "-" + unsafeName.ReplaceAllString(r.Bench+"-"+r.Agent+"-"+r.Model, "_")
	f, err := os.CreateTemp(dir, stem+"-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

// ReadRecords reads every .json file in dir as a Record, the earliest first. A file that isn't a
// Record fails the read, naming the file.
func ReadRecords(dir string) ([]Record, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var recs []Record
	for _, f := range files {
		data, err := os.ReadFile(f) // #nosec G304 -- f is a Glob match inside the directory the caller named
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		recs = append(recs, r)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].When.Before(recs[j].When) })
	return recs, nil
}

// TagTallies is a Tally per tag over cases, as Summary.Tags counts them: a case with several tags
// counts in each, and one with none in no tag (#556). A history step has records, not Results, so it
// counts from these. nil when no case has a tag.
func TagTallies(cases []CaseRecord) map[string]Tally {
	var out map[string]Tally
	for _, c := range cases {
		for _, tag := range c.Tags {
			if out == nil {
				out = map[string]Tally{}
			}
			t := out[tag]
			t.Add(c.Outcome)
			out[tag] = t
		}
	}
	return out
}

// TagCosts is the Cost per tag over cases, as Summary.TagCost counts it, for records a host built
// without Results (#574). nil when no case has a tag.
func TagCosts(cases []CaseRecord) map[string]Cost {
	var out map[string]Cost
	for _, c := range cases {
		for _, tag := range c.Tags {
			if out == nil {
				out = map[string]Cost{}
			}
			tc := out[tag]
			tc.add(c.Cost)
			out[tag] = tc
		}
	}
	return out
}

var tagColumns = []string{"when", "commit", "bench", "agent", "model", "run", "tag", "pass", "wrong", "abstain", "of"}

// HistoryTagTable writes a row per record and tag, as "csv" or "md" like HistoryTable: the tag's
// outcomes in that run, counted from the record's cases (TagTallies), tags in name order (#556).
// A record whose cases have no tags writes no row.
func HistoryTagTable(w io.Writer, recs []Record, format string) error {
	var rows [][]string
	for _, r := range recs {
		tallies := TagTallies(r.Cases)
		tags := make([]string, 0, len(tallies))
		for tag := range tallies {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			t := tallies[tag]
			rows = append(rows, []string{r.When.UTC().Format(time.RFC3339), r.Commit, r.Bench, r.Agent, r.Model, fmt.Sprint(r.Run), tag,
				fmt.Sprint(t.Pass), fmt.Sprint(t.Wrong), fmt.Sprint(t.Abstain), fmt.Sprint(t.Cases())})
		}
	}
	return writeTable(w, tagColumns, rows, format)
}

// writeTable writes rows under columns as "csv" or "md" (a Markdown table).
func writeTable(w io.Writer, columns []string, rows [][]string, format string) error {
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		cw.Write(columns)
		cw.WriteAll(rows)
		return cw.Error()
	case "md":
		fmt.Fprintf(w, "| %s |\n|%s\n", strings.Join(columns, " | "), strings.Repeat(" --- |", len(columns)))
		for _, row := range rows {
			fmt.Fprintf(w, "| %s |\n", strings.Join(row, " | "))
		}
		return nil
	}
	return fmt.Errorf("nl: no history format %q (csv or md)", format)
}

var historyColumns = []string{"when", "commit", "bench", "agent", "model", "settings", "run", "pass", "wrong", "abstain", "of",
	"calls", "tokens_in", "tokens_cached", "tokens_out", "usd", "seconds", "usd_per_correct", "seconds_per_correct"}

// HistoryTable writes a row per record, as "csv" or "md" (a Markdown table): its outcomes, what it
// spent, and its cost and time per correct answer. Cost per correct answer is empty when nothing
// passed, and marked with a trailing "+" when a model was unpriced, since it is then a lower bound.
// Seconds and seconds per correct answer are empty when any case's time is unknown (Cost.Untimed).
func HistoryTable(w io.Writer, recs []Record, format string) error {
	var rows [][]string
	for _, r := range recs {
		var settings []string
		for k, v := range r.Settings {
			settings = append(settings, k+"="+v)
		}
		sort.Strings(settings)
		s := r.Summary
		usd := fmt.Sprintf("%.4f", s.Cost.Dollars)
		perUSD, perSec := "", ""
		if d, sec, ok := s.PerCorrect(); ok {
			perUSD, perSec = fmt.Sprintf("%.4f", d), fmt.Sprintf("%.1f", sec)
		}
		if len(s.Cost.Unpriced) > 0 {
			usd += "+"
			if perUSD != "" {
				perUSD += "+"
			}
		}
		secs := fmt.Sprintf("%.1f", s.Cost.Seconds)
		if s.Cost.Untimed > 0 {
			secs, perSec = "", "" // a case with no time would read as instant (#581)
		}
		rows = append(rows, []string{r.When.UTC().Format(time.RFC3339), r.Commit, r.Bench, r.Agent, r.Model, strings.Join(settings, " "),
			fmt.Sprint(r.Run), fmt.Sprint(s.Pass), fmt.Sprint(s.Wrong), fmt.Sprint(s.Abstain), fmt.Sprint(s.Cases()),
			fmt.Sprint(s.Cost.Calls), fmt.Sprint(s.Cost.TokensIn), fmt.Sprint(s.Cost.TokensCached), fmt.Sprint(s.Cost.TokensOut),
			usd, secs, perUSD, perSec})
	}
	return writeTable(w, historyColumns, rows, format)
}
