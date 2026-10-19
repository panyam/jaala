package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/testutil"
)

// roads is the toy host: towns a to e joined by one-way roads in a line, a weight per town, and a
// reach module.
func roads(t *testing.T) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	src := roadSource(func(string) bool { return true })
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	if err := v.AddModule("trip", datalog.LanguageName, `# Every town a town gets to by roads.
reach(?a, ?b) :- road(?a, ?b);
reach(?a, ?c) :- reach(?a, ?b), road(?b, ?c);
`, ""); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// roadSource is the toy host's facts with only the roads from towns keep allows.
func roadSource(keep func(from string) bool) *ns.MemSource {
	src := ns.NewMemSource().
		DeclareSchema("road", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Doc: "A one-way road between two towns."}).
		DeclareSchema("weight", ns.Schema{Arity: 2, Labels: []string{"town", "w"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	towns := []string{"a", "b", "c", "d", "e"}
	for i := range len(towns) - 1 {
		if keep(towns[i]) {
			src.Add("road", ns.Tuple{Vals: []ns.Value{ns.S(towns[i]), ns.S(towns[i+1])}, Cites: []string{fmt.Sprintf("map:%d", i+1)}})
		}
	}
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.N(3)}})
	return src
}

// pack is the toy host's pack: a few lines.
var pack = mcp.Pack{
	Pack: nl.Pack{
		About:    "Roads between towns. Each road is one way.",
		Card:     `# Towns are named in lower case, "a" to "e".`,
		Examples: []nl.Example{{Question: "where can a get to?", Program: `trip.reach("a", ?t) => ?t`}},
	},
	Name: "roads",
}

func dial(t *testing.T, p mcp.Pack, o mcp.Options) *testutil.TestClient {
	t.Helper()
	v, b := roads(t)
	if o.Base == nil {
		o.Base = func(context.Context) (*datalog.Base, error) { return b, nil }
	}
	srv, err := mcp.New(v, p, o)
	if err != nil {
		t.Fatal(err)
	}
	return testutil.NewTestClient(t, srv)
}

func call(t *testing.T, c *testutil.TestClient, tool string, args map[string]any) *core.ToolResult {
	t.Helper()
	res, err := c.Client.ToolCallFull(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("%s: a protocol error, not a tool error: %v", tool, err)
	}
	return res
}

// The client reads the instructions where the spec puts them, written from the pack, and a pack's own
// instructions replace them.
func TestTheInstructionsSayWhatTheServerIsFor(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	got := c.Client.Instructions
	for _, want := range []string{"Roads between towns.", "call schema", `"data"`, "# where can a get to?", `trip.reach("a", ?t) => ?t`, "explain"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ask tool") {
		t.Errorf("instructions mention ask without a model:\n%s", got)
	}
	if got != mcp.Instructions(pack, false) {
		t.Errorf("the client read other instructions than Instructions writes")
	}
	own := pack
	own.Instructions = "Use me for roads."
	if got := dial(t, own, mcp.Options{}).Client.Instructions; got != "Use me for roads." {
		t.Errorf("control: the pack's own instructions read %q", got)
	}
	if line := mcp.AgentLine(pack); line != "For questions about roads between towns, use the roads MCP server before searching files: it answers exactly from its graph. Call its schema tool, then query." {
		t.Errorf("agent line %q", line)
	}
	for _, tool := range c.ListTools() {
		if tool.Name != "explain" && !strings.Contains(tool.Description, "roads between towns") {
			t.Errorf("%s's description doesn't say what the data is: %s", tool.Name, tool.Description)
		}
	}
}

// query answers rows with the facts each was derived from, a page at a time.
func TestQueryAnswersAPageAtATime(t *testing.T) {
	c := dial(t, pack, mcp.Options{Limit: 2})
	res := call(t, c, "query", map[string]any{"program": `trip.reach("a", ?t) => ?t`})
	text := res.Content[0].Text
	if res.IsError || !strings.Contains(text, "columns: t\n1. b\n") || !strings.Contains(text, `road("a", "b")  [map:1]`) || !strings.Contains(text, `rows 1 to 2 of 4; continue with page_token "2"`) {
		t.Fatalf("first page:\n%s", text)
	}
	res = call(t, c, "query", map[string]any{"program": `trip.reach("a", ?t) => ?t`, "page_token": "2"})
	text = res.Content[0].Text
	if !strings.Contains(text, "3. d\n") || !strings.Contains(text, "4. e\n") || strings.Contains(text, "1. b") || !strings.Contains(text, "4 rows\n") || strings.Contains(text, "page_token") {
		t.Errorf("last page:\n%s", text)
	}
	res = call(t, c, "query", map[string]any{"program": `trip.reach("a", ?t) => ?t`, "limit": 1, "json": true})
	data, _ := json.Marshal(res.StructuredContent)
	var got struct {
		Columns   []string     `json:"columns"`
		Rows      [][]string   `json:"rows"`
		Witness   [][]struct{} `json:"witness"`
		Total     int          `json:"total"`
		PageToken string       `json:"page_token"`
	}
	if err := json.Unmarshal(data, &got); err != nil || len(got.Rows) != 1 || got.Rows[0][0] != "b" || got.Total != 4 || got.PageToken != "1" || len(got.Witness) != 1 {
		t.Errorf("json %s (%v)", data, err)
	}
	if res := call(t, c, "query", map[string]any{"program": `road("e", ?t) => ?t`}); res.Content[0].Text != "columns: t\nno rows\n" {
		t.Errorf("an empty answer: %q", res.Content[0].Text)
	}
}

