package translator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
)

// hostPrompts replaces every Prompts field with a marker naming it, templates keeping their
// placeholders, so a test can tell which text reached the model.
func hostPrompts() *nl.Prompts {
	return &nl.Prompts{
		Instructions:         "H-INSTRUCTIONS",
		FrontEndInstructions: map[string]string{"shout": "H-SHOUT"},
		Actions:              "\nH-ACTIONS %s|%s\n",
		Find:                 "H-FIND",
		AskedAgain:           "H-ASKED",
		LastLook:             "H-LAST",
		TriedAgain:           "H-TRIED",
		LookFound:            "H-LOOKFOUND %s",
		LookNothing:          "H-LOOKNOTHING",
		Stuck:                "H-STUCK %s",
		ToolLook:             "H-TOOLLOOK",
		ToolValues:           "H-TOOLVALUES %s",
		ToolTry:              "H-TOOLTRY",
		ToolFind:             "H-TOOLFIND",
		Fix:                  "H-FIX %s",
		FixEmpty:             "H-FIXEMPTY %s",
		EmptyFallback:        "H-EMPTYFALLBACK",
		Reask:                "H-REASK %[4]s|%[1]s|%[3]s|%[2]s",
		Grounded:             "H-GROUNDED",
		Pick:                 "H-PICK\n",
		PickRetry:            "H-PICKRETRY",
		CompactFound:         "H-FOUND",
		CompactRefused:       "H-REFUSED",
		Build:                "H-BUILD\n",
		Dropped:              "H-DROPPED %s|%s",
		DropNothing:          "H-DROPNOTHING",
		NotAdded:             "H-NOTADDED",
		ToolAdd:              "H-TOOLADD",
		ToolDrop:             "H-TOOLDROP",
		LedgerFound:          "H-LEDGERFOUND",
		LedgerTried:          "H-LEDGERTRIED",
		Extract:              "\nH-EXTRACT\n",
		ExtractFix:           "H-EXTRACTFIX",
	}
}

func with(p *nl.Pack, pr *nl.Prompts) *nl.Pack {
	q := *p
	q.Prompts = pr
	return &q
}

func want(s string, parts ...string) string {
	var miss []string
	for _, p := range parts {
		if !strings.Contains(s, p) {
			miss = append(miss, fmt.Sprintf("lacks %q", p))
		}
	}
	if miss == nil {
		return ""
	}
	return strings.Join(miss, "; ") + fmt.Sprintf(" in %q", s)
}

func absent(s string, parts ...string) string {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return fmt.Sprintf("still holds %q; ", p)
		}
	}
	return ""
}

