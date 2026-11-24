package nl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// xverdict names which of an ExtractVerdict's outcomes is set, failing on one setting none or several.
func xverdict(t *testing.T, vd nl.ExtractVerdict) string {
	t.Helper()
	var set []string
	if vd.Extraction != nil {
		set = append(set, "accept")
	}
	if vd.Continue != "" {
		set = append(set, "continue")
	}
	if vd.Stop != nil {
		set = append(set, "stop")
	}
	if len(set) != 1 {
		t.Fatalf("verdict sets %v: %+v", set, vd)
	}
	return set[0]
}

const linked = "a links to b."

// A session asks with the text, holds replies to ExtractGrammar, and accepts a reply whose facts
// check and quote the text, its fence taken off, citing the quoted words.
func TestAnExtractSessionAcceptsFactsTheTextStates(t *testing.T) {
	v := vocabulary(t)
	s, err := nl.NewExtractSession(linked, v, nl.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := nl.ExtractGrammar(v)
	if s.Ask() != "Text:\n"+linked || s.Grammar() != g || !strings.Contains(s.Prefix(), "# edge(from, to)") || s.MaxTries() != 3 {
		t.Errorf("ask %q, grammar %v, tries %d, prefix:\n%s", s.Ask(), s.Grammar() == g, s.MaxTries(), s.Prefix())
	}
	vd := s.NewConversation().Judge(context.Background(), nl.Try{Model: "m", Text: "```\n+ edge(\"a\", \"b\") from \"a links to b\"\n```"})
	if xverdict(t, vd) != "accept" || vd.Extraction.Model != "m" || len(vd.Extraction.Facts) != 1 || vd.Extraction.Facts[0].Span != "a links to b" || vd.Try.Err != nil {
		t.Errorf("verdict %+v", vd)
	}
	if plain, _ := nl.NewExtractSession(linked, v, nl.ExtractOptions{NoGrammar: true}); plain.Grammar() != "" {
		t.Errorf("NoGrammar sends %q", plain.Grammar())
	}
}

// A refused reply continues with the line it failed on and ExtractFix; the same reply again stops,
// wrapping the first refusal, and so does the last try. control: a different reply continues.
func TestAnExtractConversationContinuesThenStops(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewExtractSession(linked, v, nl.ExtractOptions{MaxTries: 3})
	bad := `+ edge("a", "b") from "nowhere in the text"`
	c := s.NewConversation()
	vd := c.Judge(context.Background(), nl.Try{Text: bad})
	if xverdict(t, vd) != "continue" || vd.Try.Kind != nl.KindSpan || !strings.HasSuffix(vd.Continue, nl.DefaultPrompts().ExtractFix) || !strings.Contains(vd.Continue, "line 1") {
		t.Fatalf("verdict %+v", vd)
	}
	vd = c.Judge(context.Background(), nl.Try{Text: bad})
	if xverdict(t, vd) != "stop" || vd.Try.Kind != nl.KindRepeat || !errors.Is(vd.Stop, vd.Try.Err) {
		t.Errorf("the repeat %+v", vd)
	}
	c = s.NewConversation()
	c.Judge(context.Background(), nl.Try{Text: bad})
	if vd := c.Judge(context.Background(), nl.Try{Text: `+ nope("a") from "a links to b"`}); xverdict(t, vd) != "continue" {
		t.Errorf("control: a different reply %+v", vd)
	}
	vd = c.Judge(context.Background(), nl.Try{Text: `+ edge("a") from "a links to b"`})
	var ce *datalog.CheckError
	if xverdict(t, vd) != "stop" || !errors.As(vd.Stop, &ce) {
		t.Errorf("the last try %+v", vd)
	}
}

// weighed is vocabulary's relations over a source holding two weights, keyed by node.
func weighed(t *testing.T) (*ns.Vocabulary, *datalog.Base, *nl.Pack) {
	t.Helper()
	node := ns.ArgType{Kind: "node"}
	src := ns.NewMemSource().
		DeclareSchema("edge", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Types: []ns.ArgType{node, node}}).
		DeclareSchema("weight", ns.Schema{Arity: 2, Labels: []string{"node", "w"}, Types: []ns.ArgType{node, {Type: ns.TypeNumber}}})
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("b"), ns.N(2)}})
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.N(1)}})
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b, &nl.Pack{Keys: map[string][]int{"weight": {0}}}
}

