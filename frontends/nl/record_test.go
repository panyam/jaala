package nl_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// scored runs Eval over cases whose translate answers from outcomes by question: "pass" answers the
// case's row, "wrong" another, and "abstain" an agent's abstention.
func scored(t *testing.T, cases []nl.Case, traces map[string]nl.Trace) nl.Report {
	t.Helper()
	_, b := depsBase(t)
	return nl.Eval(context.Background(), cases, b, func(_ context.Context, q string) (nl.Program, nl.Trace, error) {
		tr := traces[q]
		switch {
		case strings.HasPrefix(q, "pass"):
			return nl.Program{Text: "p", Rows: []datalog.Row{{Bind: map[datalog.Var]ns.Value{"x": ns.S("a")}}}}, tr, nil
		case strings.HasPrefix(q, "wrong"):
			return nl.Program{Text: "w", Rows: []datalog.Row{{Bind: map[datalog.Var]ns.Value{"x": ns.S("b")}}}}, tr, nil
		}
		return nl.Program{}, tr, &nl.Abstained{Reason: nl.AbstainAgent, Diagnosis: "unsure"}
	})
}

func TestAnAbstentionIsCountedApartFromAWrongAnswer(t *testing.T) {
	want := [][]string{{"a"}}
	rep := scored(t, []nl.Case{
		{Name: "p", Question: "pass", Want: want},
		{Name: "w", Question: "wrong", Want: want},
		{Name: "a", Question: "abstain", Want: want},
	}, nil)
	var got []string
	for _, r := range rep.Cases {
		got = append(got, r.Outcome()+":"+r.AbstainReason())
	}
	if w := []string{"pass:", "wrong:", "abstain:agent"}; !reflect.DeepEqual(got, w) {
		t.Errorf("outcomes %v, want %v", got, w)
	}
	if rep.Passed != 1 || rep.Wrong != 1 || rep.Abstained != 1 {
		t.Errorf("report passed %d, wrong %d, abstained %d; want 1 each", rep.Passed, rep.Wrong, rep.Abstained)
	}
	if s := rep.Summary(nil); s.Tally != (nl.Tally{Pass: 1, Wrong: 1, Abstain: 1}) || s.Cases() != 3 {
		t.Errorf("summary %+v", s.Tally)
	}
}

// A failure that isn't an abstention is wrong, even one wrapping a budget: only an *Abstained abstains.
func TestAFailureIsWrongUnlessItAbstained(t *testing.T) {
	_, b := depsBase(t)
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "f", Question: "f"}, {Name: "a", Question: "a"}}, b,
		func(_ context.Context, q string) (nl.Program, nl.Trace, error) {
			spent := &nl.BudgetSpent{Tokens: 9}
			if q == "a" {
				return nl.Program{}, nl.Trace{}, &nl.Abstained{Reason: nl.AbstainBudget, Err: spent}
			}
			return nl.Program{}, nl.Trace{}, spent
		})
	if o := rep.Cases[0].Outcome(); o != nl.OutcomeWrong {
		t.Errorf("a spent budget is %q, want wrong", o)
	}
	if o, why := rep.Cases[1].Outcome(), rep.Cases[1].AbstainReason(); o != nl.OutcomeAbstain || why != nl.AbstainBudget {
		t.Errorf("an abstention on the budget is %q (%q), want abstain (budget)", o, why)
	}
}

func TestSummaryCountsEachTagApart(t *testing.T) {
	want := [][]string{{"a"}}
	rep := scored(t, []nl.Case{
		{Name: "1", Question: "pass1", Want: want, Tags: []string{"closure", "join"}},
		{Name: "2", Question: "wrong2", Want: want, Tags: []string{"closure"}},
		{Name: "3", Question: "abstain3", Want: want, Tags: []string{"join"}},
		{Name: "4", Question: "pass4", Want: want},
	}, nil)
	s := rep.Summary(nil)
	want2 := map[string]nl.Tally{"closure": {Pass: 1, Wrong: 1}, "join": {Pass: 1, Abstain: 1}}
	if !reflect.DeepEqual(s.Tags, want2) {
		t.Errorf("tags %+v, want %+v", s.Tags, want2)
	}
	if s.Tally != (nl.Tally{Pass: 2, Wrong: 1, Abstain: 1}) {
		t.Errorf("control: the run's tally %+v counts the untagged case too", s.Tally)
	}
}

