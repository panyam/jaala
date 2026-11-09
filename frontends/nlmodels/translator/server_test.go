package translator

import (
	"context"
	"os"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/ns"
)

// A real llama-server keeps the Runner loop's answers to nl's grammar (#496), which chakra sends on
// each answering step as ResponseGuided. ./selfcheck.sh runs it beside chakra's own server test, on
// llama.cpp's 1 MB test model:
//
//	llama-server -m ~/.cache/jaala/stories260K.gguf --port 18089 --chat-template chatml
//	JAALA_LLAMACPP_URL=http://127.0.0.1:18089 go test ./translator -run RealServer -v
//
// The test model writes nonsense, so every answer is refused and its tries run out; each refused
// answer must still be text the grammar accepts, as a prefix when the model was cut off. The role
// sets every kind of penalty (#517), which a server refusing them fails as a model error.
func TestARealServerHoldsTheRunnersAnswersToTheGrammar(t *testing.T) {
	url := os.Getenv("JAALA_LLAMACPP_URL")
	if url == "" {
		t.Skip("JAALA_LLAMACPP_URL is not set")
	}
	// No stdlib, so the card fits the test model's 2048-token context, which llama-server won't raise.
	src := ns.NewMemSource().Declare("edge", "from", "to")
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	b := datalog.MustBase(v, src)
	text, err := nl.GrammarFor(v)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := gbnf.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	p, err := agent.NewLlamaCppProvider(agent.LlamaCppConfig{BaseURL: url + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	warm, rep, freq, pres, dry, last, allowed := 1.2, 1.1, 0.1, 0.1, 0.8, 64, 2
	pen := &agent.SamplingPenalties{Repeat: &rep, RepeatLastN: &last, Frequency: &freq, Presence: &pres, DRYMultiplier: &dry, DRYAllowedLength: &allowed}
	tr, err := New(v, nil, Config{Model: Role{Provider: p, Temperature: &warm, MaxTokens: 80, Penalties: pen}, Base: b,
		Options: func(o *nl.Options) { o.MaxTries = 3 }})
	if err != nil {
		t.Fatal(err)
	}
	_, trace, _ := tr.Ask(context.Background(), "what calls what?")
	answered := 0
	for _, try := range trace.Tries {
		if try.Kind == nl.KindModel {
			t.Fatalf("the server refused a call: %v", try.Err)
		}
		if try.Kind != nl.KindRepeat {
			answered++
		}
		if !ours.AcceptsPrefix(try.Text) {
			t.Errorf("an answer leaves the grammar as nl reads it: %q", try.Text)
		}
	}
	if answered == 0 {
		t.Fatalf("control: no answer was judged: %+v", trace.Tries)
	}
	if !t.Failed() {
		t.Logf("%d answers judged, all inside the grammar", answered)
	}
}

// A real llama-server holds a TextActions role's replies to the action grammar (#509), which a
// request stage switches to the draft's once the steps run out (chakra's StepView.Guided). Every reply
// the loop read, an action or an answer, must be text the action grammar accepts, as a prefix when
// the model was cut off; the draft grammar is a sublanguage of it.
func TestARealServerHoldsATextRolesRepliesToTheActionGrammar(t *testing.T) {
	url := os.Getenv("JAALA_LLAMACPP_URL")
	if url == "" {
		t.Skip("JAALA_LLAMACPP_URL is not set")
	}
	src := ns.NewMemSource().Declare("edge", "from", "to")
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	b := datalog.MustBase(v, src)
	acts := &nl.Actions{Steps: 2, Sample: 2}
	s, err := nl.NewSession(context.Background(), "what calls what?", v, nil, nl.Options{Actions: acts})
	if err != nil {
		t.Fatal(err)
	}
	_, text := s.TextActions()
	ours, err := gbnf.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	p, err := agent.NewLlamaCppProvider(agent.LlamaCppConfig{BaseURL: url + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	warm := 1.2
	tr, err := New(v, nil, Config{Model: Role{Provider: p, Temperature: &warm, MaxTokens: 80, TextActions: true}, Base: b, Actions: acts,
		Options: func(o *nl.Options) { o.MaxTries = 3 }})
	if err != nil {
		t.Fatal(err)
	}
	_, trace, _ := tr.Ask(context.Background(), "what calls what?")
	read := 0
	for _, st := range trace.Steps {
		read++
		if !ours.AcceptsPrefix(st.Text) {
			t.Errorf("an action leaves the action grammar: %q", st.Text)
		}
	}
	for _, try := range trace.Tries {
		if try.Kind == nl.KindModel {
			t.Fatalf("the server refused a call: %v", try.Err)
		}
		if try.Kind != nl.KindRepeat {
			read++
		}
		if !ours.AcceptsPrefix(try.Text) {
			t.Errorf("an answer leaves the action grammar: %q", try.Text)
		}
	}
	if read == 0 {
		t.Fatalf("control: no reply was read: %+v", trace)
	}
	if !t.Failed() {
		t.Logf("%d steps and %d tries, all inside the action grammar", len(trace.Steps), len(trace.Tries))
	}
}

// A real llama-server keeps Extract's replies to ExtractGrammar (#502), on the same test model: each
// refused reply is still lines the grammar accepts, as a prefix when the model was cut off.
func TestARealServerHoldsExtractsRepliesToItsGrammar(t *testing.T) {
	url := os.Getenv("JAALA_LLAMACPP_URL")
	if url == "" {
		t.Skip("JAALA_LLAMACPP_URL is not set")
	}
	src := ns.NewMemSource().Declare("edge", "from", "to")
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	text, err := nl.ExtractGrammar(v)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := gbnf.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	p, err := agent.NewLlamaCppProvider(agent.LlamaCppConfig{BaseURL: url + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	warm := 1.2
	tr, err := New(v, nil, Config{Model: Role{Provider: p, Temperature: &warm, MaxTokens: 80}})
	if err != nil {
		t.Fatal(err)
	}
	_, trace, _ := tr.Extract(context.Background(), "a links to b.", nl.ExtractOptions{MaxTries: 3})
	read := 0
	for _, try := range trace.Tries {
		if try.Kind == nl.KindModel {
			t.Fatalf("the server refused a call: %v", try.Err)
		}
		if try.Kind != nl.KindRepeat {
			read++
		}
		if !ours.AcceptsPrefix(try.Text) {
			t.Errorf("a reply leaves the extraction grammar as nl reads it: %q", try.Text)
		}
	}
	if read == 0 {
		t.Fatalf("control: no reply was read: %+v", trace.Tries)
	}
	if !t.Failed() {
		t.Logf("%d extraction replies read, all inside the grammar", read)
	}
}
