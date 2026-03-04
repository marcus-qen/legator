package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FunctionDef is an OpenAI-compatible function definition for LLM tool calling.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// LLMTool is a single tool in OpenAI function-calling format.
type LLMTool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// LLMToolCall represents a function call requested by the LLM.
type LLMToolCall struct {
	// QualifiedName is "<server>/<tool>".
	QualifiedName string
	// Arguments are the parsed tool arguments.
	Arguments map[string]any
}

// LLMToolResult is the result returned to the LLM after a tool call.
type LLMToolResult struct {
	QualifiedName string `json:"name"`
	Content       string `json:"content"`
	IsError       bool   `json:"is_error,omitempty"`
}

// Bridge converts between MCP tool definitions and LLM function-calling format
// and routes LLM tool calls to the appropriate MCP server.
type Bridge struct {
	registry *Registry
}

// NewBridge creates a new LLM bridge backed by the given registry.
func NewBridge(registry *Registry) *Bridge {
	return &Bridge{registry: registry}
}

// LLMTools returns all known MCP tools in OpenAI function-calling format.
// Tool names use the qualified "<server>_<tool>" format (slash replaced with
// underscore for LLM compatibility).
func (b *Bridge) LLMTools(ctx context.Context) ([]LLMTool, error) {
	entries, err := b.registry.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]LLMTool, 0, len(entries))
	for _, e := range entries {
		schema, err := schemaToRaw(e.Tool.InputSchema)
		if err != nil {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		llmName := qualifiedToLLMName(e.QualifiedName)
		desc := e.Tool.Description
		if desc == "" {
			desc = fmt.Sprintf("Tool %s on MCP server %s", e.Tool.Name, e.Server)
		}
		out = append(out, LLMTool{
			Type: "function",
			Function: FunctionDef{
				Name:        llmName,
				Description: desc,
				Parameters:  schema,
			},
		})
	}
	return out, nil
}

// Invoke executes an LLM tool call against the appropriate MCP server.
// The qualifiedName should be in LLM format ("<server>_<tool>") or MCP format ("<server>/<tool>").
func (b *Bridge) Invoke(ctx context.Context, call LLMToolCall) (*LLMToolResult, error) {
	qn := llmNameToQualified(call.QualifiedName)
	server, tool, err := splitQualifiedName(qn)
	if err != nil {
		// try as-is
		qn = call.QualifiedName
		server, tool, err = splitQualifiedName(qn)
		if err != nil {
			return nil, err
		}
	}

	res, err := b.registry.CallTool(ctx, server, tool, call.Arguments)
	if err != nil {
		return &LLMToolResult{
			QualifiedName: qn,
			Content:       err.Error(),
			IsError:       true,
		}, nil
	}

	content := contentToText(res)
	return &LLMToolResult{
		QualifiedName: qn,
		Content:       content,
		IsError:       res.IsError,
	}, nil
}

// qualifiedToLLMName converts "server/tool" → "server_tool" for LLM compatibility.
func qualifiedToLLMName(qn string) string {
	result := make([]byte, len(qn))
	for i := 0; i < len(qn); i++ {
		if qn[i] == '/' {
			result[i] = '_'
		} else {
			result[i] = qn[i]
		}
	}
	return string(result)
}

// llmNameToQualified converts "server_tool" → "server/tool".
// Only the first underscore is treated as the server/tool separator.
func llmNameToQualified(name string) string {
	for i, ch := range name {
		if ch == '_' {
			return name[:i] + "/" + name[i+1:]
		}
	}
	return name
}

// schemaToRaw marshals an arbitrary schema value to json.RawMessage.
func schemaToRaw(schema any) (json.RawMessage, error) {
	if schema == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	switch v := schema.(type) {
	case json.RawMessage:
		return v, nil
	case []byte:
		return json.RawMessage(v), nil
	default:
		b, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
}

// contentToText extracts a plain text string from MCP CallToolResult content.
// It concatenates all TextContent items; for other content types it JSON-marshals.
func contentToText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		switch tc := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, tc.Text)
		default:
			if raw, err := json.Marshal(c); err == nil {
				parts = append(parts, string(raw))
			}
		}
	}
	if len(parts) == 0 {
		raw, _ := json.MarshalIndent(res, "", "  ")
		return string(raw)
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}
