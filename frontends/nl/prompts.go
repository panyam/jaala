package nl

import (
	"fmt"
	"strings"
)

// Prompts is the text jaala writes into a model's prompt around the host's own (#485). jaala owns the
// loop and these defaults; a host replaces any of them word for word through Pack.Prompts, to tune
// its prompts against its own models without a jaala change. A field left "" keeps jaala's text, so
// a host sets only what it changes. DefaultPrompts returns jaala's, for a host to start from.
//
// A field whose doc names placeholders is a fmt template: a replacement keeps every one of them, in
// any order with explicit indexes (%[2]s), and Pack.Validate refuses one that drops or adds any. A
// field holds its own sentences; the blank lines between it and the rest of a turn are jaala's.
type Prompts struct {
	// Instructions follow the card in every prompt the translator writes in jaala's language.
	Instructions string
	// FrontEndInstructions replace a front end's own instructions (FrontEnd.Instructions), keyed by
	// its Language, such as "cypher". The MCP server builds its Cypher front end itself, so this is
	// how a host behind it changes them.
	FrontEndInstructions map[string]string
	// Actions follow the instructions when Options.Actions is set. Placeholders: the values target, as
	// the model's language writes one (relation.argument), then Find's line or "".
	Actions string
	// Find is Actions' line for the find action, when there is a Grounder.
	Find string
	// Build is Actions' lines for add and drop, under Actions.Build (#514).
	Build string
	// Paths is Actions' lines for paths, when the pack has a Shape (#514).
	Paths string
	// PathsFound heads the joins a paths action found. Placeholders: the two ends as the model
	// wrote them.
	PathsFound string
	// PathsNone is what a paths action sends back when no join answered. Placeholders: the two ends,
	// then how many links a join may take.
	PathsNone string
	// Dropped is what a drop sends back. Placeholders: the literal taken off, then the body left.
	Dropped string
	// DropNothing is what a drop sends back when the body is empty.
	DropNothing string
	// NotAdded follows the reason an add's body didn't check, which leaves the body as it was.
	NotAdded string
	// AskedAgain opens what an action sends back when the model asked the same action before, which
	// isn't run again (#517); what it answered then follows.
	AskedAgain string
	// LastLook ends the last action's result the model may ask for.
	LastLook string
	// TriedAgain opens what a try sends back when the model tried that draft before, which isn't run
	// again (#512); what it answered then follows.
	TriedAgain string
	// LookFound opens a look's list of the members mentioning a word that names none (#512).
	// Placeholder: the word.
	LookFound string
	// LookNothing follows the error of a look that names nothing and matches nothing.
	LookNothing string
	// DraftFirst follows the question when Actions.DraftFirst's one-shot draft was refused (#521), so
	// the model's turn with actions starts from that draft rather than from nothing. Placeholders:
	// the draft, why it was refused, and "program" or "query".
	DraftFirst string
	// Handoff follows the question when the frontier tier starts from the local tier's closest draft
	// (Options.Tiers, #538). Placeholders: the draft, why it wasn't taken, and "program" or "query".
	Handoff string
	// Stuck is the note a model gets when it repeats an action, or takes turns between two, with the
	// same results (#512). Placeholder: what it repeated, as chakra's StuckFinding writes it.
	Stuck string
	// ToolLook, ToolValues, ToolTry and ToolFind describe the actions when they are offered as tools
	// (Actions.AsTools, Session.Tools). ToolValues' placeholder is the values target, as the model's
	// language writes one.
	ToolLook, ToolValues, ToolTry, ToolFind string
	// ToolAdd and ToolDrop describe add and drop as tools, under Actions.Build.
	ToolAdd, ToolDrop string
	// PathsUsage is what a paths action sends back for an argument with no " => " (#532).
	PathsUsage string
	// PathsNoKind is what it sends back for an end with no kind. Placeholder: the argument, quoted.
	PathsNoKind string
	// PathsUnbound is what it sends back for an end whose literals never mention ?x (#530).
	// Placeholder: the end's kind.
	PathsUnbound string
	// ToolPaths describes paths as a tool, when the pack has a Shape.
	ToolPaths string
	// Fix ends a refused draft's feedback. Placeholder: "program", or "query" under a front end.
	Fix string
	// FixEmpty ends the feedback of a draft that answered nothing. Placeholder as Fix.
	FixEmpty string
	// EmptyFallback is added to the question a fallback is asked when the model before it wrote a
	// program that answered nothing.
	EmptyFallback string
	// Reask is what a model resending a draft that answered nothing is told (#483). Placeholders:
	// "program" or "query"; what it was told the first time; the grounded values (Grounded, or "");
	// the part to change.
	Reask string
	// Without heads what an empty answer's goal gives without its culprit literal. Placeholders: the
	// literal, then the variables shown.
	Without string
	// Grounded heads the grounded values listed after the question.
	Grounded string
	// Pick opens the prompt asking the model to pick one of Pack.Callables (#484).
	Pick string
	// PickRetry ends a refused pick's feedback.
	PickRetry string
	// CompactFound and CompactRefused head the memory's lists in Actions.Compact's one turn.
	CompactFound, CompactRefused string
	// LedgerFound and LedgerTried head Conversation.Ledger's two lists (#514): what the actions found,
	// and the drafts tried with what each did. nlmodels' translator names a chakra slot after each,
	// without a trailing colon.
	LedgerFound, LedgerTried string
	// LedgerHeader opens the ledger where a loop shows its lists apart (Session.LedgerHeader), as
	// nlmodels' translator does in chakra's slots.
	LedgerHeader string
	// Extract follows the relations in every Extract prompt.
	Extract string
	// ExtractFix ends a refused extraction's feedback.
	ExtractFix string
}

