package guide_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/llamacpp"
)

// llamaServer stands in for llama.cpp's llama-server: it answers every chat
// with one program, and says what the client sent it.
func llamaServer(program string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Grammar     string `json:"grammar"`
			CachePrompt bool   `json:"cache_prompt"`
			MaxTokens   int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var roles []string
		for _, m := range req.Messages {
			roles = append(roles, m.Role)
		}
		fmt.Printf("%s %s: messages %s, grammar sent %v, cache_prompt %v, max_tokens %d\n",
			r.Method, r.URL.Path, strings.Join(roles, "+"), req.Grammar != "", req.CachePrompt, req.MaxTokens)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": program}}},
			"usage":   map[string]int{"prompt_tokens": 900, "completion_tokens": 12},
		})
	}))
}

// llamacpp.New is an nl.Model over a llama-server already running: it sends
// the card as a cached system prefix and the vocabulary's grammar with every
// question.
func Example_llamaServer() {
	srv := llamaServer(`imports("app", ?d) => ?d`)
	defer srv.Close()
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	m := llamacpp.New(srv.URL)
	p, tr, err := nl.Translate(context.Background(), "what does app import?", v, nil, m,
		nl.Options{Base: datalog.MustBase(v, source())})
	if err != nil {
		fmt.Println(err)
		return
	}
	t := tr.Tries[0]
	fmt.Printf("%s wrote %s, %d tokens in, %d out\n", t.Model, p.Text, t.TokensIn, t.TokensOut)
	fmt.Println("answer:", p.Rows[0].Bind["d"].S)
	// Output:
	// POST /v1/chat/completions: messages system+user, grammar sent true, cache_prompt true, max_tokens 512
	// llama.cpp wrote imports("app", ?d) => ?d, 900 tokens in, 12 out
	// answer: api
}
