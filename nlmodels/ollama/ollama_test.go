package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/jaala/nl"
)

func TestCompleteSendsTheChatWithoutTheGrammar(t *testing.T) {
	var raw map[string]any
	var req request
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("asked %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &raw)
		json.Unmarshal(b, &req)
		io.WriteString(w, `{"message":{"role":"assistant","content":"edge(?a, ?b) => ?a"},"prompt_eval_count":80,"eval_count":9}`)
	}))
	defer s.Close()
	p := nl.Prompt{Prefix: "the card", Turns: []nl.Turn{{Role: nl.RoleUser, Text: "q"}, {Role: nl.RoleModel, Text: "draft"}}, Grammar: `root ::= "x"`}
	c := New(s.URL, "qwen2.5-coder:1.5b")
	out, err := c.Complete(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if out != (nl.Completion{Text: "edge(?a, ?b) => ?a", TokensIn: 80, TokensOut: 9}) {
		t.Errorf("completion %+v", out)
	}
	want := []message{{"system", "the card"}, {"user", "q"}, {"assistant", "draft"}}
	if len(req.Messages) != 3 || req.Messages[0] != want[0] || req.Messages[1] != want[1] || req.Messages[2] != want[2] {
		t.Errorf("messages %+v", req.Messages)
	}
	if strings.Contains(string(mustJSON(raw)), "root ::=") || req.Model != "qwen2.5-coder:1.5b" || req.Stream || req.Options.NumPredict != 512 {
		t.Errorf("request %s", mustJSON(raw))
	}
	if c.Name() != "ollama:qwen2.5-coder:1.5b" {
		t.Errorf("name %q", c.Name())
	}
}

func TestCompleteRefusesNoModelAndReportsErrors(t *testing.T) {
	asked := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"model \"x\" not found"}`)
	}))
	defer s.Close()
	if _, err := New(s.URL, "").Complete(context.Background(), nl.Prompt{}); err == nil || !strings.Contains(err.Error(), "no model") || asked != 0 {
		t.Fatalf("no model named: %v, after %d requests", err, asked)
	}
	_, err := New(s.URL, "x").Complete(context.Background(), nl.Prompt{})
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err %v", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