// DefaultPrompts is jaala's text for every field of Prompts.
func DefaultPrompts() Prompts {
	return Prompts{
		Instructions:  instructions,
		Actions:       actionInstructions,
		Find:          findInstruction,
		Build:         buildInstruction,
		Paths:         pathsInstruction,
		PathsFound:    "These joins from %s to %s answered. Write your program from the one that fits the question, changing its columns as it asks:",
		PathsNone:     "No join from %s to %s within %s links answered. Check each end's kind and literals with look and values.",
		PathsUsage:    pathsUsage,
		PathsNoKind:   "paths: an end needs a kind first, in %s",
		PathsUnbound:  "paths: the literals after %[1]s never mention ?x, the end's node, so nothing ties them to it; write them over ?x, as %[1]s ?x = \"an id\"",
		ToolPaths:     "The joins from one kind of node to another, each run on a few rows, so you can pick one rather than write it. ends is FROM => TO, each a kind then literals over ?x, such as func str.glob(?x, \"github.com/a/*\") => func ?x = \"github.com/b.F\".",
		Dropped:       "Dropped %s. The body is now: %s",
		DropNothing:   "The body is empty; there is nothing to drop.",
		NotAdded:      "That literal wasn't added; the body is as it was.",
		ToolAdd:       "Add one literal to the program you are building, and see the rows the body answers so far, or why none. literal is one atom, negation or comparison. Answer with => and the columns once the body is right.",
		ToolDrop:      "Take the last literal off the program you are building.",
		AskedAgain:    "You asked that already.",
		LastLook:      "That was the last look: reply with your answer alone, not another action.",
		TriedAgain:    "You tried that draft already, so it wasn't run again. Last time:",
		LookFound:     "Nothing is named %s; these mention it:",
		LookNothing:   "look takes a relation, module or label name, as the card lists them.",
		DraftFirst:    "You drafted this %[3]s for it first:\n%[1]s\nIt was refused:\n%[2]s\n\nUse the actions to find what it got wrong, then answer with a %[3]s that fixes it.",
		Handoff:       "A smaller model drafted this %[3]s for it first:\n%[1]s\nIt wasn't taken:\n%[2]s\n\nAnswer with a %[3]s that fixes what was wrong with it.",
		Stuck:         "You have %s. Doing it again gives the same result: change the draft, or reply with your answer.",
		ToolLook:      "What a relation, module or label holds: its arguments and what they mean. name is the relation, module or label.",
		ToolValues:    "Some of the values one argument or property holds. target is one, such as %s.",
		ToolTry:       "Run a draft on a few rows and see its rows, or why none came back. It isn't your answer: reply with that once it is right.",
		ToolFind:      "The ids or values a name from the question may stand for. name is that name.",
		Fix:           "Write the whole %s again, fixed.",
		FixEmpty:      "Write the whole %s again, with other members or values.",
		EmptyFallback: "A program written for this answered nothing; the question may need other members or values.",
		Reask: "That is the same %[1]s you sent before, and it answers nothing for the same reason:\n\n%[2]s%[3]s" +
			"\n\nDon't send it again. Change %[4]s to other members or values, and write the whole %[1]s again.",
		Without:        "Without %[1]s, the rest of the goal gives %[2]s values such as:",
		Grounded:       "Values the question's names may stand for, likeliest first (use them exactly as written):",
		Pick:           pickInstructions,
		PickRetry:      "Call another relation, or this one with other values, or reply none.",
		CompactFound:   "What you have found so far:",
		CompactRefused: "Your last answer was refused:",
		LedgerFound:    "What your actions found so far:",
		LedgerTried:    "Drafts you have tried, and what each did (don't send one again):",
		LedgerHeader:   "What you have done on this question so far:",
		Extract:        extractInstructions,
		ExtractFix:     "Write all the lines again, fixed.",
	}
}