// The text's turn lists the keys the Base holds for each keyed relation, sorted and capped at
// HeldMax, so a model updating a fact spells its key as the Base does (#563). control: without a
// Base, or with HeldMax negative, the turn is the text alone; an unkeyed relation (edge) isn't listed.
func TestAnExtractionListsTheKeysTheBaseHolds(t *testing.T) {
	v, b, pack := weighed(t)
	held := nl.DefaultPrompts().ExtractHeld
	s, err := nl.NewExtractSession(linked, v, nl.ExtractOptions{Base: b, Pack: pack})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Text:\n" + linked + "\n\n" + held + "\nweight(\"a\", _)\nweight(\"b\", _)"; s.Ask() != want {
		t.Errorf("ask:\n%s\nwant:\n%s", s.Ask(), want)
	}
	one, _ := nl.NewExtractSession(linked, v, nl.ExtractOptions{Base: b, Pack: pack, HeldMax: 1})
	if want := "Text:\n" + linked + "\n\n" + held + "\nweight(\"a\", _)\n(and 1 more)"; one.Ask() != want {
		t.Errorf("HeldMax 1:\n%s\nwant:\n%s", one.Ask(), want)
	}
	for name, o := range map[string]nl.ExtractOptions{"no Base": {Pack: pack}, "HeldMax -1": {Base: b, Pack: pack, HeldMax: -1}} {
		if s, _ := nl.NewExtractSession(linked, v, o); s.Ask() != "Text:\n"+linked {
			t.Errorf("%s: ask %q", name, s.Ask())
		}
	}
}

// A reply that reached its token cap is refused, however well it reads, and the model is told so; a
// retry that wasn't cut is accepted, and a turn whose every try was cut stops with ErrTruncated.
// control: the same reply, not cut, is accepted.
func TestATruncatedReplyIsRefused(t *testing.T) {
	v := vocabulary(t)
	s, err := nl.NewExtractSession(linked, v, nl.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	draft := `+ edge("a", "b") from "a links to b"`
	c := s.NewConversation()
	vd := c.Judge(context.Background(), nl.Try{Text: "", Truncated: true})
	if xverdict(t, vd) != "continue" || vd.Try.Kind != nl.KindTruncated || vd.Continue != nl.DefaultPrompts().ExtractTruncated {
		t.Errorf("an empty cut reply: %+v", vd)
	}
	if vd := c.Judge(context.Background(), nl.Try{Text: draft, Truncated: true}); xverdict(t, vd) != "continue" || vd.Try.Kind != nl.KindTruncated {
		t.Errorf("a cut reply that reads well: %+v", vd)
	}
	if vd := c.Judge(context.Background(), nl.Try{Text: draft}); xverdict(t, vd) != "accept" {
		t.Errorf("the retry, not cut: %+v", vd)
	}
	once, _ := nl.NewExtractSession(linked, v, nl.ExtractOptions{MaxTries: 1})
	if vd := once.NewConversation().Judge(context.Background(), nl.Try{Text: draft, Truncated: true}); xverdict(t, vd) != "stop" || !errors.Is(vd.Stop, nl.ErrTruncated) {
		t.Errorf("the last try cut: %+v", vd)
	}
}

// A pack's own held-keys heading, left-out line and cut-reply line replace jaala's, and a left-out
// line without its placeholder is refused. control: jaala's default is a template of one.
func TestAPacksExtractPromptsReplaceJaalas(t *testing.T) {
	v, b, pack := weighed(t)
	pack.Prompts = &nl.Prompts{ExtractHeld: "Known:", ExtractHeldMore: "+%s", ExtractTruncated: "Too long."}
	s, err := nl.NewExtractSession(linked, v, nl.ExtractOptions{Base: b, Pack: pack, HeldMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Text:\n" + linked + "\n\nKnown:\nweight(\"a\", _)\n+1"; s.Ask() != want {
		t.Errorf("ask:\n%s\nwant:\n%s", s.Ask(), want)
	}
	if vd := s.NewConversation().Judge(context.Background(), nl.Try{Truncated: true}); vd.Continue != "Too long." {
		t.Errorf("a cut reply continues with %q", vd.Continue)
	}
	pack.Prompts = &nl.Prompts{ExtractHeldMore: "more were held"}
	if err := pack.Validate(v); err == nil || !strings.Contains(err.Error(), "ExtractHeldMore") {
		t.Errorf("a left-out line without its placeholder: %v", err)
	}
	if n := strings.Count(nl.DefaultPrompts().ExtractHeldMore, "%s"); n != 1 {
		t.Errorf("control: jaala's ExtractHeldMore has %d placeholders", n)
	}
}
