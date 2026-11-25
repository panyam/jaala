package nl_test

import (
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/frontends/nl"
)

// untimedReport is a replay where one case kept its time and one didn't, each passing with a
// dollar's worth of tokens on a tier.
func untimedReport() nl.Report {
	call := nl.Trace{Tries: []nl.Try{{Model: "big", Tier: nl.TierLocal, TokensIn: 1_000_000, Calls: 1, Latency: 2 * time.Second}}}
	return nl.Report{Cases: []nl.Result{
		{Case: nl.Case{Name: "timed"}, Pass: true, Took: 10 * time.Second, Trace: call},
		{Case: nl.Case{Name: "replayed"}, Pass: true, Took: 3 * time.Second, Untimed: true, Trace: call},
	}}
}

// An untimed case costs no seconds and counts in Untimed, its tier's cost too, and a record's
// Summary sums it (#581). control: the timed case keeps its wall time.
func TestAnUntimedCaseCountsNoSeconds(t *testing.T) {
	rep, prices := untimedReport(), nl.Prices{"big": {In: 1}}
	timed, replayed := rep.Cases[0].Cost(prices), rep.Cases[1].Cost(prices)
	if replayed.Seconds != 0 || replayed.Untimed != 1 || replayed.Dollars != 1 {
		t.Errorf("replayed cost %+v", replayed)
	}
	if timed.Seconds != 10 || timed.Untimed != 0 {
		t.Errorf("control: timed cost %+v", timed)
	}
	if tc := rep.Cases[1].TierCost(prices)[nl.TierLocal]; tc.Seconds != 0 || tc.Untimed != 1 {
		t.Errorf("replayed tier cost %+v", tc)
	}
	s := rep.Summary(prices)
	if s.Cost.Seconds != 10 || s.Cost.Untimed != 1 {
		t.Errorf("summary cost %+v", s.Cost)
	}
	if ps := nl.Profile([]nl.Record{nl.NewRecord(rep, prices), nl.NewRecord(rep, prices)}, prices); len(ps) != 1 || ps[0].Summary.Cost.Untimed != 2 {
		t.Errorf("profile %+v", ps)
	}
}

// A record with an untimed case leaves its seconds and seconds per correct blank in the history
// and profile tables, and keeps its dollars (#581). control: the timed case alone shows its time.
func TestTheTablesLeaveAnUnknownTimeBlank(t *testing.T) {
	prices := nl.Prices{"big": {In: 1}}
	rep := untimedReport()
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	rec := nl.NewRecord(rep, prices)
	rec.When, rec.Model = at, "big"
	timed := nl.NewRecord(nl.Report{Cases: rep.Cases[:1]}, prices)
	timed.When, timed.Model = at, "big"

	var b strings.Builder
	if err := nl.HistoryTable(&b, []nl.Record{rec, timed}, "csv"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[1], ",2.0000,,1.0000,") || !strings.HasSuffix(lines[2], ",1.0000,10.0,1.0000,10.0") {
		t.Errorf("history\n%s", b.String())
	}

	for _, c := range []struct {
		recs    []nl.Record
		perCase string
	}{{[]nl.Record{rec}, "| 1.0000 |  |"}, {[]nl.Record{timed}, "| 1.0000 | 10.0 |"}} {
		b.Reset()
		if err := nl.ProfileTable(&b, nl.Profile(c.recs, prices), "md"); err != nil {
			t.Fatal(err)
		}
		if row := strings.Split(strings.TrimSpace(b.String()), "\n")[2]; !strings.Contains(row, c.perCase) {
			t.Errorf("profile row %s, want %s", row, c.perCase)
		}
	}
}
