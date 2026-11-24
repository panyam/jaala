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
// The key comes from OPENAI_API_KEY. JAALA_PILOT_MODEL picks the model (gpt-5.5), JAALA_PILOT_LIMIT
// how many questions (10), and JAALA_PILOT_PRICE_IN and _OUT its dollars per million tokens, for cost
// per correct answer. It reports and never gates: the numbers go on #437.
func TestPilot(t *testing.T) {
	if os.Getenv("JAALA_PILOT") == "" || os.Getenv("OPENAI_API_KEY") == "" || os.Getenv(longmemeval.DataPathEnv) == "" {
		t.Skip("JAALA_PILOT, OPENAI_API_KEY or " + longmemeval.DataPathEnv + " is not set")
	}
	model := envOr("JAALA_PILOT_MODEL", "gpt-5.5")
	limit, _ := strconv.Atoi(envOr("JAALA_PILOT_LIMIT", "10"))
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
	cfg := Config{Extract: translator.Role{Provider: p, Label: model}, Ask: translator.Role{Provider: p, Label: model}}
	arms := []struct {
		name    string
		adapter longmemeval.DatasetAdapter
	}{
		{"long context", longmemeval.DatasetAdapter{LongContext: true, Limit: limit, OfficialJudge: true}},
		{"jaala one shot", longmemeval.DatasetAdapter{Arm: OneShotArm(cfg), Limit: limit, OfficialJudge: true}},
	}
	for _, arm := range arms {
		suite, ok, err := eval.LoadSuite(agent.RunnerConfig{Provider: p}, arm.adapter)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Skip("the dataset gave no cases")
		}
		suite.ModelName, suite.Prices = model, prices
		rep := suite.Run(context.Background())
		t.Logf("== %s on %s: %d/%d\n%s", arm.name, model, rep.Passed, rep.Total, byAbility(rep))
		if c, ok := rep.CostPerCorrect(); ok {
			t.Logf("cost per correct answer: $%.4f", c)
		}
		for _, c := range rep.Cases {
			if !c.Pass {
				t.Logf("  failed %s: %s", c.Case, c.RunErr)
			}
		}
	}
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