// A program that doesn't parse, check or finish is a tool error an agent fixes it from.
func TestABrokenProgramIsAToolErrorWithItsFix(t *testing.T) {
	c := dial(t, pack, mcp.Options{MaxWork: 1000})
	for name, tc := range map[string]struct{ program, want string }{
		"unknown": {`reach("a", ?t) => ?t`, `fix: did you mean "trip.reach"?`},
		"parse":   {`road("a", => ?t`, "line 1, column"},
		"budget":  {`trip.reach(?a, ?b), trip.reach(?b, ?c) => ?a, ?c`, "raise max_work"},
	} {
		args := map[string]any{"program": tc.program}
		if name == "budget" {
			args["max_work"] = 5
		}
		res := call(t, c, "query", args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Errorf("%s: want a tool error containing %q, got %+v", name, tc.want, res.Content)
		}
	}
	res := call(t, c, "query", map[string]any{"program": `trip.reach(?a, ?b) => ?a, ?b`, "max_work": 1_000_000})
	if res.IsError {
		t.Errorf("control: max_work over the cap is capped, not refused: %s", res.Content[0].Text)
	}
	capped := call(t, dial(t, pack, mcp.Options{MaxWork: 5}), "query", map[string]any{"program": `trip.reach(?a, ?b) => ?a, ?b`, "max_work": 1_000_000})
	if !capped.IsError || !strings.Contains(capped.Content[0].Text, "budget of 5") || strings.Contains(capped.Content[0].Text, "raise max_work") {
		t.Errorf("a call can't raise the server's cap: %+v", capped.Content)
	}
}

func TestSchemaListsWhatAProgramCanCall(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	if root := c.ToolCall("schema", map[string]any{}); !strings.Contains(root, "road(from, to) # A one-way road") || !strings.Contains(root, "trip.reach(") {
		t.Errorf("root:\n%s", root)
	}
	if mod := c.ToolCall("schema", map[string]any{"path": "trip"}); !strings.HasPrefix(mod, "module trip\ntrip.reach(") || strings.Contains(mod, "road(") {
		t.Errorf("module:\n%s", mod)
	}
	if rel := c.ToolCall("schema", map[string]any{"path": "trip.reach"}); !strings.Contains(rel, "Every town a town gets to") || !strings.Contains(rel, "reach(?a, ?c) :- reach(?a, ?b), road(?b, ?c)") {
		t.Errorf("relation:\n%s", rel)
	}
	if data := c.ToolCall("schema", map[string]any{"path": "data"}); data != pack.Card {
		t.Errorf("data: %q", data)
	}
	if res := call(t, c, "schema", map[string]any{"path": "roads"}); !res.IsError {
		t.Errorf("an unknown path: %+v", res.Content)
	}
}

func TestExplainSaysWhyAnAnswerIsEmpty(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	text := c.ToolCall("explain", map[string]any{"program": `road("a", ?x), road(?x, "a") => ?x`})
	if !strings.HasPrefix(text, "0 rows\n") || !strings.Contains(text, `road(?x, "a") matched nothing`) || !strings.Contains(text, `road("b", "a")`) {
		t.Errorf("explain:\n%s", text)
	}
	if text := c.ToolCall("explain", map[string]any{"program": `road("a", ?x) => ?x`}); !strings.HasPrefix(text, "1 row\n") || strings.Contains(text, "matched nothing") {
		t.Errorf("control: an answer with rows:\n%s", text)
	}
}

// scripted is a Model replying with its replies in turn.
type scripted struct {
	name    string
	replies []string
	asked   int
}

func (s *scripted) Name() string { return s.name }

func (s *scripted) Complete(context.Context, nl.Prompt) (nl.Completion, error) {
	s.asked++
	if s.asked > len(s.replies) {
		return nl.Completion{}, errors.New("out of replies")
	}
	return nl.Completion{Text: s.replies[s.asked-1], TokensIn: 10, TokensOut: 5}, nil
}

