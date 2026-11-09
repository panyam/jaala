package nl_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// Relations reads every atom, negated, optional or inside an aggregate's braces too, and leaves out
// the relations the program's own rules define.
func TestRelationsReadLeaveOutTheProgramsOwnRules(t *testing.T) {
	q := datalog.MustParse(`reach(?x, ?y) :- edge(?x, ?y); reach(?x, ?z) :- reach(?x, ?y), hop(?y, ?z);
		reach(?a, ?b), not banned(?b), optional { label(?b, ?l) }, ?n = count(?c) : { owns(?a, ?c) } => ?a, ?b, ?l, ?n`)
	got := nl.Result{Program: nl.Program{Query: q}}.Relations()
	if want := []string{"banned", "edge", "hop", "label", "owns"}; !reflect.DeepEqual(got, want) {
		t.Errorf("relations %v, want %v", got, want)
	}
}

// Eval runs an abstention's draft and says whether it would have passed: a draft in jaala's language
// is parsed and run, a low-agreement answer is scored on its rows, and a front end's draft, a missing
// draft and a case that didn't abstain have nothing to say.
func TestEvalRunsTheDraftAnAbstentionOffered(t *testing.T) {
	_, b := depsBase(t)
	app := [][]string{{"app"}}
	drafts := map[string]*nl.Abstained{
		"right":    {Reason: nl.AbstainNoAnswer, Draft: nl.Program{Text: `pkg(?p, "MIT") => ?p`}},
		"wrong":    {Reason: nl.AbstainNoAnswer, Draft: nl.Program{Text: `pkg(?p, "Apache-2.0") => ?p`}},
		"ran":      {Reason: nl.AbstainLowAgreement, Draft: nl.Program{Text: "not run", Language: "cypher", Rows: []datalog.Row{{Bind: map[datalog.Var]ns.Value{"p": ns.S("app")}}}}},
		"cypher":   {Reason: nl.AbstainNoAnswer, Draft: nl.Program{Text: `pkg(?p, "MIT") => ?p`, Language: "cypher"}}, // jaala's text, labelled another language
		"nodraft":  {Reason: nl.AbstainNoAnswer},
		"unparsed": {Reason: nl.AbstainNoAnswer, Draft: nl.Program{Text: `pkg(`}},
	}
	var cases []nl.Case
	for _, n := range []string{"right", "wrong", "ran", "cypher", "nodraft", "unparsed", "pass"} {
		cases = append(cases, nl.Case{Name: n, Question: n, Want: app, Reads: []string{"pkg"}})
	}
	rep := nl.Eval(context.Background(), cases, b, func(_ context.Context, q string) (nl.Program, nl.Trace, error) {
		if q == "pass" {
			return nl.Program{Text: "p", Query: datalog.MustParse(`pkg(?p, "MIT") => ?p`)}, nl.Trace{}, nil
		}
		return nl.Program{}, nl.Trace{}, drafts[q]
	})
	got := map[string]string{}
	for _, r := range rep.Cases {
		got[r.Name] = "nil"
		if r.WouldPass != nil {
			got[r.Name] = map[bool]string{true: "pass", false: "wrong"}[*r.WouldPass]
		}
	}
	want := map[string]string{"right": "pass", "wrong": "wrong", "ran": "pass", "cypher": "nil", "nodraft": "nil", "unparsed": "nil", "pass": "nil"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("would pass %v, want %v", got, want)
	}
	rec := nl.NewRecord(rep, nil)
	if c := rec.Cases[0]; c.WouldPass == nil || !*c.WouldPass || !reflect.DeepEqual(c.Reads, []string{"pkg"}) || c.Relations != nil {
		t.Errorf("abstained case record %+v", c)
	}
	if c := rec.Cases[6]; c.Outcome != nl.OutcomePass || !reflect.DeepEqual(c.Relations, []string{"pkg"}) {
		t.Errorf("control: the passing case records the relations its program read: %+v", c)
	}
}

