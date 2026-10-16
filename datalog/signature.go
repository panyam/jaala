package datalog

import (
	"github.com/panyam/jaala/ns"
	"slices"
	"sort"
	"strings"
)

// parseArgType reads a declaration, the text after ":" in a rule head argument:
//
//	decl   = "?" ident                       (* kind from that argument, per row *)
//	       | "string" | "number" [ "[" unit "]" ]
//	       | ident [ "(" "?" ident ")" ]     (* entity kind, optionally located by an owner *)
//	       | vocab ;
//	vocab  = "{" string { "," string } "}" ;  (* may also follow any of the above *)
//
// "string" and "number" are therefore not available as entity kinds.
func parseArgType(sp span) (ns.ArgType, error) {
	sp = sp.trim()
	s := sp.text()
	var t ns.ArgType
	if i := sp.find("{"); i >= 0 {
		if !sp.is(sp.hi-sp.lo-1, "}") {
			return t, sp.errorf("query: unterminated vocabulary in %q", s)
		}
		for _, item := range sp.tokens(i+1, sp.hi-sp.lo-1).split(",") {
			item = item.trim()
			if toks := item.toks(); len(toks) != 1 || toks[0].kind != tokString || !toks[0].flag {
				return t, item.errorf("query: vocabulary entry %q must be a \"string\"", item.text())
			}
			t.Domain = append(t.Domain, item.text()[1:len(item.text())-1])
		}
		s = sp.before(i).text()
		if s == "" {
			t.Type = ns.TypeString
			return t, nil
		}
	}
	switch {
	case s == "":
		return t, sp.errorf("query: empty type declaration")
	case s[0] == '?':
		if !isIdent(s[1:]) {
			return t, sp.errorf("query: bad kind variable %q", s)
		}
		t.KindFrom = s[1:]
	case s == ns.TypeString || s == ns.TypeNumber:
		t.Type = s
	case strings.HasPrefix(s, ns.TypeNumber+"[") && strings.HasSuffix(s, "]"):
		t.Type, t.Unit = ns.TypeNumber, strings.TrimSpace(s[len(ns.TypeNumber)+1:len(s)-1])
	case strings.HasSuffix(s, ")"):
		open := strings.IndexByte(s, '(')
		owner := strings.TrimSpace(s[open+1 : len(s)-1])
		if open < 0 || len(owner) < 2 || owner[0] != '?' || !isIdent(owner[1:]) || !isIdent(strings.TrimSpace(s[:open])) {
			return t, sp.errorf("query: bad type declaration %q (an owner is written kind(?var))", s)
		}
		t.Kind, t.Owner = strings.TrimSpace(s[:open]), owner[1:]
	case isIdent(s):
		t.Kind = s
	default:
		return t, sp.errorf("query: bad type declaration %q", s)
	}
	return t, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// unionSorted merges vocabularies, sorted and deduped.
func unionSorted(a, b []string) []string {
	out := append(append([]string(nil), a...), b...)
	sort.Strings(out)
	return slices.Compact(out)
}
