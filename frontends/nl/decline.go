package nl

import (
	"fmt"
	"strings"

	"github.com/panyam/jaala/internal/gbnf"
)

// DeclinePrefix opens a reply saying the data can't answer the question (Abstain.Declined, #551); the
// rest of its line says why.
const DeclinePrefix = "none:"

// declineMax caps the reason a guided decline gives, in characters, so a model writing prose stops.
const declineMax = 200

// declines says the model is offered a decline: Abstain.Declined is set.
func (o Options) declines() bool { return o.Abstain != nil && o.Abstain.Declined }

// declineLine is Prompts.Decline for a draft called what, or "" when the model isn't offered one.
func (o Options) declineLine(what string) string {
	if !o.declines() {
		return ""
	}
	return fmt.Sprintf(o.Pack.prompts().Decline, what)
}

// declining reads a reply as a decline: DeclinePrefix and a reason, its lines joined by spaces. A
// reasoning model holds to no grammar and may explain over several lines, which are still its reason.
func declining(text string) (why string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), DeclinePrefix)
	if !ok {
		return "", false
	}
	why = strings.Join(strings.Fields(rest), " ")
	return why, why != ""
}

// declineGrammar is grammar with a decline as its root's other choice: the draft grammar's root
// becomes drafted, and root takes a decline or a draft. actionGrammar and finalGrammar wrap it as they
// wrap any draft grammar.
func declineGrammar(grammar string) string {
	if grammar == "" {
		return ""
	}
	reason := gbnf.Class{Spec: "^\\x00-\\x1F", Sample: "no data. "} // any character but a control one, so one line
	rules := []gbnf.Rule{
		{Name: "root", E: gbnf.Alt{gbnf.Seq{gbnf.Lit(DeclinePrefix + " "), reason, gbnf.UpTo(reason, declineMax-1)}, gbnf.Ref("drafted")}},
	}
	return gbnf.Render(rules) + rootRule.ReplaceAllString(grammar, "drafted ::=")
}
