package memory

import (
	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// The memory's relations.
const (
	// RelSession holds one row per ingested session: its id and its date as the history writes it.
	RelSession = "session"
	// RelFact holds what the user said is so: a subject, an attribute of it and its value. The
	// subject and attribute are its key, so a later statement replaces an earlier one.
	RelFact = "fact"
	// RelEvent holds what the user said happened, and when.
	RelEvent = "event"
)

// declare adds the memory's relations to src.
func declare(src *ns.TimedMemSource) {
	src.DeclareSchema(RelSession, ns.Schema{Arity: 2, Labels: []string{"id", "date"},
		Doc: "A conversation session and the date it took place, as written in the history."})
	src.DeclareSchema(RelFact, ns.Schema{Arity: 3, Labels: []string{"subject", "attribute", "value"},
		Doc: `Something the user said is so: "user" or another subject, an attribute of it in a few lowercase words, and its value. A later statement about the same subject and attribute replaces the earlier one.`})
	src.DeclareSchema(RelEvent, ns.Schema{Arity: 2, Labels: []string{"what", "date"},
		Doc: `Something the user said happened, in a few words, and the date it happened as YYYY/MM/DD, worked out from the session's date when the text says "yesterday" or "last week".`})
}

// vocabulary is the memory's relations over src, with jaala's language and its standard library.
func vocabulary(src *ns.TimedMemSource) (*ns.Vocabulary, error) {
	declare(src)
	v, err := ns.NewVocabulary(src)
	if err != nil {
		return nil, err
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		return nil, err
	}
	if err := stdlib.Register(v); err != nil {
		return nil, err
	}
	return v, nil
}

// Pack is the memory's domain for nl: what the data is about, the keys that make a later fact replace
// an earlier one, and an example of a session read into facts and events.
func Pack() *nl.Pack {
	return &nl.Pack{
		About: "What a user told an assistant across dated chat sessions: facts about the user and their world, and events with the dates they happened.",
		Keys:  map[string][]int{RelFact: {0, 1}},
		ExtractExamples: []nl.ExtractExample{{
			Text: "Session s1, 2023/05/20 (Sat) 10:02\nuser: I just moved to Denver, and I adopted a beagle yesterday.\nassistant: Congratulations on the new dog!",
			Lines: `+ fact("user", "lives in", "Denver") from "I just moved to Denver"
+ fact("user", "dog breed", "beagle") from "I adopted a beagle"
+ event("adopted a beagle", "2023/05/19") from "I adopted a beagle yesterday"`,
		}},
	}
}
