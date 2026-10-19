package nl

import (
	"fmt"
	"sync"

	"github.com/panyam/jaala/internal/gbnf"
)

// Grammar is the program syntax as a GBNF grammar (llama.cpp's form, its start rule root), for a
// model decoding under it to write only text that parses (#323). It covers jaala's own spelling, the
// one the docs and the printer use: clauses separated by "; ", "not" for negation, the answer after
// " => ". It leaves out the forms jaala reads only so people and agents can write other Datalogs
// (comments, a "." ending, "?-", "!atom", #276), and allows a single space or newline where the
// spelling has room for one, so a model can't spend its tokens on blank space.
//
// Every text the grammar accepts parses; not every text that parses is in the grammar. Checking the
// program (its names, arities and bindings) is still Validate's.
func Grammar() string { return grammarText() }

var grammarText = sync.OnceValue(func() string { return render(programRules()) })

// A grammar is rules in the order they are written; the first is root. internal/gbnf renders and
// samples it (table converts it), as it does the Cypher front end's (#354).
type rule struct {
	name string
	e    gexpr
}

// gexpr is one of lit, ref, class, seq, alt and rep.
type gexpr any

type (
	lit   string // the text itself
	ref   string // another rule
	class struct {
		spec   string // GBNF's class without brackets: a-z, ^"\\
		sample string // the characters sampling draws from, all inside spec
	}
	seq []gexpr
	alt []gexpr
	rep struct {
		e        gexpr
		min, max int // max -1 is unbounded
	}
)

func opt(e gexpr) rep  { return rep{e, 0, 1} }
func star(e gexpr) rep { return rep{e, 0, -1} }
func plus(e gexpr) rep { return rep{e, 1, -1} }

// table is rules as internal/gbnf's.
func table(rules []rule) []gbnf.Rule {
	out := make([]gbnf.Rule, len(rules))
	for i, r := range rules {
		out[i] = gbnf.Rule{Name: r.name, E: toGBNF(r.e)}
	}
	return out
}

func toGBNF(e gexpr) gbnf.Expr {
	switch e := e.(type) {
	case lit:
		return gbnf.Lit(e)
	case ref:
		return gbnf.Ref(e)
	case class:
		return gbnf.Class{Spec: e.spec, Sample: e.sample}
	case seq:
		out := make(gbnf.Seq, len(e))
		for i, x := range e {
			out[i] = toGBNF(x)
		}
		return out
	case alt:
		out := make(gbnf.Alt, len(e))
		for i, x := range e {
			out[i] = toGBNF(x)
		}
		return out
	case rep:
		return gbnf.Rep{E: toGBNF(e.e), Min: e.min, Max: e.max}
	}
	panic(fmt.Sprintf("nl: no gexpr %T", e))
}

func render(rules []rule) string { return gbnf.Render(table(rules)) }

// nameMax caps a name's length (an ident, a variable's name, a unit), so a model that wants to write
// prose can't run one name to its token limit (#340); every program the tests and docs write uses
// far shorter ones.
const nameMax = 32

// upTo is e at most n times.
func upTo(e gexpr, n int) rep { return rep{e, 0, n} }

// list is e, then any more of e each after sep.
func list(e gexpr, sep string) seq { return seq{e, star(seq{lit(sep), e})} }

const (
	upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower   = "abcdefghijklmnopqrstuvwxyz"
	digits  = "0123456789"
	letters = lower + upper
)

