package memory

// Prompts is every piece of text a Memory or OneShotArm shows a model, so a host can change any of it.
// A zero field keeps DefaultPrompts'.
type Prompts struct {
	// Query describes the query tool. The vocabulary's card follows it.
	Query string
	// ReadSession describes the read_session tool.
	ReadSession string
	// Answer is OneShotArm's instructions. Placeholders: the question's date, then the computed
	// answer (Answer.String).
	Answer string
	// NoAnswer is OneShotArm's instructions when no program answered. Placeholders: the question's
	// date, then why.
	NoAnswer string
}

// DefaultPrompts is the text a Memory and OneShotArm show by default.
var DefaultPrompts = Prompts{
	Query: `Runs a program in jaala's Datalog over what the user told you in past sessions, and returns its rows, each with the words it came from. ` +
		`A program is rules, if any, then one goal: 'rule(?a, ?b) :- body; ... body => ?a, ?b'. Variables are ?name and strings are "quoted"; its columns follow => without parentheses. ` +
		`A refused program comes back with what to fix. The relations and predicates:`,
	ReadSession: "Returns the text of one past session, by its id from the session relation, for what the facts don't say.",
	Answer: "The current date is %s. These rows were computed from the user's past sessions, " +
		"and each cites the words it came from. Answer the user's question from them. " +
		"If they don't answer it, say you don't know.\n\n%s",
	NoAnswer: "The current date is %s. Nothing in the user's past sessions answered this question (%s). " +
		"Say you don't know.",
}

// withDefaults fills p's zero fields from DefaultPrompts.
func (p Prompts) withDefaults() Prompts {
	d := DefaultPrompts
	if p.Query != "" {
		d.Query = p.Query
	}
	if p.ReadSession != "" {
		d.ReadSession = p.ReadSession
	}
	if p.Answer != "" {
		d.Answer = p.Answer
	}
	if p.NoAnswer != "" {
		d.NoAnswer = p.NoAnswer
	}
	return d
}