// Every field a pack sets reaches the prompt in place of jaala's text (#485): the instructions, a
// front end's, the tools' descriptions, an action asked again and the last one, the fix lines, the
// fallback's note, the reask, the grounded values' heading, the pick's and its retry, the compact
// turn's headings, a try tried again, a look for a plain word, and the stuck note. control: a pack setting none sends jaala's text.
func TestAPacksPromptsReplaceJaalasText(t *testing.T) {
	v := vocabulary(t)
	b := graphBase(t, v)
	ctx := context.Background()
	g := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"a"}}}}
	for _, c := range []struct {
		name string
		o    nl.Options
		g    nl.Grounder
		// replies the model gives in turn, and what the asked prompts must hold
		replies []string
		check   func(m *scripted, tr nl.Trace) string
	}{
		{"instructions, the tools and find", nl.Options{Base: b, Actions: &nl.Actions{}}, g, []string{`edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			pre := m.asked[0].Prefix
			d := m.descs
			tools := fmt.Sprintf("%s|%s|%s|%s", d[nl.ActionLook], d[nl.ActionValues], d[nl.ActionTry], d[nl.ActionFind])
			return want(pre, "H-INSTRUCTIONS") + absent(pre, "# Write one program", "H-ACTIONS", "#   look NAME") +
				want(tools, "H-TOOLLOOK|H-TOOLVALUES relation.argument|H-TOOLTRY|H-TOOLFIND") +
				want(m.asked[0].Turns[0].Text, "\n\nH-GROUNDED\na: \"a\"")
		}},
		{"a front end's instructions", nl.Options{Base: b, FrontEnd: shout{}}, nil, []string{`SAY edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			return want(m.asked[0].Prefix, "H-SHOUT") + absent(m.asked[0].Prefix, "# Say SAY", "H-INSTRUCTIONS")
		}},
		{"asked again and the last look", nl.Options{Base: b, Actions: &nl.Actions{Steps: 2}}, nil, []string{"look edge", "look edge", "values edge.0", `edge("a", ?b) => ?b`}, func(_ *scripted, tr nl.Trace) string {
			if r := tr.Steps[1].Result; !strings.HasPrefix(r, "H-ASKED ") {
				return fmt.Sprintf("second look's result %q", r)
			}
			if r := tr.Steps[2].Result; !strings.HasSuffix(r, "\n\nH-LAST") {
				return fmt.Sprintf("the last step's result %q", r)
			}
			return ""
		}},
		{"building a literal at a time", nl.Options{Base: b, Actions: &nl.Actions{Steps: 6, Build: true}}, nil, []string{`add edge("a", ?b)`, "add edge(", "drop", "drop", `edge("a", ?b) => ?b`}, func(m *scripted, tr nl.Trace) string {
			if len(tr.Steps) != 4 {
				return fmt.Sprintf("%d steps", len(tr.Steps))
			}
			return want(m.descs[nl.ActionAdd], "H-TOOLADD") + want(m.descs[nl.ActionDrop], "H-TOOLDROP") +
				want(tr.Steps[1].Result, "\n\nH-NOTADDED") + want(tr.Steps[2].Result, `H-DROPPED edge("a", ?b)|(empty)`) +
				want(tr.Steps[3].Result, "H-DROPNOTHING")
		}},
		{"the ledger", nl.Options{Base: b, Actions: &nl.Actions{Steps: 2, Ledger: true}}, nil, []string{"values edge.0", "try\n" + `edge("zz", ?b) => ?b`, `edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			ledger := m.asked[2].Turns[len(m.asked[2].Turns)-1].Text
			return want(ledger, "H-LEDGERFOUND\n- values edge.0", "\n\nH-LEDGERTRIED\n- try ")
		}},
		{"a try tried again and the stuck note", nl.Options{Base: b, Actions: &nl.Actions{}}, nil, []string{"try\n" + `edge("zz", ?b) => ?b`, "try\n" + `edge("zz", ?b) => ?b`, `edge("a", ?b) => ?b`}, func(m *scripted, tr nl.Trace) string {
			if r := tr.Steps[1].Result; !strings.HasPrefix(r, "H-TRIED\n") {
				return fmt.Sprintf("the second try's result %q", r)
			}
			for _, turn := range m.asked[2].Turns {
				if strings.HasPrefix(turn.Text, "H-STUCK repeated try 2 times") {
					return ""
				}
			}
			return fmt.Sprintf("no stuck note in %+v", m.asked[2].Turns)
		}},
		{"a look for a plain word", nl.Options{Base: b, Actions: &nl.Actions{}}, nil, []string{"look dge", "look zebra", `edge("a", ?b) => ?b`}, func(_ *scripted, tr nl.Trace) string {
			if found, none := tr.Steps[0].Result, tr.Steps[1].Result; !strings.HasPrefix(found, "H-LOOKFOUND dge\n") || !strings.HasSuffix(none, "\nH-LOOKNOTHING") {
				return fmt.Sprintf("the looks' results %q, %q", found, none)
			}
			return ""
		}},
		{"fix and fix empty", nl.Options{Base: b, MaxTries: 3}, nil, []string{`nope(?a) => ?a`, `edge("zz", ?b) => ?b`, `edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			turns := m.asked[2].Turns
			return want(turns[2].Text, "\n\nH-FIX program") + want(turns[4].Text, "\n\nH-FIXEMPTY program")
		}},
		{"the reask", nl.Options{Base: b}, g, []string{`edge("zz", ?b) => ?b`, `edge("zz", ?b) => ?b`, `edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			again := m.asked[2].Turns[4].Text
			return want(again, "H-REASK `edge(\"zz\", ?b)`, which matched nothing,|program|\n\nH-GROUNDED\na: \"a\"|the program answered no rows")
		}},
		{"the pick and its retry", nl.Options{Base: b, Pack: edges()}, nil, []string{`edge("c", ?to)`, `edge("b", ?to)`}, func(m *scripted, _ nl.Trace) string {
			return want(m.asked[0].Prefix, "H-PICK\n") + absent(m.asked[0].Prefix, "# Answer the question with one call") +
				want(m.asked[1].Turns[2].Text, "\n\nH-PICKRETRY")
		}},
		{"the compact turn", nl.Options{Base: b, Actions: &nl.Actions{Steps: 2, Compact: true}}, nil, []string{"look edge", `edge("zz", ?b) => ?b`, `edge("a", ?b) => ?b`}, func(m *scripted, _ nl.Trace) string {
			return want(m.asked[2].Turns[0].Text, "\n\nH-FOUND\n", "\n\nH-REFUSED\n")
		}},
	} {
		pack := c.o.Pack
		if pack == nil {
			pack = &nl.Pack{}
		}
		o := c.o
		o.Pack = with(pack, hostPrompts())
		m := model("small", c.replies...)
		_, tr, err := translate(ctx, "q", v, c.g, m, o)
		if err != nil {
			t.Errorf("%s: %v (%v)", c.name, err, kinds(tr))
			continue
		}
		if msg := c.check(m, tr); msg != "" {
			t.Errorf("%s: %s", c.name, msg)
		}
		o.Pack = pack
		ctl := model("small", c.replies...)
		if _, tr, _ := translate(ctx, "q", v, c.g, ctl, o); c.check(ctl, tr) == "" {
			t.Errorf("%s: control: jaala's own text passed the check", c.name)
		}
	}

	small, big := model("small", `edge("zz", ?b) => ?b`), model("big", `edge("a", ?b) => ?b`)
	translate(ctx, "q", v, nil, small, nl.Options{Base: b, Pack: with(&nl.Pack{}, hostPrompts())}, big)
	if msg := want(big.asked[0].Turns[0].Text, "q\n\nH-EMPTYFALLBACK"); msg != "" {
		t.Errorf("the fallback's note: %s", msg)
	}
}

