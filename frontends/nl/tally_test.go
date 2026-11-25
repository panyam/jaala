package nl_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/frontends/nl"
)

// Tally.Add counts jaala's three outcomes, as Summary does, and nothing else (#556).
func TestTallyAddCountsEachOutcome(t *testing.T) {
	var tl nl.Tally
	for _, o := range []string{nl.OutcomePass, nl.OutcomePass, nl.OutcomeWrong, nl.OutcomeAbstain, "pending", ""} {
		tl.Add(o)
	}
	if tl != (nl.Tally{Pass: 2, Wrong: 1, Abstain: 1}) || tl.Cases() != 4 {
		t.Errorf("tally %+v", tl)
	}
}

// taggedReport has a case per outcome, two tags across them, and one case with none.
func taggedReport() nl.Report {
	return nl.Report{Cases: []nl.Result{
		{Name: "a", Tags: []string{"closure", "join"}, Pass: true},
		{Name: "b", Tags: []string{"closure"}, Err: errors.New("other rows")},
		{Name: "c", Tags: []string{"join"}, Err: &nl.Abstained{Reason: nl.AbstainBudget}},
		{Name: "d", Pass: true},
	}}
}

// TagTallies counts a record's cases per tag as Summary.Tags counts the Results they came from
// (#556). control: the untagged case counts in no tag.
func TestTagTalliesCountRecordsAsSummaryCountsResults(t *testing.T) {
	rec := nl.NewRecord(taggedReport(), nil)
	got := nl.TagTallies(rec.Cases)
	if !reflect.DeepEqual(got, rec.Summary.Tags) {
		t.Errorf("TagTallies %+v, Summary.Tags %+v", got, rec.Summary.Tags)
	}
	want := map[string]nl.Tally{"closure": {Pass: 1, Wrong: 1}, "join": {Pass: 1, Abstain: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TagTallies %+v, want %+v", got, want)
	}
	if nl.TagTallies([]nl.CaseRecord{{Name: "d", Outcome: nl.OutcomePass}}) != nil {
		t.Error("control: an untagged case was counted in a tag")
	}
}

// HistoryTagTable writes a row per record and tag, tags in name order, and none for a record with
// no tags.
func TestHistoryTagTableWritesARowPerTag(t *testing.T) {
	rec := nl.NewRecord(taggedReport(), nil)
	rec.When, rec.Commit, rec.Bench, rec.Agent, rec.Model, rec.Run = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC), "abc", "external", "claude", "opus", 2
	untagged := rec
	untagged.Cases = rec.Cases[3:]
	var b strings.Builder
	if err := nl.HistoryTagTable(&b, []nl.Record{rec, untagged}, "md"); err != nil {
		t.Fatal(err)
	}
	want := "| when | commit | bench | agent | model | run | tag | pass | wrong | abstain | of |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| 2026-11-25T09:00:00Z | abc | external | claude | opus | 2 | closure | 1 | 1 | 0 | 2 |\n" +
		"| 2026-11-25T09:00:00Z | abc | external | claude | opus | 2 | join | 1 | 0 | 1 | 2 |\n"
	if b.String() != want {
		t.Errorf("table\n%s\nwant\n%s", b.String(), want)
	}
	b.Reset()
	if err := nl.HistoryTagTable(&b, []nl.Record{rec}, "csv"); err != nil || !strings.HasPrefix(b.String(), "when,commit,bench,agent,model,run,tag,pass,wrong,abstain,of\n2026-11-25T09:00:00Z,abc,external,claude,opus,2,closure,1,1,0,2\n") {
		t.Errorf("csv %q, %v", b.String(), err)
	}
	if err := nl.HistoryTagTable(&b, nil, "xml"); err == nil {
		t.Error("an unknown format writes nothing and no error")
	}
}
