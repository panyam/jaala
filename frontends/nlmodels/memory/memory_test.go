package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval/longmemeval"
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/internal/modeltest"
	"github.com/panyam/jaala/frontends/nlmodels/translator"
)

var bg = context.Background()

// script answers each prompt with the next reply listed under the first key its turns contain, as a
// model that reads a session or a question and writes the same thing every run would. Its tokens are
// the prompt's and the reply's length over four.
type script struct {
	name    string
	replies map[string][]string
	asked   map[string]int
	seen    []string
}

func scripted(name string, replies map[string][]string) *script {
	return &script{name: name, replies: replies, asked: map[string]int{}}
}

func (m *script) Name() string { return m.name }

func (m *script) Complete(_ context.Context, p modeltest.Prompt) (modeltest.Completion, error) {
	var text strings.Builder
	for _, t := range p.Turns {
		text.WriteString(t.Text + "\n")
	}
	m.seen = append(m.seen, text.String())
	for key, rs := range m.replies {
		if !strings.Contains(text.String(), key) {
			continue
		}
		n := m.asked[key]
		m.asked[key]++
		if n >= len(rs) {
			return modeltest.Completion{}, errors.New("no reply left for " + key)
		}
		return modeltest.Completion{Text: rs[n], TokensIn: (len(p.Prefix) + text.Len()) / 4, TokensOut: len(rs[n]) / 4}, nil
	}
	return modeltest.Completion{}, fmt.Errorf("no reply for a prompt holding %q", text.String())
}

// prompts are the texts of every turn m was asked, in order.
func (m *script) prompts() []string { return m.seen }

func role(m *script) translator.Role {
	return translator.Role{Provider: modeltest.Provider(m), Label: m.name}
}

// boston and denver are two sessions, the second moving the user.
var (
	boston = longmemeval.Session{ID: "s1", Date: "2023/05/20 (Sat) 10:02", Turns: []longmemeval.Turn{
		{Role: "user", Content: "I live in Boston and I just adopted a beagle."},
		{Role: "assistant", Content: "Congratulations!"},
	}}
	denver = longmemeval.Session{ID: "s2", Date: "2023/06/02 (Fri) 09:15", Turns: []longmemeval.Turn{
		{Role: "user", Content: "Big news, I moved to Denver last week."},
	}}
	readsBoth = map[string][]string{
		"I live in Boston": {`+ fact("user", "lives in", "Boston") from "I live in Boston"
+ event("adopted a beagle", "2023/05/20") from "I just adopted a beagle"`},
		"I moved to Denver": {`+ fact("user", "lives in", "Denver") from "I moved to Denver"`},
	}
)

