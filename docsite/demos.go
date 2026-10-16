package main

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/panyam/jaala/docsite/demo"
	"gopkg.in/yaml.v3"
)

// demoFailures records every example that didn't run as its spec says, so main fails the build. A
// template function's error alone would only blank the page, and s3gen would still exit 0.
var (
	demoFailuresMu sync.Mutex
	demoFailures   []string
)

func failDemo(path string, err error) {
	demoFailuresMu.Lock()
	defer demoFailuresMu.Unlock()
	demoFailures = append(demoFailures, fmt.Sprintf("%s: %v", path, err))
}

// loadSpec reads a docsite-relative demo spec.
func loadSpec(path string) (demo.Spec, error) {
	data, ok := readDocsiteFile(path)
	if !ok {
		return demo.Spec{}, fmt.Errorf("no demo spec at %s", path)
	}
	var s demo.Spec
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return demo.Spec{}, fmt.Errorf("%s: %v", path, err)
	}
	return s, nil
}

// demoHTML is the `demo` template function: {{ demo "demos/overview/reach.yaml" }} renders the
// example's rules and goal and the answer jaala gives them, computed now, at build time.
func demoHTML(path string) template.HTML {
	s, err := loadSpec(path)
	if err != nil {
		failDemo(path, err)
		return template.HTML(`<p class="demo-error">` + html.EscapeString(err.Error()) + `</p>`)
	}
	t, runErr := demo.Run(s)
	if err := demo.Check(s, t, runErr); err != nil {
		failDemo(path, err)
	}
	return template.HTML(renderDemo(path, s, t, runErr))
}

// renderDemo writes the example as one raw-HTML block with no blank line in it, because markdown
// ends an HTML block at the first blank line. Newlines inside <pre> are written as &#10; for the same
// reason.
func renderDemo(path string, s demo.Spec, t demo.Table, runErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="demo" data-demo="%s" data-spec="%s">`, html.EscapeString(path), html.EscapeString(editableSpec(s)))
	source := strings.TrimSpace(strings.TrimSpace(s.Program) + "\n" + strings.TrimSpace(s.Query))
	b.WriteString(`<pre class="demo-source"><code>` + preText(source) + `</code></pre>`)
	b.WriteString(`<div class="demo-answer">`)
	switch {
	case runErr != nil:
		b.WriteString(`<p class="demo-error">` + html.EscapeString(runErr.Error()) + `</p>`)
	case len(t.Rows) == 0:
		b.WriteString(`<p class="demo-empty">No rows.</p>`)
	default:
		b.WriteString(`<table><thead><tr>`)
		for _, c := range t.Columns {
			b.WriteString(`<th>` + html.EscapeString(c) + `</th>`)
		}
		if s.Cites {
			b.WriteString(`<th class="demo-cites">cites</th>`)
		}
		b.WriteString(`</tr></thead><tbody>`)
		for i, r := range t.Rows {
			b.WriteString(`<tr>`)
			for _, v := range r {
				b.WriteString(`<td>` + html.EscapeString(v) + `</td>`)
			}
			if s.Cites {
				b.WriteString(`<td class="demo-cites">` + html.EscapeString(strings.Join(t.Cites[i], ", ")) + `</td>`)
			}
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</div>`)
	if caption := factsCaption(s); caption != "" {
		b.WriteString(`<figcaption>` + caption + `</figcaption>`)
	}
	b.WriteString(`</figure>`)
	return b.String()
}

// editableSpec is the spec the in-page editor starts from (static/js/demos.js): what the example runs,
// without what it pins, since an edited example has no expectation to meet.
func editableSpec(s demo.Spec) string {
	s.Expect, s.ExpectError = nil, ""
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func factsCaption(s demo.Spec) string {
	var parts []string
	if s.Fixture != "" {
		parts = append(parts, `Facts: the <a href="`+datasetURL(s.Fixture)+`"><code>`+html.EscapeString(s.Fixture)+`</code></a> dataset`)
	}
	if strings.TrimSpace(s.Facts) != "" {
		facts := strings.TrimSpace(s.Facts)
		if s.Fixture != "" {
			parts = append(parts, `plus <code>`+preText(facts)+`</code>`)
		} else {
			parts = append(parts, `Facts: <code>`+preText(facts)+`</code>`)
		}
	}
	if s.FoldSourceUnits && len(parts) > 0 {
		parts = append(parts, `read with their units folded (<code>FoldSourceUnits</code>)`)
	}
	return strings.Join(parts, " ")
}

func preText(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "&#10;")
}

// exitOnDemoFailures ends a build that rendered an example which didn't run as its spec says.
func exitOnDemoFailures() {
	demoFailuresMu.Lock()
	defer demoFailuresMu.Unlock()
	if len(demoFailures) == 0 {
		return
	}
	for _, f := range demoFailures {
		log.Printf("demo failed: %s", f)
	}
	os.Exit(1)
}