// prompts is p's Prompts over jaala's defaults, field by field; a nil pack, or a nil Prompts, is all
// defaults.
func (p *Pack) prompts() Prompts {
	d := DefaultPrompts()
	if p == nil || p.Prompts == nil {
		return d
	}
	h := *p.Prompts
	for _, f := range []struct{ host, def *string }{
		{&h.Instructions, &d.Instructions}, {&h.Actions, &d.Actions}, {&h.Find, &d.Find},
		{&h.Build, &d.Build}, {&h.Paths, &d.Paths}, {&h.PathsFound, &d.PathsFound}, {&h.PathsNone, &d.PathsNone}, {&h.PathsUsage, &d.PathsUsage}, {&h.PathsNoKind, &d.PathsNoKind}, {&h.PathsUnbound, &d.PathsUnbound}, {&h.ToolPaths, &d.ToolPaths}, {&h.Dropped, &d.Dropped}, {&h.DropNothing, &d.DropNothing}, {&h.NotAdded, &d.NotAdded},
		{&h.ToolAdd, &d.ToolAdd}, {&h.ToolDrop, &d.ToolDrop},
		{&h.AskedAgain, &d.AskedAgain}, {&h.LastLook, &d.LastLook}, {&h.TriedAgain, &d.TriedAgain},
		{&h.LookFound, &d.LookFound}, {&h.LookNothing, &d.LookNothing}, {&h.Stuck, &d.Stuck}, {&h.DraftFirst, &d.DraftFirst}, {&h.Handoff, &d.Handoff}, {&h.ToolLook, &d.ToolLook},
		{&h.ToolValues, &d.ToolValues}, {&h.ToolTry, &d.ToolTry}, {&h.ToolFind, &d.ToolFind}, {&h.Fix, &d.Fix}, {&h.FixEmpty, &d.FixEmpty},
		{&h.EmptyFallback, &d.EmptyFallback}, {&h.Reask, &d.Reask}, {&h.Grounded, &d.Grounded}, {&h.Without, &d.Without},
		{&h.Pick, &d.Pick}, {&h.PickRetry, &d.PickRetry}, {&h.CompactFound, &d.CompactFound},
		{&h.CompactRefused, &d.CompactRefused}, {&h.LedgerFound, &d.LedgerFound}, {&h.LedgerTried, &d.LedgerTried}, {&h.LedgerHeader, &d.LedgerHeader},
		{&h.Extract, &d.Extract}, {&h.ExtractFix, &d.ExtractFix},
	} {
		if *f.host != "" {
			*f.def = *f.host
		}
	}
	d.FrontEndInstructions = h.FrontEndInstructions
	return d
}

// templates are the Prompts fields that are fmt templates, with how many placeholders each takes.
func (t Prompts) templates() []struct {
	name, text string
	n          int
} {
	return []struct {
		name, text string
		n          int
	}{{"Actions", t.Actions, 2}, {"ToolValues", t.ToolValues, 1}, {"Fix", t.Fix, 1}, {"FixEmpty", t.FixEmpty, 1}, {"Reask", t.Reask, 4},
		{"LookFound", t.LookFound, 1}, {"Stuck", t.Stuck, 1}, {"DraftFirst", t.DraftFirst, 3}, {"Handoff", t.Handoff, 3}, {"Dropped", t.Dropped, 2}, {"PathsFound", t.PathsFound, 2}, {"PathsNone", t.PathsNone, 3}, {"PathsNoKind", t.PathsNoKind, 1}, {"PathsUnbound", t.PathsUnbound, 1}, {"Without", t.Without, 2}}
}

// checkTemplates refuses a template that drops one of its placeholders or reads one it isn't given:
// filled with a marker per argument, every marker must appear and fmt must report nothing amiss.
func (t Prompts) checkTemplates() error {
	for _, tm := range t.templates() {
		args := make([]any, tm.n)
		for i := range args {
			args[i] = fmt.Sprintf("\x00%d\x00", i)
		}
		out := fmt.Sprintf(tm.text, args...)
		if strings.Contains(out, "%!") {
			return fmt.Errorf("nl: the pack's prompt %s isn't a template of %d placeholders: %q", tm.name, tm.n, tm.text)
		}
		for i, a := range args {
			if !strings.Contains(out, a.(string)) {
				return fmt.Errorf("nl: the pack's prompt %s leaves out placeholder %d of %d", tm.name, i+1, tm.n)
			}
		}
	}
	return nil
}