// Cost prices each model's tokens by its own Price, the cached part at the cached rate, counts a call
// per try or step a loop gave no Calls (and none for one with no tokens), and names a model with no
// Price instead of counting it free.
func TestCostPricesEachModelAndNamesTheUnpriced(t *testing.T) {
	tr := nl.Trace{
		Tries: []nl.Try{
			{Model: "small", TokensIn: 1_000_000, TokensCached: 400_000, TokensOut: 100_000, Calls: 2},
			{Model: "big", TokensIn: 10, TokensOut: 5},
		},
		Steps: []nl.Step{
			{Model: "small", TokensIn: 500_000, TokensOut: 0},
			{Model: "small"}, // the second action of one reply: no tokens, no call
			{Model: "idle"},  // no tokens, so no price needed
		},
		Votes: []nl.Try{{Model: "small", TokensOut: 1_000_000, Calls: 1}},
	}
	rep := scored(t, []nl.Case{{Name: "p", Question: "pass", Want: [][]string{{"a"}}}}, map[string]nl.Trace{"pass": tr})
	prices := nl.Prices{"small": {In: 1, Cached: 0.1, Out: 4}}
	c := rep.Cases[0].Cost(prices)
	// tries: 0.6*1 + 0.4*0.1 + 0.1*4 = 1.04; step: 0.5; vote: 4
	if math.Abs(c.Dollars-5.54) > 1e-9 {
		t.Errorf("dollars %v, want 5.54", c.Dollars)
	}
	if c.Calls != 5 || c.TokensIn != 1_500_010 || c.TokensCached != 400_000 || c.TokensOut != 1_100_005 {
		t.Errorf("cost %+v", c)
	}
	if !reflect.DeepEqual(c.Unpriced, []string{"big"}) {
		t.Errorf("unpriced %v, want [big]", c.Unpriced)
	}
	priced := rep.Cases[0].Cost(nl.Prices{"small": {In: 1, Cached: 0.1, Out: 4}, "big": {}})
	if priced.Unpriced != nil || priced.Dollars != c.Dollars {
		t.Errorf("control: a free model is priced, not unpriced: %+v", priced)
	}
	d, sec, ok := rep.Summary(prices).PerCorrect()
	if !ok || d != c.Dollars || sec != rep.Cases[0].Took.Seconds() {
		t.Errorf("per correct %v %v %v", d, sec, ok)
	}
}

func TestEvalTimesEachCase(t *testing.T) {
	_, b := depsBase(t)
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "s", Question: "s"}}, b, func(context.Context, string) (nl.Program, nl.Trace, error) {
		time.Sleep(20 * time.Millisecond)
		return nl.Program{}, nl.Trace{}, errors.New("slow")
	})
	if took := rep.Cases[0].Took; took < 20*time.Millisecond {
		t.Errorf("took %v, want at least the 20ms translate slept", took)
	}
}

func TestNothingPassedHasNoCostPerCorrectAnswer(t *testing.T) {
	rep := scored(t, []nl.Case{{Name: "w", Question: "wrong", Want: [][]string{{"a"}}}}, nil)
	if _, _, ok := rep.Summary(nil).PerCorrect(); ok {
		t.Error("a run with no pass has a cost per correct answer")
	}
}