// Each skill reads its own evidence from the tries: a case that can't show a skill isn't counted
// toward it, and two records of one model add up.
func TestProfileReadsEachSkillFromTheTries(t *testing.T) {
	try := func(class, kind, err string) nl.TryRecord {
		return nl.TryRecord{Model: "m", Class: class, Kind: kind, Err: err}
	}
	ok := nl.TryRecord{Model: "m"}
	yes, no := true, false
	small := nl.Record{Model: "small", Summary: nl.Summary{Tally: nl.Tally{Pass: 2, Wrong: 1}, Tags: map[string]nl.Tally{"hop": {Pass: 1}},
		Cost: nl.Cost{Dollars: 1, Seconds: 10}}, Cases: []nl.CaseRecord{
		// a parse error, then a grounding miss, then an accepted draft reading the right relation
		// "mit" offered up front and again by a find, used; "app" offered, not used
		{Outcome: nl.OutcomePass, Program: "p", Reads: []string{"pkg"}, Relations: []string{"imports", "pkg"},
			Offered:   []nl.Candidate{{Name: "mit", Values: []string{"MIT"}}, {Name: "app", Values: []string{"app-1", "app-2"}}, {Name: "mit", Values: []string{"MIT-0"}}},
			Constants: []string{"MIT-0", "api"},
			Tries:     []nl.TryRecord{try(nl.ClassConstruct, nl.KindParse, "bad"), try(nl.ClassGrounding, nl.KindEmpty, "empty"), ok}},
		// right first time, reading the wrong relation
		{Outcome: nl.OutcomePass, Program: "p", Reads: []string{"pkg"}, Relations: []string{"imports"}, Tries: []nl.TryRecord{ok}},
		// a model failure first: no syntax, and no repair after it; then a harness repair, which isn't the model's
		// offered, but no accepted program: not a grounding case
		{Outcome: nl.OutcomeWrong, Offered: []nl.Candidate{{Name: "x", Values: []string{"x1"}}}, Tries: []nl.TryRecord{try(nl.ClassModel, nl.KindModel, "down"), try(nl.ClassSemantics, nl.KindAnswer, "no"), {Model: "m", Repair: "mit"}}},
	}}
	more := nl.Record{Model: "small", Summary: nl.Summary{Tally: nl.Tally{Abstain: 2}, Tags: map[string]nl.Tally{"hop": {Abstain: 1}}, Cost: nl.Cost{Dollars: 1, Seconds: 10}},
		Cases: []nl.CaseRecord{
			{Outcome: nl.OutcomeAbstain, WouldPass: &no, Tries: []nl.TryRecord{try(nl.ClassGrounding, nl.KindEmpty, "empty")}},
			{Outcome: nl.OutcomeAbstain, WouldPass: &yes, Tries: []nl.TryRecord{try(nl.ClassConstruct, nl.KindParse, "bad"), try("", nl.KindRepeat, "again")}},
			{Outcome: nl.OutcomeAbstain, WouldPass: &no},
			// no Reads: not a schema case; a name offered nothing isn't a grounding case
			{Outcome: nl.OutcomePass, Program: "p", Relations: []string{"pkg"}, Offered: []nl.Candidate{{Name: "lib", Values: []string{"lib"}}, {Name: "none"}}, Constants: []string{"lib"}},
		}}
	big := nl.Record{Model: "big", Summary: nl.Summary{Tally: nl.Tally{Pass: 1}}}
	ps := nl.Profile([]nl.Record{small, big, more}, nl.Prices{"small": {In: 0.5}})
	if len(ps) != 2 || ps[0].Model != "big" || ps[1].Model != "small" || ps[0].Priced || !ps[1].Priced || ps[1].Price.In != 0.5 {
		t.Fatalf("profiles %+v", ps)
	}
	p := ps[1]
	got := map[string]nl.Rate{"syntax": p.Syntax, "grounding": p.Grounding, "matched": p.Matched, "schema": p.Schema, "repair": p.Repair, "calibration": p.Calibration}
	want := map[string]nl.Rate{
		"syntax":      {Hit: 2, Of: 4}, // the model failure's case is left out
		"grounding":   {Hit: 2, Of: 3}, // per name: mit (as MIT-0) and lib used, app not
		"matched":     {Hit: 2, Of: 4}, // the first try that ran; the last case never ran one
		"schema":      {Hit: 1, Of: 2},
		"repair":      {Hit: 1, Of: 4}, // parse→grounding, grounding→ok, semantics→harness repair, parse→repeat
		"calibration": {Hit: 2, Of: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("skills %+v, want %+v", got, want)
	}
	if s := p.Summary; s.Tally != (nl.Tally{Pass: 2, Wrong: 1, Abstain: 2}) || s.Tags["hop"] != (nl.Tally{Pass: 1, Abstain: 1}) || s.Cost.Dollars != 2 || s.Cost.Seconds != 20 {
		t.Errorf("control: the two records add up: %+v", s)
	}
}

func TestProfileTablePutsThePriceBesideTheSkills(t *testing.T) {
	ps := []nl.ModelProfile{
		{Model: "small", Price: nl.Price{In: 0.5, Cached: 0.05, Out: 2}, Priced: true,
			Summary: nl.Summary{Tally: nl.Tally{Pass: 2, Wrong: 1}, Tags: map[string]nl.Tally{"join": {Wrong: 1}, "hop": {Pass: 2}}, Cost: nl.Cost{Dollars: 1, Seconds: 30}},
			Syntax:  nl.Rate{Hit: 2, Of: 3}, Grounding: nl.Rate{Hit: 1, Of: 2}, Repair: nl.Rate{Hit: 1, Of: 1}},
		{Model: "big", Summary: nl.Summary{Tally: nl.Tally{Pass: 1}, Cost: nl.Cost{Dollars: 1, Seconds: 4, Unpriced: []string{"big"}}}},
	}
	var b strings.Builder
	if err := nl.ProfileTable(&b, ps, "md"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	want := []string{
		"| small | 0.5 | 0.05 | 2 | 2 | 1 | 0 | 3 | 0.5000 | 15.0 | 2/3 | 1/2 |  |  | 1/1 |  | hop 2/0/0; join 0/1/0 |",
		"| big |  |  |  | 1 | 0 | 0 | 1 | 1.0000+ | 4.0 |  |  |  |  |  |  |  |",
	}
	if len(lines) != 4 || !reflect.DeepEqual(lines[2:], want) {
		t.Errorf("table\n%s\nwant rows\n%s", b.String(), strings.Join(want, "\n"))
	}
	if err := nl.ProfileTable(&b, nil, "xml"); err == nil {
		t.Error("an unknown format writes nothing and no error")
	}
}

// Constants reads every constant the accepted program writes, in atoms, comparisons, expressions,
// rule heads and braces, and leaves out variables and absent.
func TestConstantsAreWhatTheProgramWrites(t *testing.T) {
	q := datalog.MustParse(`lic(?p, "tag") :- pkg(?p, "MIT"); lic(?p, ?t), "left" != ?t, not dep(?p, "old"), ?n = count(?d) : { dep(?p, ?d), ?d != "x" }, ?w = ?n * 2, optional { attr(?p, "k", ?v) } => ?p`)
	got := nl.Result{Program: nl.Program{Query: q}}.Constants()
	if want := []string{"2", "MIT", "k", "left", "old", "tag", "x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("constants %v, want %v", got, want)
	}
	if c := (nl.Result{}).Constants(); c != nil {
		t.Errorf("control: no program has constants %v", c)
	}
}

// A record keeps what the Grounder offered and the constants the accepted program wrote, which
// grounding compares.
func TestARecordKeepsWhatWasOfferedAndWhatWasWritten(t *testing.T) {
	offered := []nl.Candidate{{Name: "mit", Values: []string{"MIT"}}}
	r := nl.Result{Case: nl.Case{Name: "c"}, Program: nl.Program{Text: "p", Query: datalog.MustParse(`pkg(?p, "MIT") => ?p`)}, Trace: nl.Trace{Offered: offered}}
	c := nl.NewRecord(nl.Report{Cases: []nl.Result{r}}, nil).Cases[0]
	if !reflect.DeepEqual(c.Offered, offered) || !reflect.DeepEqual(c.Constants, []string{"MIT"}) {
		t.Errorf("record offered %+v, constants %v", c.Offered, c.Constants)
	}
	if p := nl.Profile([]nl.Record{{Model: "m", Cases: []nl.CaseRecord{c}}}, nil); p[0].Grounding != (nl.Rate{Hit: 1, Of: 1}) {
		t.Errorf("control: the record's own fields ground: %+v", p[0].Grounding)
	}
}
