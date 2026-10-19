package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
)

// A call is one tool call: the server's tools with the pack its Run gives, if any, the Run, and the
// longest the call may take, 0 for no limit.
type call struct {
	tools
	run     Run
	timeout time.Duration
}

// readsVocabulary are the tools that read the vocabulary itself rather than a Base, which wait for
// Options.Vocabulary on a server given none up front.
var readsVocabulary = map[string]bool{"schema": true, "ask": true, "cypher": true, "look": true, "values": true}

// tool is the tool name taking In's arguments and A's beside them, flat in one schema allowing no
// others. Go can't embed a type parameter, so the handler reads the arguments twice, into In and
// into A, asks x.Prepare how the call runs, and hands handle the call and In.
func tool[In, A any](t *tools, x Extension[A], host object, name, desc string, handle func(*call, core.ToolContext, In) (core.ToolResult, error)) (core.TypedToolResult, error) {
	own, err := objectOf(core.GenerateSchema[In]())
	if err != nil {
		return core.TypedToolResult{}, err
	}
	for _, n := range host.names {
		if _, ok := own.props[n]; ok {
			return core.TypedToolResult{}, fmt.Errorf("mcp: the host's argument %q is one of %s's own", n, name)
		}
	}
	h := func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		args := req.Arguments
		if len(bytes.TrimSpace(args)) == 0 {
			args = json.RawMessage("{}")
		}
		var in In
		var a A
		if err := json.Unmarshal(args, &in); err != nil {
			return core.ErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return core.ErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		c := &call{tools: *t, timeout: t.o.Timeout}
		if c.timeout > 0 {
			var cancel context.CancelFunc
			ctx.Context, cancel = context.WithTimeout(ctx.Context, c.timeout)
			defer cancel()
		}
		if x.Prepare != nil {
			r, err := x.Prepare(ctx, name, a)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			c.run = r
			if r.Pack != nil {
				c.p = *r.Pack
			}
			if r.Timeout > 0 && (c.timeout == 0 || r.Timeout < c.timeout) {
				var cancel context.CancelFunc
				ctx.Context, cancel = context.WithTimeout(ctx.Context, r.Timeout)
				defer cancel()
				c.timeout = r.Timeout
			}
		}
		if readsVocabulary[name] {
			v, err := t.vocab.get(ctx)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			c.v = v
		}
		res, err := handle(c, ctx, in)
		return res, err
	}
	def := core.ToolDef{Name: name, Description: desc, InputSchema: own.merge(host)}
	return core.TypedToolResult{ToolDef: def, Handler: h}, nil
}

// An object is a JSON Schema of an object, as mcpkit generates one from a struct: its properties
// in the struct's order, and those it requires.
type object struct {
	dialect  string
	names    []string
	props    map[string]json.RawMessage
	required []string
}

func objectOf(raw json.RawMessage) (object, error) {
	var s struct {
		Dialect    string          `json:"$schema"`
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return object{}, err
	}
	if s.Type != "object" {
		return object{}, fmt.Errorf("want a struct, whose schema is an object, not %s", raw)
	}
	o := object{dialect: s.Dialect, props: map[string]json.RawMessage{}, required: s.Required}
	if len(s.Properties) == 0 {
		return o, nil
	}
	// A map would lose the struct's order, which is the order a client lists the arguments in.
	d := json.NewDecoder(bytes.NewReader(s.Properties))
	if _, err := d.Token(); err != nil {
		return object{}, err
	}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return object{}, err
		}
		name, ok := tok.(string)
		if !ok {
			return object{}, errors.New("a property without a name")
		}
		var p json.RawMessage
		if err := d.Decode(&p); err != nil {
			return object{}, err
		}
		o.names = append(o.names, name)
		o.props[name] = p
	}
	return o, nil
}

// merge is o's properties then host's, requiring what either requires and allowing nothing else.
func (o object) merge(host object) json.RawMessage {
	var b bytes.Buffer
	b.WriteString("{")
	if o.dialect != "" {
		d, _ := json.Marshal(o.dialect)
		fmt.Fprintf(&b, `"$schema":%s,`, d)
	}
	b.WriteString(`"type":"object","properties":{`)
	var props []string
	for _, s := range []object{o, host} {
		for _, n := range s.names {
			k, _ := json.Marshal(n)
			props = append(props, string(k)+":"+string(s.props[n]))
		}
	}
	b.WriteString(strings.Join(props, ","))
	b.WriteString("}")
	if req := append(append([]string{}, o.required...), host.required...); len(req) > 0 {
		r, _ := json.Marshal(req)
		fmt.Fprintf(&b, `,"required":%s`, r)
	}
	b.WriteString(`,"additionalProperties":false}`)
	return b.Bytes()
}
