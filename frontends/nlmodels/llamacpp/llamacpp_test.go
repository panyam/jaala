package llamacpp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/frontends/nl"
)

// server answers as llama-server would, recording each request.
func server(t *testing.T, status int, reply string) (*httptest.Server, *[]request) {
	t.Helper()
	var got []request
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("asked %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("not JSON: %s", b)
		}
		got = append(got, req)
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

var prompt = nl.Prompt{
	Prefix:  "the card",
	Turns:   []nl.Turn{{Role: nl.RoleUser, Text: "q"}, {Role: nl.RoleModel, Text: "draft"}, {Role: nl.RoleUser, Text: "fix it"}},
	Grammar: `root ::= "x"`,
}

func TestCompleteSendsTheChatAndTheGrammar(t *testing.T) {
	s, got := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"edge(?a, ?b) => ?a"}}],
		"usage":{"prompt_tokens":120,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":100}}}`)
	c := New(s.URL + "/")
	c.Model = "qwen"
	out, err := c.Complete(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	if out != (nl.Completion{Text: "edge(?a, ?b) => ?a", TokensIn: 120, TokensOut: 9, TokensCached: 100}) {
		t.Errorf("completion %+v", out)
	}
	req := (*got)[0]
	want := []message{{"system", "the card"}, {"user", "q"}, {"assistant", "draft"}, {"user", "fix it"}}
	if len(req.Messages) != len(want) {
		t.Fatalf("messages %+v", req.Messages)
	}
	for i := range want {
		if req.Messages[i] != want[i] {
			t.Errorf("message %d: %+v, want %+v", i, req.Messages[i], want[i])
		}
	}
	if req.Grammar != prompt.Grammar || !req.CachePrompt || req.Stream || req.MaxTokens != 512 || req.Temperature != 0 || req.Model != "qwen" {
		t.Errorf("request %+v", req)
	}
	if c.Name() != "llama.cpp:qwen" || New("x").Name() != "llama.cpp" {
		t.Errorf("names %q %q", c.Name(), New("x").Name())
	}
}

func TestCompleteSendsNoGrammarWhenThePromptHasNone(t *testing.T) {
	s, _ := server(t, 200, `{"choices":[{"message":{"content":"x"}}]}`)
	var raw map[string]any
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&raw)
		io.WriteString(w, `{"choices":[{"message":{"content":"x"}}],"timings":{"cache_n":7}}`)
	})
	p := prompt
	p.Grammar = ""
	out, err := New(s.URL).Complete(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := raw["grammar"]; sent || out.TokensCached != 7 {
		t.Errorf("grammar sent: %v, cached %d (want the timings' 7)", sent, out.TokensCached)
	}
}

func TestCompleteReportsWhatTheServerRefused(t *testing.T) {
	s, _ := server(t, 400, `{"error":{"message":"failed to parse grammar"}}`)
	_, err := New(s.URL).Complete(context.Background(), prompt)
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "failed to parse grammar") {
		t.Fatalf("err %v", err)
	}
	s2, _ := server(t, 200, `{"choices":[]}`)
	if _, err := New(s2.URL).Complete(context.Background(), prompt); err == nil {
		t.Fatal("no choices answered without an error")
	}
}

func TestCompleteStopsWhenCancelled(t *testing.T) {
	block := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := New(s.URL).Complete(ctx, prompt); err == nil || ctx.Err() == nil {
		t.Fatalf("err %v", err)
	}
}