// A template a pack replaces keeps its placeholders, in any order: one dropping or adding one is
// refused by Validate and by Translate before any model is asked, under a front end too.
func TestAPacksPromptTemplatesKeepTheirPlaceholders(t *testing.T) {
	v := vocabulary(t)
	for _, c := range []struct {
		pr   nl.Prompts
		want string
	}{
		{nl.Prompts{Fix: "Fix it."}, "prompt Fix isn't a template of 1 placeholders"},
		{nl.Prompts{Actions: "%s %s %s"}, "prompt Actions isn't a template of 2 placeholders"},
		{nl.Prompts{Reask: "%[1]s %[2]s %[3]s"}, "prompt Reask leaves out placeholder 4 of 4"},
		{nl.Prompts{Reask: "%[5]s"}, "prompt Reask isn't a template of 4 placeholders"},
		{nl.Prompts{LookFound: "Nothing here."}, "prompt LookFound isn't a template of 1 placeholders"},
		{nl.Prompts{Stuck: "Stop."}, "prompt Stuck isn't a template of 1 placeholders"},
	} {
		pack := &nl.Pack{Prompts: &c.pr}
		if err := pack.Validate(v); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: Validate %v, want %q", c.pr, err, c.want)
		}
		m := model("small", `edge("a", ?b) => ?b`)
		if _, _, err := translate(context.Background(), "q", v, nil, m, nl.Options{Pack: pack, FrontEnd: shout{}}); err == nil || len(m.asked) != 0 {
			t.Errorf("%+v: Translate %v after %d asks", c.pr, err, len(m.asked))
		}
	}
	ok := &nl.Pack{Prompts: &nl.Prompts{Reask: "%[4]s %[3]s %[2]s %[1]s", Fix: "Again, as a %s."}}
	if err := ok.Validate(v); err != nil {
		t.Errorf("control: placeholders reordered: %v", err)
	}
}

// DefaultPrompts is the text jaala sends when a pack sets none, so a host can start from it.
func TestDefaultPromptsAreWhatJaalaSends(t *testing.T) {
	v := vocabulary(t)
	d := nl.DefaultPrompts()
	m := model("small", `nope(?a) => ?a`, `edge("a", ?b) => ?b`)
	if _, _, err := translate(context.Background(), "q", v, nil, m, nl.Options{}); err != nil {
		t.Fatal(err)
	}
	if msg := want(m.asked[0].Prefix, d.Instructions) + want(m.asked[1].Turns[2].Text, "\n\n"+fmt.Sprintf(d.Fix, "program")); msg != "" {
		t.Error(msg)
	}
}

// Extract's instructions and its retry line are a pack's too.
func TestAPacksPromptsReplaceExtractsText(t *testing.T) {
	v, b := owned(t)
	m := model("m", `+ imports("app", "api")`, `+ imports("app", "api") from "app imports api"`)
	if _, _, err := extract(context.Background(), "app imports api.", v, m, nl.ExtractOptions{Base: b, Pack: with(ownedPack(), hostPrompts())}); err != nil {
		t.Fatal(err)
	}
	if msg := want(m.asked[0].Prefix, "\nH-EXTRACT\n") + absent(m.asked[0].Prefix, "# Write the facts the text states") + want(m.asked[1].Turns[2].Text, "\n\nH-EXTRACTFIX"); msg != "" {
		t.Error(msg)
	}
}