func ingested(t *testing.T, cfg Config, sessions ...longmemeval.Session) *Memory {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if _, err := m.Ingest(bg, s); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func column(t *testing.T, m *Memory, program string, opts ...datalog.Option) []string {
	t.Helper()
	q, err := datalog.Parse(program)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := datalog.SemiNaive{}.EvalTable(bg, q, m.Base(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range tab.Rows {
		out = append(out, r[0].S)
	}
	return out
}

func TestALaterSessionReplacesAFactAndTheEarlierVersionKeepsIt(t *testing.T) {
	m := ingested(t, Config{Extract: role(scripted("x", readsBoth))}, boston, denver)
	if m.Version() != 2 {
		t.Fatalf("version %d after two sessions, want 2", m.Version())
	}
	lives := `fact("user", "lives in", ?c) => ?c`
	if got := column(t, m, lives); fmt.Sprint(got) != "[Denver]" {
		t.Errorf("latest: %v, want [Denver]", got)
	}
	if got := column(t, m, lives, datalog.At(1)); fmt.Sprint(got) != "[Boston]" {
		t.Errorf("at 1: %v, want [Boston]", got)
	}
	// control: what the second session doesn't name still holds after it, and each session has its row
	if got := column(t, m, `event(?w, ?d) => ?w`); fmt.Sprint(got) != "[adopted a beagle]" {
		t.Errorf("latest events: %v, want the first session's", got)
	}
	if got := column(t, m, `session(?id, ?d) => ?id order by ?id`); fmt.Sprint(got) != "[s1 s2]" {
		t.Errorf("sessions: %v, want [s1 s2]", got)
	}
}

func TestIngestReportsUsageAndARefusedSessionCommitsNothing(t *testing.T) {
	bad := `+ lives("user", "Boston") from "I live in Boston"`
	m, err := New(Config{Extract: role(scripted("x", map[string][]string{"I live in Boston": {bad, bad, bad}}))})
	if err != nil {
		t.Fatal(err)
	}
	spent, err := m.Ingest(bg, boston)
	if err == nil {
		t.Fatal("a session read only into an unknown relation was accepted")
	}
	if m.Version() != 0 {
		t.Errorf("version %d after a refused session, want 0", m.Version())
	}
	if spent["x"].InputTokens == 0 || spent["x"].OutputTokens == 0 {
		t.Errorf("a refused session reported %v, want the tries' tokens under x", spent)
	}
	// control: an accepted session reports its tokens under the model's label too
	good := ingested(t, Config{Extract: role(scripted("x", readsBoth))})
	spent, err = good.Ingest(bg, boston)
	if err != nil {
		t.Fatal(err)
	}
	if len(spent) != 1 || spent["x"].OutputTokens == 0 {
		t.Errorf("an accepted session reported %v, want tokens under x only", spent)
	}
}

func call(t *testing.T, src agent.ToolSource, name string, args map[string]any) string {
	t.Helper()
	r, err := src.Call(bg, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if r.IsError {
		t.Errorf("%s %v came back as an error result", name, args)
	}
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestTheToolsAnswerWithCitesAndExplainARefusal(t *testing.T) {
	m := ingested(t, Config{Extract: role(scripted("x", readsBoth))}, boston, denver)
	tools := m.Tools()
	defs, err := tools.Tools(bg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if d.Name == "query" && !strings.Contains(d.Description, "fact(subject") {
			t.Errorf("query's description doesn't carry the card:\n%s", d.Description)
		}
	}
	if fmt.Sprint(names) != "[query read_session]" {
		t.Errorf("tools %v, want [query read_session]", names)
	}
	got := call(t, tools, "query", map[string]any{"program": `fact("user", "lives in", ?c) => ?c`})
	if !strings.Contains(got, "Denver") || !strings.Contains(got, "I moved to Denver") {
		t.Errorf("query answered\n%s\nwant Denver, citing the words that said it", got)
	}
	unknown := `lives(?c) => ?c`
	q, err := datalog.Parse(unknown)
	if err != nil {
		t.Fatal(err)
	}
	_, evalErr := datalog.SemiNaive{}.EvalTable(bg, q, m.Base())
	broken := `fact("user", ?a`
	_, parseErr := datalog.Parse(broken)
	if evalErr == nil || parseErr == nil {
		t.Fatalf("control: %v, %v", evalErr, parseErr)
	}
	for program, want := range map[string]string{unknown: nl.Explain(unknown, evalErr), broken: nl.Explain(broken, parseErr)} {
		if got := call(t, tools, "query", map[string]any{"program": program}); got != want {
			t.Errorf("query %s came back as\n%s\nwant nl's explanation\n%s", program, got, want)
		}
	}
	if got := call(t, tools, "read_session", map[string]any{"id": "s2"}); !strings.Contains(got, "Big news, I moved to Denver") {
		t.Errorf("read_session s2: %q", got)
	}
	if got := call(t, tools, "read_session", map[string]any{"id": "s9"}); got != `no session "s9"` {
		t.Errorf("read_session s9: %q", got)
	}
}

var question = longmemeval.Instance{
	QuestionID: "q1", QuestionType: "knowledge-update", Question: "Which city does the user live in now?",
	QuestionDate:       "2023/06/10 (Sat) 12:00",
	HaystackSessionIDs: []string{"s1", "s2"}, HaystackDates: []string{boston.Date, denver.Date},
	HaystackSessions: [][]longmemeval.Turn{boston.Turns, denver.Turns},
}

func TestOneShotArmPutsTheCitedRowsInTheInstructions(t *testing.T) {
	asks := scripted("q", map[string][]string{question.Question: {`fact("user", "lives in", ?c) => ?c`}})
	setup, err := OneShotArm(Config{Extract: role(scripted("x", readsBoth)), Ask: role(asks)})(bg, question)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{question.QuestionDate, "Denver", "I moved to Denver"} {
		if !strings.Contains(setup.Instructions, want) {
			t.Errorf("instructions lack %q:\n%s", want, setup.Instructions)
		}
	}
	if strings.Contains(setup.Instructions, "Boston") {
		t.Errorf("instructions carry the replaced city:\n%s", setup.Instructions)
	}
	if setup.Usage["x"].OutputTokens == 0 || setup.Usage["q"].OutputTokens == 0 {
		t.Errorf("usage %v, want the extraction's under x and the question's under q", setup.Usage)
	}
}

func TestOneShotArmSaysNothingAnsweredWhenNoProgramDid(t *testing.T) {
	bad := `lives(?c) => ?c`
	asks := scripted("q", map[string][]string{question.Question: {bad, bad, bad}})
	setup, err := OneShotArm(Config{Extract: role(scripted("x", readsBoth)), Ask: role(asks)})(bg, question)
	if err != nil {
		t.Fatalf("a question no program answered failed the case: %v", err)
	}
	if !strings.HasPrefix(setup.Instructions, fmt.Sprintf(strings.SplitN(DefaultPrompts.NoAnswer, "(", 2)[0], question.QuestionDate)) {
		t.Errorf("instructions:\n%s\nwant NoAnswer's", setup.Instructions)
	}
	if setup.Usage["q"].OutputTokens == 0 {
		t.Errorf("usage %v, want the failed question's tokens under q", setup.Usage)
	}
}

func TestChakrasMemoryArmGivesTheAgentTheMemorysTools(t *testing.T) {
	arm := longmemeval.MemoryArm(func() (longmemeval.SessionMemory, error) {
		return New(Config{Extract: role(scripted("x", readsBoth))})
	})
	setup, err := arm(bg, question)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Tools == nil {
		t.Fatal("no tools")
	}
	if got := call(t, setup.Tools, "query", map[string]any{"program": `session(?id, ?d) => ?id order by ?id`}); !strings.Contains(got, "s1") || !strings.Contains(got, "s2") {
		t.Errorf("the arm's memory holds\n%s\nwant both sessions", got)
	}
	if setup.Usage["x"].OutputTokens == 0 {
		t.Errorf("usage %v, want both sessions' extraction under x", setup.Usage)
	}
}

func TestPromptsKeepTheirPlaceholdersAndAHostsOwnText(t *testing.T) {
	for name, p := range map[string]string{"Answer": DefaultPrompts.Answer, "NoAnswer": DefaultPrompts.NoAnswer} {
		if n := strings.Count(p, "%s"); n != 2 {
			t.Errorf("%s has %d placeholders, want 2", name, n)
		}
	}
	got := Prompts{Query: "mine"}.withDefaults()
	if got.Query != "mine" || got.ReadSession != DefaultPrompts.ReadSession {
		t.Errorf("withDefaults: %+v", got)
	}
}

// A later session's prompt lists the fact keys the memory holds, so the model can update one under
// the same key (#563). control: the first session's prompt lists none.
func TestALaterSessionIsShownTheKeysHeld(t *testing.T) {
	x := scripted("x", readsBoth)
	ingested(t, Config{Extract: role(x)}, boston, denver)
	p := x.prompts()
	if len(p) != 2 {
		t.Fatalf("%d prompts, want one per session", len(p))
	}
	if strings.Contains(p[0], `fact("user"`) {
		t.Errorf("the first session was shown keys:\n%s", p[0])
	}
	if !strings.Contains(p[1], `fact("user", "lives in", _)`) {
		t.Errorf("the second session wasn't shown the key the first left:\n%s", p[1])
	}
}

// A session line the model writes is dropped, so each session keeps the one row Ingest writes, with
// the session's full date.
func TestASessionLineTheModelWritesIsDropped(t *testing.T) {
	m := ingested(t, Config{Extract: role(scripted("x", map[string][]string{"I live in Boston": {`+ session("s1", "2023/05/20") from "Session s1, 2023/05/20"
+ fact("user", "lives in", "Boston") from "I live in Boston"`}}))}, boston)
	if got := column(t, m, `session(?id, ?d) => ?d`); fmt.Sprint(got) != "["+boston.Date+"]" {
		t.Errorf("session rows %v, want the one with %q", got, boston.Date)
	}
}

// An extraction whose every reply reached its token cap fails the session, rather than committing it
// as one that states nothing (#563). control: the same model with room to reply commits the session.
func TestACutExtractionFailsTheSession(t *testing.T) {
	line := readsBoth["I live in Boston"][0]
	cut := role(scripted("x", map[string][]string{"I live in Boston": {line, line, line}}))
	cut.MaxTokens = 1
	m, err := New(Config{Extract: cut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ingest(bg, boston); !errors.Is(err, nl.ErrTruncated) {
		t.Errorf("a session every reply of which was cut: %v, want ErrTruncated", err)
	}
	if m.Version() != 0 {
		t.Errorf("version %d after a cut session, want 0", m.Version())
	}
	if ok := ingested(t, Config{Extract: role(scripted("x", readsBoth))}, boston); ok.Version() != 1 {
		t.Errorf("control: version %d, want 1", ok.Version())
	}
}
