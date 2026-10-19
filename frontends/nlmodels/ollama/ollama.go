// Package ollama is an nl.Model over an Ollama server's chat endpoint (#330). Ollama takes no GBNF
// grammar (its format field is a JSON schema, which doesn't fit a program's text), so a Prompt's
// Grammar isn't sent and the model writes freely; nl.Translate checks every draft either way, so
// what it costs is tries. A host wanting the grammar held runs the model under llama.cpp instead
// (package llamacpp).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/jaala/frontends/nl"
)

// A Client asks one Ollama server for one model. The zero value isn't usable; New fills in the
// defaults.
type Client struct {
	// URL is the server's base URL, such as http://localhost:11434.
	URL string
	// Model is the Ollama model to run, such as qwen2.5-coder:1.5b. Ollama requires one.
	Model string
	// MaxTokens caps a reply's length (Ollama's num_predict); 512 by default.
	MaxTokens int
	// Temperature is sent as is. The default 0 decodes greedily, so a Trace repeats, and an nl.Vote's
	// drafts agree by construction: a vote wants a temperature above 0, and each of its drafts is
	// seeded by its Draw.
	Temperature float64
	// HTTP sends the requests; http.DefaultClient by default.
	HTTP *http.Client
}

// New is a Client of the Ollama server at url running model, replying with at most 512 tokens,
// greedily.
func New(url, model string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Model: model, MaxTokens: 512, HTTP: http.DefaultClient}
}

// Name is "ollama:" and the Model.
func (c *Client) Name() string { return "ollama:" + c.Model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Options  struct {
		Temperature float64 `json:"temperature"`
		NumPredict  int     `json:"num_predict,omitempty"`
		Seed        int     `json:"seed,omitempty"`
	} `json:"options"`
}

type response struct {
	Message         message `json:"message"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

// Complete sends p as a chat: its Prefix as the system message and its Turns as user and assistant
// messages. Ollama reports no cached tokens, so TokensCached stays 0. A reply with a status other than
// 200 is an error carrying the start of the server's body.
func (c *Client) Complete(ctx context.Context, p nl.Prompt) (nl.Completion, error) {
	if c.Model == "" {
		return nl.Completion{}, fmt.Errorf("ollama: no model named")
	}
	req := request{Model: c.Model}
	req.Options.Temperature, req.Options.NumPredict, req.Options.Seed = c.Temperature, c.MaxTokens, p.Draw
	if p.Prefix != "" {
		req.Messages = append(req.Messages, message{"system", p.Prefix})
	}
	for _, t := range p.Turns {
		role := "user"
		if t.Role == nl.RoleModel {
			role = "assistant"
		}
		req.Messages = append(req.Messages, message{role, t.Text})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nl.Completion{}, err
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nl.Completion{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return nl.Completion{}, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nl.Completion{}, fmt.Errorf("ollama: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nl.Completion{}, fmt.Errorf("ollama: %w", err)
	}
	return nl.Completion{Text: res.Message.Content, TokensIn: res.PromptEvalCount, TokensOut: res.EvalCount}, nil
}
