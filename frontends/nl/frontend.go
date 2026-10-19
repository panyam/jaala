package nl

import (
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A FrontEnd is a language a model writes in place of jaala's own (#354): Cypher (frontends/cypher's
// FrontEnd) and later others. Translate with Options.FrontEnd prompts with the front end's card,
// instructions and examples, compiles each draft into a program and checks and runs that as it would
// one the model wrote in jaala, and sends every refusal back in the front end's terms, so a host can
// measure which language its model writes better over the same questions. It is an interface here,
// implemented by the front end, because nl imports no front end.
type FrontEnd interface {
	// Language names it, for Try and Program ("cypher").
	Language() string
	// Card is what a draft over v can name, in the language's terms, in place of Card(v).
	Card(v *ns.Vocabulary) (string, error)
	// Instructions say how to write one draft, after the card.
	Instructions() string
	// Examples are questions with drafts answering them, in the language, which Translate shows the
	// model in place of the pack's (jaala) examples and compiles and checks before asking it.
	Examples() []Example
	// Grammar is a GBNF grammar a decoder can hold the model to, sent as Prompt.Grammar; "" for none.
	Grammar(v *ns.Vocabulary) (string, error)
	// Compile reads a draft and writes the jaala program answering it. Its Compiled is set even when it
	// fails, so its Explain writes the refusal.
	Compile(v *ns.Vocabulary, text string) (Compiled, error)
}

// Compiled is a draft a FrontEnd compiled: the program it became, and how to explain an error in the
// draft's terms.
type Compiled struct {
	// Program is the jaala text the draft compiled to, and Query that text parsed; both empty when
	// compiling failed.
	Program string
	Query   datalog.Query
	// Explain writes an error from compiling the draft, or from checking or running its program, the
	// way the model fixes the draft from: in its language, at its place in the draft.
	Explain func(err error) string
	// Kind sorts a compile refusal for Try.Kind, such as "gap" for a construct the language allows
	// and jaala can't express yet; "" is KindCompile.
	Kind string
}

// KindCompile is a draft its FrontEnd refused to compile.
const KindCompile = "compile"