// ask translates through the pack, whose Check refuses an answer the model then fixes, and mode picks
// the models.
func TestAskTranslatesThroughThePack(t *testing.T) {
	if names := toolNames(dial(t, pack, mcp.Options{})); strings.Contains(names, "ask") {
		t.Errorf("ask is offered without a model: %s", names)
	}
	checked := pack
	checked.Check = func(_ context.Context, p nl.Program) error {
		if len(p.Rows) > 1 {
			return fmt.Errorf("%d towns, but the question asks for the next one", len(p.Rows))
		}
		return nil
	}
	small := &scripted{name: "small", replies: []string{`trip.reach("a", ?t) => ?t`, `road("a", ?t) => ?t`}}
	big := &scripted{name: "big", replies: []string{`road("a", ?t) => ?t`}}
	c := dial(t, checked, mcp.Options{Model: small, Fallback: big})
	text := c.ToolCall("ask", map[string]any{"question": "which town is next after a?"})
	for _, want := range []string{"program:\n  road(\"a\", ?t) => ?t", "1. b\n", "1. small: answer", "2. small: accepted"} {
		if !strings.Contains(text, want) {
			t.Errorf("ask lacks %q:\n%s", want, text)
		}
	}
	if big.asked != 0 {
		t.Errorf("the fallback was asked")
	}
	big = &scripted{name: "big", replies: []string{`road("a", ?t) => ?t`}}
	small = &scripted{name: "small"}
	c = dial(t, checked, mcp.Options{Model: small, Fallback: big})
	if text := c.ToolCall("ask", map[string]any{"question": "which town is next after a?", "mode": "frontier"}); !strings.Contains(text, "1. big: accepted") || small.asked != 0 {
		t.Errorf("frontier asked the small model %d times:\n%s", small.asked, text)
	}
	small = &scripted{name: "small", replies: []string{`nowhere(?t) => ?t`}}
	big = &scripted{name: "big", replies: []string{`road("a", ?t) => ?t`}}
	c = dial(t, checked, mcp.Options{Model: small, Fallback: big, Limit: 5})
	res := call(t, c, "ask", map[string]any{"question": "which town is next after a?", "mode": "local"})
	if !res.IsError || big.asked != 0 || !strings.Contains(res.Content[0].Text, "small: unknown") {
		t.Errorf("local: fallback asked %d times, %+v", big.asked, res.Content)
	}
}

func toolNames(c *testutil.TestClient) string {
	var names []string
	for _, tool := range c.ListTools() {
		names = append(names, tool.Name)
	}
	return strings.Join(names, " ")
}

// Format writes the answer instead of the default table.
func TestAPackFormatsItsAnswers(t *testing.T) {
	formatted := pack
	formatted.Format = func(a mcp.Answer) string {
		return fmt.Sprintf("%d of %d towns from %s", a.Len, a.Table.Len(), a.Program)
	}
	c := dial(t, formatted, mcp.Options{})
	if text := c.ToolCall("query", map[string]any{"program": `trip.reach("a", ?t) => ?t`}); text != `4 of 4 towns from trip.reach("a", ?t) => ?t` {
		t.Errorf("formatted %q", text)
	}
}

func TestNewRefusesABrokenPack(t *testing.T) {
	v, b := roads(t)
	base := func(context.Context) (*datalog.Base, error) { return b, nil }
	broken := pack
	broken.Examples = []nl.Example{{Question: "q", Program: `reach("a", ?t) => ?t`}}
	if _, err := mcp.New(v, broken, mcp.Options{Base: base}); err == nil || !strings.Contains(err.Error(), "example 1") {
		t.Errorf("a broken example: %v", err)
	}
	if _, err := mcp.New(v, pack, mcp.Options{}); err == nil {
		t.Errorf("no Base: no error")
	}
	if _, err := mcp.New(v, pack, mcp.Options{Base: base}); err != nil {
		t.Errorf("control: %v", err)
	}
}

// A pack's Hint follows an empty answer from query, cypher and explain, so an agent sees what the data
// holds where its program matched nothing, with no sample asked. control: an answer with rows has none.
func TestAnEmptyAnswerCarriesThePacksHint(t *testing.T) {
	hinted := pack
	hinted.Hint = func(_ context.Context, why *datalog.EmptyReport) string {
		if why.Relation == "" {
			return ""
		}
		return fmt.Sprintf("hint: %s has no %q here", why.Relation, why.Args[len(why.Args)-1].S)
	}
	c := dial(t, hinted, mcp.Options{})
	program := `road("a", ?x), road(?x, "zz") => ?x`
	for _, tool := range []string{"query", "explain"} {
		if text := c.ToolCall(tool, map[string]any{"program": program}); !strings.Contains(text, `hint: road has no "zz" here`) || !strings.Contains(text, "matched nothing") {
			t.Errorf("%s:\n%s", tool, text)
		}
	}
	if text := c.ToolCall("query", map[string]any{"program": `road("a", ?x) => ?x`}); strings.Contains(text, "hint:") {
		t.Errorf("control: an answer with rows:\n%s", text)
	}
}
