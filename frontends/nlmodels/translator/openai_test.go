package translator_test

import (
	"context"
	"os"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/frontends/nlmodels/translator"
)

// TestEvalOnOpenAI runs TestEval's questions on a frontier model through chakra's Responses
// provider (#549), as the translator arm of panyam/declaire#300's first step does. It costs money, so
// it runs only when asked, never in CI:
//
//	JAALA_OPENAI_LIVE=1 go test ./translator -run EvalOnOpenAI -v
//
// The key comes from OPENAI_API_KEY. JAALA_OPENAI_MODEL picks the model (gpt-5.5), JAALA_OPENAI_EFFORT
// its reasoning effort (medium), and JAALA_OPENAI_REASONING=0 asks it as a plain role, capped at
// MaxTokens, to show what that cap does. As a Reasoning role no call may reach its cap.
func TestEvalOnOpenAI(t *testing.T) {
	if os.Getenv("JAALA_OPENAI_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("JAALA_OPENAI_LIVE or OPENAI_API_KEY is not set")
	}
	model, effort := os.Getenv("JAALA_OPENAI_MODEL"), os.Getenv("JAALA_OPENAI_EFFORT")
	if model == "" {
		model = "gpt-5.5"
	}
	if effort == "" {
		effort = "medium"
	}
	reasoning := os.Getenv("JAALA_OPENAI_REASONING") != "0"
	p, err := agent.NewResponsesProvider(agent.ResponsesConfig{BaseURL: "https://api.openai.com/v1", Model: model,
		APIKey: os.Getenv("OPENAI_API_KEY"), ReasoningEffort: effort})
	if err != nil {
		t.Fatal(err)
	}
	v, b := depsBase(t)
	tr, err := translator.New(v, nil, translator.Config{Model: translator.Role{Provider: p, Label: model, Reasoning: reasoning}, Base: b})
	if err != nil {
		t.Fatal(err)
	}
	rep := nl.Eval(context.Background(), evalCases, b, tr.Ask)
	s := rep.Summary(nil)
	t.Logf("%s at %s effort, reasoning role %v\n%s", model, effort, reasoning, rep.String())
	t.Logf("pass %d, wrong %d, abstain %d; truncated calls %d; %d calls, %.0fs", s.Pass, s.Wrong, s.Abstain, rep.Truncated, s.Cost.Calls, s.Cost.Seconds)
	for _, r := range rep.Cases {
		if !r.Pass {
			t.Logf("%s: got %v, want %v; program %q; err %v", r.Name, r.Got, r.Want, r.Program.Text, r.Err)
		}
	}
	if reasoning && rep.Truncated > 0 {
		t.Errorf("%d calls reached the %d-token cap", rep.Truncated, translator.ReasoningMaxTokens)
	}
}