// A record written to a directory reads back equal, the earliest first, beside a file that isn't one.
func TestARecordReadsBackAsItWasWritten(t *testing.T) {
	want := [][]string{{"a"}}
	tr := nl.Trace{
		Tries: []nl.Try{
			{Model: "small", Text: "bad(", Kind: nl.KindParse, Err: errors.New("query: no"), TokensIn: 7, Calls: 1, Latency: time.Second},
			{Model: "small", Text: "p", TokensIn: 9, TokensOut: 2},
		},
		Steps: []nl.Step{{Model: "small", Action: nl.ActionLook, Text: "look edge", Result: "long", After: 1, TokensIn: 3}},
	}
	rep := scored(t, []nl.Case{
		{Name: "p", Question: "pass", Want: want, Tags: []string{"one hop"}},
		{Name: "a", Question: "abstain", Want: want},
	}, map[string]nl.Trace{"pass": tr})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a record"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := nl.NewRecord(rep, nl.Prices{"small": {In: 1}})
	later.When, later.Commit, later.Bench, later.Agent, later.Model = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), "abc", "external", "translate", "small"
	later.Settings = map[string]string{"actions": "6"}
	earlier := later
	earlier.When, earlier.Run = later.When.Add(-time.Hour), 2
	for _, r := range []nl.Record{later, earlier} {
		path, err := r.Write(dir)
		if err != nil {
			t.Fatal(err)
		}
		if r.Run == 0 { // a name sorting first, so the order read back is When's and not the files'
			if err := os.Rename(path, filepath.Join(dir, "0-later.json")); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := nl.ReadRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []nl.Record{earlier, later}) {
		t.Fatalf("read back\n%+v\nwant\n%+v", got, []nl.Record{earlier, later})
	}
	c := got[1].Cases[0]
	if c.Outcome != nl.OutcomePass || len(c.Tries) != 2 || c.Tries[0].Class != nl.ClassConstruct || c.Tries[0].Err != "query: no" ||
		c.Tries[0].Seconds != 1 || c.Steps[0].Action != nl.ActionLook || c.Program != "p" {
		t.Errorf("case record %+v", c)
	}
	if a := got[1].Cases[1]; a.Outcome != nl.OutcomeAbstain || a.Abstained != nl.AbstainAgent || a.Class != nl.ClassAbstained {
		t.Errorf("abstained case record %+v", a)
	}
}

func TestHistoryTableGivesCostPerCorrectAnswer(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	priced := nl.Record{When: at, Commit: "abc", Bench: "external", Agent: "translate", Model: "small", Settings: map[string]string{"b": "2", "a": "1"},
		Summary: nl.Summary{Tally: nl.Tally{Pass: 2, Wrong: 1, Abstain: 1}, Cost: nl.Cost{Calls: 8, TokensIn: 100, TokensOut: 10, Dollars: 0.5, Seconds: 30}}}
	unpriced := priced
	unpriced.Summary.Cost.Unpriced = []string{"big"}
	none := priced
	none.Summary.Tally = nl.Tally{Wrong: 1}
	var b strings.Builder
	if err := nl.HistoryTable(&b, []nl.Record{priced, unpriced, none}, "md"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	want := []string{
		"| 2026-11-09T12:00:00Z | abc | external | translate | small | a=1 b=2 | 0 | 2 | 1 | 1 | 4 | 8 | 100 | 0 | 10 | 0.5000 | 30.0 | 0.2500 | 15.0 |",
		"| 2026-11-09T12:00:00Z | abc | external | translate | small | a=1 b=2 | 0 | 2 | 1 | 1 | 4 | 8 | 100 | 0 | 10 | 0.5000+ | 30.0 | 0.2500+ | 15.0 |",
		"| 2026-11-09T12:00:00Z | abc | external | translate | small | a=1 b=2 | 0 | 0 | 1 | 0 | 1 | 8 | 100 | 0 | 10 | 0.5000 | 30.0 |  |  |",
	}
	if len(lines) != 5 || !reflect.DeepEqual(lines[2:], want) {
		t.Errorf("table\n%s\nwant rows\n%s", b.String(), strings.Join(want, "\n"))
	}
	if err := nl.HistoryTable(&b, nil, "xml"); err == nil {
		t.Error("an unknown format writes nothing and no error")
	}
}
