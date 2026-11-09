package mcp_test

import (
	"context"
	"fmt"
	"time"

	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// testModel is a scripted model the tests' Translator asks: what nl.Model was until #502, when nl
// stopped asking models. This module can't import nlmodels, which brings chakra.
type testModel interface {
	Name() string
	Complete(ctx context.Context, p testPrompt) (testReply, error)
}

// testPrompt is what a testModel is asked: the session's prefix, the conversation and the grammar.
type testPrompt struct {
	Prefix  string
	Turns   []nl.Turn
	Grammar string
}

// testReply is a testModel's reply and what it cost.
type testReply struct {
	Text                string
	TokensIn, TokensOut int
}

// sessions is a Translator for the ask tool's tests: an nl.Session judged turn by turn, its models
// the tests' scripted ones, the fallback in a fresh conversation after the model. It has none of
// nlmodels' translator's votes, tools or budget, which the ask tool doesn't set; this module can't
// import nlmodels, which brings chakra.
type sessions struct {
	v               *ns.Vocabulary
	model, fallback testModel
}

// translating is a Translator over models: the model, then the fallback when one is given. The dial
// helpers fill in the vocabulary they build (withVocabulary).
func translating(models ...testModel) *sessions {
	t := &sessions{model: models[0]}
	if len(models) > 1 {
		t.fallback = models[1]
	}
	return t
}

// withVocabulary gives o's sessions Translator, if it has one, the vocabulary v.
func withVocabulary(o mcp.Options, v *ns.Vocabulary) {
	switch t := o.Translator.(type) {
	case *sessions:
		t.v = v
	case *tiering:
		t.sessions.v = v
	}
}

func (t *sessions) HasFallback() bool { return t.fallback != nil }

func (t *sessions) Translate(ctx context.Context, question string, o nl.Options, model, fallback bool) (nl.Program, nl.Trace, error) {
	p, tr, err := t.translate(ctx, question, o, model, fallback)
	return o.Abstain.Judge(p, tr, err)
}

func (t *sessions) translate(ctx context.Context, question string, o nl.Options, model, fallback bool) (nl.Program, nl.Trace, error) {
	var tr nl.Trace
	s, err := nl.NewSession(ctx, question, t.v, nil, o)
	if err != nil {
		return nl.Program{}, tr, err
	}
	var models []testModel
	if model {
		models = append(models, t.model)
	}
	if fallback && t.fallback != nil {
		models = append(models, t.fallback)
	}
	var last error
	for k, m := range models {
		c := s.NewConversation(last, k == len(models)-1)
		turns := []nl.Turn{{Role: nl.RoleUser, Text: c.Ask()}}
		for {
			start := time.Now()
			reply, err := m.Complete(ctx, testPrompt{Prefix: s.Prefix(), Turns: turns, Grammar: s.Grammar()})
			if err != nil {
				tr.Tries = append(tr.Tries, nl.Try{Model: m.Name(), Err: err, Kind: nl.KindModel, Latency: time.Since(start)})
				last = fmt.Errorf("model %s: %w", m.Name(), err)
				break
			}
			vd := c.Judge(ctx, nl.Try{Model: m.Name(), Text: reply.Text, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut, Latency: time.Since(start)})
			tr.Tries = append(tr.Tries, vd.Tries...)
			if vd.Program != nil {
				return *vd.Program, tr, nil
			}
			if vd.Stop != nil {
				last = vd.Stop
				break
			}
			turns = append(turns, nl.Turn{Role: nl.RoleModel, Text: vd.Tries[len(vd.Tries)-1].Text}, nl.Turn{Role: nl.RoleUser, Text: vd.Continue})
		}
	}
	return nl.Program{}, tr, fmt.Errorf("%w after %d tries: %w", nl.ErrNoProgram, len(tr.Tries), last)
}