func programRules() []rule {
	return []rule{
		{"root", seq{star(seq{ref("rule"), ref("sep")}), ref("goal")}},
		{"sep", alt{lit("; "), lit(";\n")}},
		{"rule", seq{ref("head"), lit(" :- "), ref("literals")}},
		{"head", seq{ref("relation"), lit("("), opt(list(ref("harg"), ", ")), lit(")")}},
		{"harg", alt{seq{lit("?"), ref("vname"), opt(seq{lit(": "), ref("decl")})}, ref("constant"), ref("aggregate"), ref("arith")}},
		{"decl", alt{lit("string"), lit("number"), seq{lit("number["), ref("unit"), lit("]")}, ref("ident"), seq{ref("ident"), lit(" "), ref("domain")}, ref("domain")}},
		{"domain", seq{lit("{"), list(ref("string"), ", "), lit("}")}},
		{"literals", seq{ref("literal"), star(seq{alt{lit(", "), lit(",\n  ")}, ref("literal")})}},
		{"literal", alt{ref("fact"), seq{lit("not "), ref("atom")}, ref("comparison"), ref("bodyagg")}},
		{"fact", seq{ref("atom"), opt(seq{lit(" as ?"), ref("vname")})}}, // an atom binding its fact (#279)
		{"atom", seq{ref("relation"), lit("("), opt(list(ref("term"), ", ")), lit(")")}},
		{"comparison", seq{ref("sum"), lit(" "), ref("cmpop"), lit(" "), ref("sum")}},
		{"cmpop", alt{lit("<="), lit(">="), lit("!="), lit("<"), lit(">"), lit("=")}},
		{"sum", seq{ref("product"), star(seq{alt{lit(" + "), lit(" - ")}, ref("product")})}},
		{"product", seq{ref("unary"), star(seq{alt{lit(" * "), lit(" / ")}, ref("unary")})}},
		{"unary", alt{ref("operand"), seq{lit("-"), ref("variable")}, seq{lit("-"), ref("call")}, seq{lit("-("), ref("sum"), lit(")")}, seq{lit("("), ref("sum"), lit(")")}, ref("call")}},
		{"operand", alt{ref("variable"), ref("string"), ref("unumber")}},
		{"call", seq{ref("callname"), lit("("), list(ref("sum"), ", "), lit(")")}},
		{"callname", seq{ref("ident"), star(seq{lit("."), ref("ident")})}},
		// A head argument or an answer column that computes (#206). An undotted call there is an
		// aggregate's spelling, so a call has to be dotted, and a term alone, or negated in brackets
		// (-(9)), reads as a term.
		{"arith", alt{
			seq{ref("product"), plus(seq{alt{lit(" + "), lit(" - ")}, ref("product")})},
			seq{ref("unary"), plus(seq{alt{lit(" * "), lit(" / ")}, ref("unary")})},
			seq{lit("-"), ref("variable")}, seq{lit("-"), ref("call")},
			ref("dcall"),
		}},
		{"dcall", seq{ref("ident"), plus(seq{lit("."), ref("ident")}), lit("("), list(ref("sum"), ", "), lit(")")}},
		{"bodyagg", seq{lit("?"), ref("vname"), lit(" = "), ref("aggregate"), lit(" : { "), list(ref("inner"), ", "), lit(" }")}},
		{"inner", alt{ref("fact"), seq{lit("not "), ref("atom")}, ref("comparison")}},
		{"aggregate", seq{ref("aggfn"), lit("("), opt(lit("distinct ")), lit("?"), ref("vname"), lit(")")}},
		{"aggfn", alt{lit("count"), lit("min"), lit("max"), lit("sum"), lit("list")}},
		{"goal", seq{ref("literals"), opt(seq{lit(" => "), list(ref("column"), ", "), ref("tail")})}},
		{"column", alt{seq{lit("?"), ref("vname")}, ref("aggregate"), ref("arith")}},
		{"tail", seq{
			opt(seq{lit(" having "), list(ref("having"), ", ")}),
			opt(seq{lit(" order by "), list(ref("order"), ", ")}),
			opt(seq{lit(" after "), list(ref("constant"), ", ")}),
			opt(seq{lit(" limit "), class{"1-9", "123"}, rep{class{"0-9", digits}, 0, 5}}),
			opt(seq{lit(" offset "), class{"0-9", digits}, rep{class{"0-9", digits}, 0, 5}}),
		}},
		{"having", seq{ref("aggregate"), lit(" "), ref("cmpop"), lit(" "), ref("term")}},
		{"order", seq{ref("column"), opt(alt{lit(" asc"), lit(" desc")})}},
		{"term", alt{ref("variable"), ref("constant")}},
		{"variable", alt{seq{lit("?"), ref("vname")}, lit("_")}},
		// ?_ is the wildcard _, so a name is never _ alone.
		{"vname", alt{seq{class{"a-zA-Z0-9", "abnxyz019"}, upTo(class{"a-zA-Z0-9_", "abnxyz019_"}, nameMax-1)}, seq{lit("_"), rep{class{"a-zA-Z0-9_", "abxy0_"}, 1, nameMax - 1}}}},
		{"constant", alt{ref("string"), ref("unumber"), seq{lit("-"), ref("unumber")}}},
		// A number's unit is letters straight after it, and e alone would be an exponent left unfinished.
		{"unumber", seq{plus(class{"0-9", digits}), opt(seq{lit("."), plus(class{"0-9", digits})}),
			opt(seq{class{"eE", "eE"}, opt(class{"-+", "-+"}), class{"0-9", digits}, opt(class{"0-9", digits})}), opt(ref("unit"))}},
		{"unit", alt{seq{class{"a-dA-Df-zF-Z", "AVkmsx"}, upTo(class{"a-zA-Z", "AVhmo"}, nameMax-1)}, seq{class{"eE", "eE"}, rep{class{"a-zA-Z", "AVhmo"}, 1, nameMax - 1}}}},
		// A string keeps a backslash and the character after it, a quote included.
		{"string", seq{lit(`"`), star(alt{class{`^"\\\x00-\x1F`, "ab c:,()=#'"}, seq{lit(`\`), class{`^\x00-\x1F`, `"\n`}}}), lit(`"`)}},
		{"relation", seq{ref("ident"), star(seq{alt{lit("."), lit("-")}, ref("ident")})}},
		{"ident", seq{class{"a-zA-Z", "abeLnrs"}, upTo(class{"a-zA-Z0-9_", "abdeinorst0_"}, nameMax-1)}},
	}
}
