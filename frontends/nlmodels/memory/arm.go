package memory

import (
	"context"
	"fmt"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/eval/longmemeval"
)

// OneShotArm is LongMemEval's one-shot jaala arm, for DatasetAdapter.Arm. For each instance it builds
// a fresh Memory, ingests every haystack session in order, has the Ask model write one program for
// the question, and puts that program's cited rows in the agent's instructions with the question's
// date. The agent then phrases them, and says it doesn't know when they're empty.
//
// Setup's usage is the extraction's and the Ask model's together, since both run before the agent's
// turn: chakra reports it as the case's SetupUsage. A failed ingestion fails the case. A question no
// program answers doesn't: the agent is told nothing answered, so an abstention question can pass.
func OneShotArm(cfg Config) func(context.Context, longmemeval.Instance) (eval.Setup, error) {
	return func(ctx context.Context, inst longmemeval.Instance) (eval.Setup, error) {
		m, err := New(cfg)
		if err != nil {
			return eval.Setup{}, err
		}
		var spent map[string]agent.Usage
		for _, s := range Sessions(inst) {
			u, err := m.Ingest(ctx, s)
			spent = addUsage(spent, u)
			if err != nil {
				return eval.Setup{Usage: spent}, fmt.Errorf("longmemeval %s: %w", inst.QuestionID, err)
			}
		}
		a, u, err := m.Ask(ctx, inst.Question)
		spent = addUsage(spent, u)
		if err != nil {
			return eval.Setup{Instructions: fmt.Sprintf(m.prompts.NoAnswer, inst.QuestionDate, err), Usage: spent}, nil
		}
		return eval.Setup{Instructions: fmt.Sprintf(m.prompts.Answer, inst.QuestionDate, a), Usage: spent}, nil
	}
}

// Sessions lists an instance's haystack as dated sessions in the dataset's order, as chakra's
// MemoryArm ingests them: a session without an id is "session-<index>".
func Sessions(inst longmemeval.Instance) []longmemeval.Session {
	out := make([]longmemeval.Session, len(inst.HaystackSessions))
	for i, turns := range inst.HaystackSessions {
		s := longmemeval.Session{ID: fmt.Sprintf("session-%d", i), Turns: turns}
		if i < len(inst.HaystackSessionIDs) && inst.HaystackSessionIDs[i] != "" {
			s.ID = inst.HaystackSessionIDs[i]
		}
		if i < len(inst.HaystackDates) {
			s.Date = inst.HaystackDates[i]
		}
		out[i] = s
	}
	return out
}
