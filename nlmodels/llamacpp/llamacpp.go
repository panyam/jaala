// Package llamacpp is an nl.Model over llama.cpp's llama-server (#330): it sends each nl.Prompt to the
// server's OpenAI-compatible chat endpoint with the prompt's grammar, so the model can only write text
// that parses, and with prompt caching on, so the card in the prompt's prefix is processed once. It
// runs no model; a host starts llama-server with the model it wants.
package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/jaala/nl"
)

// A Client asks one llama-server. The zero value isn't usable; New fills in the defaults.
type Client struct {
	// URL is the server's base URL, such as http://localhost:8080.
	URL string
	// Model names the model in the Trace, and is sent as the request's model, which llama-server
	// serving one model ignores.
	Model string
	// MaxTokens caps a reply's length; a program rarely needs more than the default 512.
	MaxTokens int
	// Temperature is sent as is. The default 0 decodes greedily, so a prompt gets the same draft every
	// time, which is what makes a Trace repeatable.
	Temperature float64
	// HTTP sends the requests; http.DefaultClient by default.
	HTTP *http.Client
}

// New is a Client of the llama-server at url, replying with at most 512 tokens, greedily.
func New(url string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), MaxTokens: 512, HTTP: http.DefaultClient}
}

// Name is "llama.cpp", or "llama.cpp:" and the Model when one is set.
func (c *Client) Name() string {
	if c.Model == "" {
		return "llama.cpp"
	}
	return "llama.cpp:" + c.Model
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model       string    `json:"model,omitempty"`
	Messages    []message `json:"messages"`
	Grammar     string    `json:"grammar,omitempty"`
	CachePrompt bool      `json:"cache_prompt"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type response struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Timings struct {
		CacheN int `json:"cache_n"`
	} `json:"timings"`
}

// Complete sends p as a chat: its Prefix as the system message, its Turns as user and assistant
// messages, and its Grammar, when it has one, as the grammar the reply is decoded under. The tokens it
// reports are the server's usage; the cached ones are its prompt_tokens_details, or failing that the
// prompt tokens its cache answered (timings.cache_n). A reply with a status other than 200 is an
// error carrying the start of the server's body, which says why (an unparseable grammar, say).
func (c *Client) Complete(ctx context.Context, p nl.Prompt) (nl.Completion, error) {
	req := request{Model: c.Model, Grammar: p.Grammar, CachePrompt: true, MaxTokens: c.MaxTokens, Temperature: c.Temperature}
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
	var res response
	if err := post(ctx, c.HTTP, c.URL+"/v1/chat/completions", body, &res); err != nil {
		return nl.Completion{}, fmt.Errorf("llamacpp: %w", err)
	}
	if len(res.Choices) == 0 {
		return nl.Completion{}, fmt.Errorf("llamacpp: the server replied with no choices")
	}
	cached := res.Usage.PromptTokensDetails.CachedTokens
	if cached == 0 {
		cached = res.Timings.CacheN
	}
	return nl.Completion{
		Text:         res.Choices[0].Message.Content,
		TokensIn:     res.Usage.PromptTokens,
		TokensOut:    res.Usage.CompletionTokens,
		TokensCached: cached,
	}, nil
}

// post sends body as JSON and decodes a 200 reply into out.
func post(ctx context.Context, client *http.Client, url string, body []byte, out any) error {
	if client == nil {
		client = http.DefaultClient
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
