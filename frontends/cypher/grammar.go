package cypher

import (
	"sort"

	"github.com/panyam/jaala/internal/gbnf"
)

// Grammar is the Cypher this package compiles as a GBNF grammar narrowed to g (#354), for a model
// decoding under it to write only queries that parse and name g's labels, relationship types and
// properties: MATCH (with a path variable over one relationship, #280), WHERE, WITH, UNION, EXISTS { },
// and RETURN with its aggregates, ORDER BY, SKIP and
// LIMIT, with OR, XOR, AND and NOT in openCypher's precedence, in one spelling (keywords upper case,
// one space between tokens). It leaves out what a model translating a question has no use for and
// what compiles to no program yet: CALL, a $parameter, a regex, and every construct Compile refuses
// as a gap.
//
// Every text it accepts parses. Not every one compiles: a variable RETURN reads that nothing matched,
// SKIP with no ORDER BY, or an aggregate in WHERE is a mistake only Compile can see, as Validate's
// checks are beyond nl.Grammar.
func Grammar(g Graph) string { return gbnf.Render(grammarRules(g)) }

func grammarRules(g Graph) []gbnf.Rule {
	type (
		lit = gbnf.Lit
		ref = gbnf.Ref
		seq = gbnf.Seq
		alt = gbnf.Alt
	)
	opt, star, list := gbnf.Opt, gbnf.Star, gbnf.List
	cls := func(spec, sample string) gbnf.Class { return gbnf.Class{Spec: spec, Sample: sample} }
	names := func(ns []string) gbnf.Expr {
		if len(ns) == 0 {
			return lit("_")
		}
		a := alt{}
		for _, n := range ns {
			a = append(a, lit(n))
		}
		return a
	}
	keys := map[string]bool{}
	for _, n := range g.Nodes {
		for k := range n.Props {
			keys[k] = true
		}
	}
	for k := range g.Derived {
		keys[k] = true
	}
	var keyList []string
	for k := range keys {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)
	pkey := names(keyList)
	if g.Props != nil || len(keyList) == 0 {
		// any key the Props relation's facts may name, beside the ones the graph lists
		pkey = alt{pkey, seq{cls("a-z", "kv"), gbnf.UpTo(cls("a-z_", "ey_"), 15)}}
	}
	return []gbnf.Rule{
		{Name: "root", E: seq{ref("query"), star(seq{alt{lit(" UNION "), lit(" UNION ALL ")}, ref("query")})}},
		{Name: "query", E: seq{ref("match"), star(seq{lit(" "), ref("omatch")}),
			star(seq{lit(" WITH "), list(ref("witem"), ", "), opt(seq{lit(" WHERE "), ref("expr")}), star(seq{lit(" "), ref("omatch")})}),
			lit(" "), ref("return")}},
		// an OPTIONAL MATCH after a match (#282), which keeps the row with absent where it fails
		{Name: "omatch", E: alt{ref("match"), seq{lit("OPTIONAL "), ref("match")}}},
		{Name: "match", E: seq{lit("MATCH "), list(alt{ref("pattern"), seq{lit("shortestPath("), ref("pattern"), lit(")")}, ref("path")}, ", "), opt(seq{lit(" WHERE "), ref("expr")})}},
		{Name: "pattern", E: seq{ref("node"), gbnf.UpTo(seq{ref("rel"), ref("node")}, 3)}},
		// a path variable binds a path of one relationship, which may have a variable length (#280)
		{Name: "path", E: seq{ref("var"), lit(" = "), alt{ref("hop"), seq{lit("shortestPath("), ref("hop"), lit(")")}}}},
		{Name: "hop", E: seq{ref("node"), ref("rel"), ref("node")}},
		{Name: "node", E: seq{lit("("), opt(ref("var")), opt(seq{lit(":"), ref("label")}), opt(ref("props")), lit(")")}},
		{Name: "props", E: seq{lit(" {"), list(seq{ref("pkey"), lit(": "), ref("literal")}, ", "), lit("}")}},
		{Name: "rel", E: alt{seq{lit("-"), opt(ref("relbody")), lit("->")}, seq{lit("<-"), opt(ref("relbody")), lit("-")}, seq{lit("-"), opt(ref("relbody")), lit("-")}}},
		{Name: "relbody", E: seq{lit("[:"), list(ref("type"), "|:"), opt(ref("range")), lit("]")}},
		{Name: "range", E: seq{lit("*"), opt(seq{ref("bound"), opt(seq{lit(".."), ref("bound")})})}},
		{Name: "bound", E: seq{cls("0-9", "0123"), gbnf.UpTo(cls("0-9", "0"), 1)}},
		{Name: "witem", E: alt{ref("var"), seq{alt{ref("expr"), ref("aggregate")}, lit(" AS "), ref("var")}}},
		{Name: "return", E: seq{lit("RETURN "), opt(lit("DISTINCT ")), list(ref("ritem"), ", "),
			opt(seq{lit(" ORDER BY "), list(seq{ref("expr"), opt(lit(" DESC"))}, ", ")}),
			opt(seq{lit(" SKIP "), ref("int")}), opt(seq{lit(" LIMIT "), ref("int")})}},
		{Name: "ritem", E: seq{alt{ref("expr"), ref("aggregate")}, opt(seq{lit(" AS "), ref("var")})}},
		{Name: "aggregate", E: alt{lit("count(*)"), seq{ref("aggfn"), lit("("), opt(lit("DISTINCT ")), ref("expr"), lit(")")}}},
		{Name: "aggfn", E: alt{lit("count"), lit("min"), lit("max"), lit("sum"), lit("collect")}},
		{Name: "expr", E: list(ref("excl"), " OR ")},
		{Name: "excl", E: list(ref("conj"), " XOR ")},
		{Name: "conj", E: list(ref("neg"), " AND ")},
		{Name: "neg", E: seq{opt(lit("NOT ")), ref("cmp")}},
		{Name: "cmp", E: seq{ref("sum"), opt(alt{
			seq{ref("cmpop"), ref("sum")},
			seq{alt{lit(" STARTS WITH "), lit(" ENDS WITH "), lit(" CONTAINS ")}, ref("sum")},
			lit(" IS NULL"), lit(" IS NOT NULL"),
			seq{lit(" IN ["), list(ref("literal"), ", "), lit("]")},
			seq{lit(" IN nodes("), ref("var"), lit(")")},
		})}},
		{Name: "cmpop", E: alt{lit(" = "), lit(" <> "), lit(" < "), lit(" <= "), lit(" > "), lit(" >= ")}},
		{Name: "sum", E: seq{ref("product"), star(seq{alt{lit(" + "), lit(" - ")}, ref("product")})}},
		{Name: "product", E: seq{ref("unary"), star(seq{alt{lit(" * "), lit(" / ")}, ref("unary")})}},
		{Name: "unary", E: seq{opt(lit("-")), ref("primary")}},
		{Name: "primary", E: alt{
			seq{ref("var"), lit("."), ref("pkey")},
			ref("var"),
			seq{ref("var"), lit(":"), ref("label")},
			ref("literal"),
			ref("function"),
			seq{lit("("), ref("expr"), lit(")")},
			seq{lit("EXISTS { MATCH "), ref("pattern"), opt(seq{lit(" WHERE "), ref("expr")}), lit(" }")},
		}},
		{Name: "function", E: alt{
			seq{alt{lit("toLower("), lit("toUpper(")}, ref("expr"), lit(")")},
			seq{lit("replace("), ref("expr"), lit(", "), ref("expr"), lit(", "), ref("expr"), lit(")")},
			seq{lit("split("), ref("expr"), lit(", "), ref("expr"), lit(")["), cls("0-9", "01"), lit("]")},
			seq{alt{lit("length("), lit("nodes("), lit("relationships(")}, ref("var"), lit(")")},
		}},
		{Name: "literal", E: alt{ref("string"), ref("number"), lit("true"), lit("false")}},
		{Name: "string", E: seq{lit("'"), gbnf.UpTo(cls(`^'\\\x00-\x1F`, "ab c-1"), 40), lit("'")}},
		{Name: "number", E: seq{ref("int"), opt(seq{lit("."), gbnf.Plus(cls("0-9", "05"))})}},
		{Name: "int", E: seq{cls("0-9", "123"), gbnf.UpTo(cls("0-9", "0"), 5)}},
		// A variable starts lower case, and every one sampling draws starts with a, b or n so that
		// none is a keyword and enough of them name what an earlier clause matched.
		{Name: "var", E: seq{cls("a-z", "abn"), gbnf.UpTo(cls("a-zA-Z0-9_", "1"), 15)}},
		{Name: "label", E: names(g.labelNames())},
		{Name: "type", E: names(g.typeNames())},
		{Name: "pkey", E: pkey},
	}
}
