package gmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// ToolHandler executes a tool call. The returned value is serialized as JSON
// text; a string is returned verbatim.
type ToolHandler func(context.Context, json.RawMessage) (any, error)

type serverTool struct {
	tool    Tool
	handler ToolHandler
}

// Server is an MCP server over a newline-delimited JSON-RPC stream.
type Server struct {
	info            ServerInfo
	protocolVersion string

	mu    sync.RWMutex
	tools map[string]serverTool
}

// NewServer returns a server with the given name.
func NewServer(name string) *Server {
	return &Server{
		info:            ServerInfo{Name: name},
		protocolVersion: DefaultProtocolVersion,
		tools:           make(map[string]serverTool),
	}
}

// SetVersion sets the server version reported by initialize.
func (s *Server) SetVersion(version string) { s.info.Version = version }

// SetProtocolVersion overrides the advertised protocol version.
func (s *Server) SetProtocolVersion(version string) {
	if strings.TrimSpace(version) != "" {
		s.protocolVersion = version
	}
}

// AddTool registers a tool.
func (s *Server) AddTool(tool Tool, handler ToolHandler) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("gmcp: tool name is required")
	}
	if len(tool.InputSchema) > 0 && !json.Valid(tool.InputSchema) {
		return fmt.Errorf("gmcp: tool %s input schema is not valid JSON", tool.Name)
	}
	if handler == nil {
		return fmt.Errorf("gmcp: tool %s has no handler", tool.Name)
	}
	if len(tool.InputSchema) == 0 {
		tool.InputSchema = json.RawMessage(`{"type":"object"}`)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[tool.Name]; exists {
		return fmt.Errorf("gmcp: duplicate tool %q", tool.Name)
	}
	s.tools[tool.Name] = serverTool{tool: tool, handler: handler}
	return nil
}

// Serve reads requests from r and writes responses to w until r ends or ctx is
// cancelled. Closing the reader is the way to unblock a blocked read.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(w)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var request Request
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			if err := writeResponse(writer, Response{
				JSONRPC: JSONRPCVersion,
				ID:      json.RawMessage("null"),
				Error:   NewError(CodeParseError, "parse error"),
			}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			// Notifications need no response.
			continue
		}

		response := s.handle(ctx, request)
		if err := writeResponse(writer, response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, request Request) Response {
	response := Response{JSONRPC: JSONRPCVersion, ID: request.ID}

	switch request.Method {
	case "initialize":
		params := InitializeParams{}
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				response.Error = NewError(CodeInvalidParams, "invalid initialize params")
				return response
			}
		}
		response.Result = mustJSON(InitializeResult{
			ProtocolVersion: s.protocolVersion,
			Capabilities:    ServerCapabilities{Tools: &ToolsCapability{}},
			ServerInfo:      s.info,
		})

	case "ping":
		response.Result = json.RawMessage(`{}`)

	case "tools/list":
		response.Result = mustJSON(ListToolsResult{Tools: s.listTools()})

	case "tools/call":
		params := CallToolParams{}
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				response.Error = NewError(CodeInvalidParams, "invalid tools/call params")
				return response
			}
		}
		response.Result = s.callTool(ctx, params)

	default:
		response.Error = NewError(CodeMethodNotFound, "method not found: "+request.Method)
	}
	return response
}

func (s *Server) listTools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tools := make([]Tool, 0, len(s.tools))
	for _, entry := range s.tools {
		tools = append(tools, entry.tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func (s *Server) callTool(ctx context.Context, params CallToolParams) json.RawMessage {
	s.mu.RLock()
	entry, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return mustJSON(CallToolResult{
			IsError: true,
			Content: []Content{{Type: "text", Text: "unknown tool: " + params.Name}},
		})
	}

	arguments := params.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return mustJSON(CallToolResult{
			IsError: true,
			Content: []Content{{Type: "text", Text: "arguments are not valid JSON"}},
		})
	}

	result, err := entry.handler(ctx, arguments)
	if err != nil {
		return mustJSON(CallToolResult{
			IsError: true,
			Content: []Content{{Type: "text", Text: err.Error()}},
		})
	}

	text, err := resultText(result)
	if err != nil {
		return mustJSON(CallToolResult{
			IsError: true,
			Content: []Content{{Type: "text", Text: err.Error()}},
		})
	}
	return mustJSON(CallToolResult{Content: []Content{{Type: "text", Text: text}}})
}

func resultText(result any) (string, error) {
	switch value := result.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal tool result: %w", err)
		}
		return string(raw), nil
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("gmcp: marshal %T: %v", value, err))
	}
	return raw
}

func writeResponse(w *bufio.Writer, response Response) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("gmcp: marshal response: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}
