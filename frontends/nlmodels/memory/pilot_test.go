//go:build eval_llm

package memory

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/eval/longmemeval"
	"github.com/panyam/jaala/frontends/nlmodels/translator"
)

// TestPilot is #415's pilot: LongMemEval's long-context arm against jaala's one-shot arm, on the same
// model, graded by the benchmark's own judge. It costs money, so it runs only when asked, never in CI:
//
//	LONGMEMEVAL_DATA_PATH=.../longmemeval_oracle.json JAALA_PILOT=1 \
//	  go test -tags eval_llm ./memory -run Pilot -v -timeout 60m
//
// The key comes from OPENAI_API_KEY. JAALA_PILOT_MODEL picks the model (gpt-5.5), JAALA_PILOT_PER_TYPE
// how many questions of each of the six question types (2), the first in the file, and JAALA_PILOT_PRICE_IN and _OUT its dollars per million tokens, for cost
// per correct answer. JAALA_PILOT_JUDGE is the grading model, upstream's gpt-4o-2024-08-06 by default,
// whatever model answers (see judge). It reports and never gates: the numbers go on #437.
func TestPilot(t *testing.T) {
	if os.Getenv("JAALA_PILOT") == "" || os.Getenv("OPENAI_API_KEY") == "" || os.Getenv(longmemeval.DataPathEnv) == "" {
		t.Skip("JAALA_PILOT, OPENAI_API_KEY or " + longmemeval.DataPathEnv + " is not set")
	}
	model := envOr("JAALA_PILOT_MODEL", "gpt-5.5")
	perType, _ := strconv.Atoi(envOr("JAALA_PILOT_PER_TYPE", "2"))
	p, err := agent.NewResponsesProvider(agent.ResponsesConfig{BaseURL: "https://api.openai.com/v1", Model: model,
		APIKey: os.Getenv("OPENAI_API_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	var prices eval.Prices
	in, _ := strconv.ParseFloat(os.Getenv("JAALA_PILOT_PRICE_IN"), 64)
	out, _ := strconv.ParseFloat(os.Getenv("JAALA_PILOT_PRICE_OUT"), 64)
	if in > 0 || out > 0 {
		prices = eval.Prices{model: {InputPerM: in, OutputPerM: out}}
	}
	judgeModel := envOr("JAALA_PILOT_JUDGE", "gpt-4o-2024-08-06")
	jp, err := agent.NewResponsesProvider(agent.ResponsesConfig{BaseURL: "https://api.openai.com/v1", Model: judgeModel,
		APIKey: os.Getenv("OPENAI_API_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	// A reasoning model spends a plain role's MaxTokens on thinking and replies empty, so both roles reason.
	role := translator.Role{Provider: p, Label: model, Reasoning: true}
	cfg := Config{Extract: role, Ask: role}
	arms := []struct {
		name    string
		adapter longmemeval.DatasetAdapter
	}{
		{"long context", longmemeval.DatasetAdapter{LongContext: true, OfficialJudge: true}},
		{"jaala one shot", longmemeval.DatasetAdapter{Arm: OneShotArm(cfg), OfficialJudge: true}},
	}
	for _, arm := range arms {
		suite := perTypeSuite(t, agent.RunnerConfig{Provider: p}, arm.adapter, perType)
		judgedBy(suite, judge{jp})
		suite.ModelName, suite.Prices = model, prices
		rep := suite.Run(context.Background())
		t.Logf("== %s on %s, judged by %s: %d/%d\n%s", arm.name, model, judgeModel, rep.Passed, rep.Total, byAbility(rep))
		setup, turns := tokens(rep)
		t.Logf("tokens in/out: setup %d/%d, answering %d/%d", setup.InputTokens, setup.OutputTokens, turns.InputTokens, turns.OutputTokens)
		if c, ok := rep.CostPerCorrect(); ok {
			t.Logf("cost per correct answer: $%.4f", c)
		}
		for _, c := range rep.Cases {
			if !c.Pass {
				var why []string
				for _, sc := range c.Scores {
					why = append(why, sc.Name+": "+sc.Detail)
				}
				t.Logf("  failed %s: %s %s", c.Case, c.RunErr, strings.Join(why, "; "))
			}
		}
	}
}

// judge is the grading model with its output cap raised to 16 tokens, the least OpenAI's Responses API
// takes. chakra's official scorer asks for 10, which the API refuses, and it grades with the suite's
// own provider, so judgedBy hands each case's scorers this one instead.
type judge struct{ agent.Provider }

func (j judge) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	req.MaxTokens = max(req.MaxTokens, 16)
	return j.Provider.Generate(ctx, req)
}

// judgedBy has every case of s graded with p, whatever provider the suite answers with.
func judgedBy(s eval.Suite, p agent.Provider) {
	for i := range s.Cases {
		if score := s.Cases[i].Scorers; score != nil {
			s.Cases[i].Scorers = func(agent.Provider) []eval.Scorer { return score(p) }
		}
	}
}

// tokens adds up a report's setup usage (ingestion, and arm 3's question) and its answering usage,
// over every model.
func tokens(rep eval.SuiteReport) (setup, turns agent.Usage) {
	for _, c := range rep.Cases {
		for _, u := range c.SetupUsage {
			setup.InputTokens += u.InputTokens
			setup.OutputTokens += u.OutputTokens
		}
		for _, u := range c.Usage {
			turns.InputTokens += u.InputTokens
			turns.OutputTokens += u.OutputTokens
		}
	}
	return setup, turns
}

// questionTypes are LongMemEval's six question types.
var questionTypes = []string{"single-session-user", "single-session-assistant", "single-session-preference",
	"multi-session", "knowledge-update", "temporal-reasoning"}

// perTypeSuite is a's first n questions of each question type, as one suite. The file opens with
// 133 temporal-reasoning questions, so a plain Limit would test one ability.
func perTypeSuite(t *testing.T, cfg agent.RunnerConfig, a longmemeval.DatasetAdapter, n int) eval.Suite {
	t.Helper()
	var all eval.Suite
	for _, qt := range questionTypes {
		a.Types, a.Limit = func(got string) bool { return got == qt }, n
		s, ok, err := eval.LoadSuite(cfg, a)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Skip("the dataset gave no cases")
		}
		if all.Cases == nil {
			all = s
			continue
		}
		all.Cases = append(all.Cases, s.Cases...)
	}
	return all
}

// byAbility tallies a report's cases by question type, the part of a case's name before its slash.
func byAbility(rep eval.SuiteReport) string {
	pass, total := map[string]int{}, map[string]int{}
	for _, c := range rep.Cases {
		ability, _, _ := strings.Cut(c.Case, "/")
		total[ability]++
		if c.Pass {
			pass[ability]++
		}
	}
	names := make([]string, 0, len(total))
	for n := range total {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "  %-28s %d/%d\n", n, pass[n], total[n])
	}
	return b.String()
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
