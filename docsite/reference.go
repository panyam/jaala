package main

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/panyam/jaala/docsite/demo"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// datasetURL is where a fixture is shown, which every mention of it links to.
func datasetURL(name string) string {
	return PathPrefix + "/reference/datasets/#" + name
}

// datasetHTML is the `dataset` template function: a fixture's relations as tables, with the facts as
// written under a fold. Written as one HTML block with no blank line, since markdown would end the
// block there.
func datasetHTML(name string) template.HTML {
	d, err := demo.LoadDataset(name)
	if err != nil {
		failDemo("dataset "+name, err)
		return template.HTML(`<p class="demo-error">` + html.EscapeString(err.Error()) + `</p>`) // #nosec G203 -- fixed markup around escaped error text
	}
	var b strings.Builder
	b.WriteString(`<div class="dataset">`)
	b.WriteString(`<p>` + html.EscapeString(d.Description) + `</p>`)
	for _, r := range d.Relations {
		fmt.Fprintf(&b, `<h3 id="%s-%s"><code>%s(%s)</code></h3>`, name, r.Name,
			html.EscapeString(r.Name), html.EscapeString(strings.Join(r.Columns, ", ")))
		b.WriteString(`<div class="demo-answer"><table><thead><tr>`)
		for _, c := range r.Columns {
			b.WriteString(`<th>` + html.EscapeString(c) + `</th>`)
		}
		b.WriteString(`</tr></thead><tbody>`)
		for _, f := range r.Facts {
			b.WriteString(`<tr>`)
			for _, v := range f.Args {
				b.WriteString(`<td>` + html.EscapeString(v.S) + `</td>`)
			}
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}
	b.WriteString(`<details><summary>The facts as written</summary><pre><code>` + preText(strings.TrimSpace(d.Text)) + `</code></pre></details>`)
	b.WriteString(`</div>`)
	return template.HTML(b.String()) // #nosec G203 -- built above from fixed markup and escaped dataset text
}

// parserFile is the parser whose doc comment holds the grammar the reference shows.
const parserFile = "../datalog/parse.go"

// grammar is the `grammar` template function: the EBNF from the comment on datalog.Parse, so the
// reference shows the parser's own grammar and has nothing to keep in step with it.
func grammar() string {
	g, err := extractGrammar(filepath.Join(projectRoot, parserFile))
	if err != nil {
		failDemo("grammar", err)
		return err.Error()
	}
	return g
}

// extractGrammar reads the indented block after "Grammar (EBNF" in a Go file's comments.
func extractGrammar(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a parser file in this repo, read at build time
	if err != nil {
		return "", err
	}
	var out []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case !in && strings.Contains(line, "Grammar (EBNF"):
			in = true
		case in && strings.HasPrefix(line, "//\t"):
			out = append(out, strings.TrimPrefix(line, "//\t"))
		case in && len(out) > 0:
			return strings.Join(out, "\n"), nil
		}
	}
	return "", fmt.Errorf("no grammar found in %s", path)
}

var backticks = regexp.MustCompile("`([^`]+)`")

// builtinsHTML is the `builtins` template function: every name the standard library registers, with
// its signature and doc, read from the registry, so the list can't miss one.
func builtinsHTML() template.HTML {
	entries, err := builtins()
	if err != nil {
		failDemo("builtins", err)
		return template.HTML(`<p class="demo-error">` + html.EscapeString(err.Error()) + `</p>`) // #nosec G203 -- fixed markup around escaped error text
	}
	var b strings.Builder
	b.WriteString(`<div class="demo-answer"><table><thead><tr><th>Name and arguments</th><th>What it does</th></tr></thead><tbody>`)
	for _, e := range entries {
		doc := backticks.ReplaceAllString(html.EscapeString(e.Doc), "<code>$1</code>")
		fmt.Fprintf(&b, `<tr><td><code>%s</code></td><td>%s</td></tr>`, html.EscapeString(e.Signature()), doc)
	}
	b.WriteString(`</tbody></table></div>`)
	return template.HTML(b.String()) // #nosec G203 -- built above from fixed markup and escaped builtin docs
}

// builtins is every member the standard library registers on an empty vocabulary, walking modules.
func builtins() ([]ns.Entry, error) {
	v, err := ns.NewVocabulary(nil)
	if err != nil {
		return nil, err
	}
	if err := stdlib.Register(v); err != nil {
		return nil, err
	}
	var out []ns.Entry
	var walk func(path string) error
	walk = func(path string) error {
		e, err := v.Lookup(path)
		if err != nil {
			return err
		}
		if e.Kind != ns.EntryModule {
			out = append(out, e)
			return nil
		}
		for _, m := range e.Members {
			if err := walk(m); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk("")
}
