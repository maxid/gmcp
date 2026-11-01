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

// SubscriptionHandler receives a subscription after the server acknowledged it.
// It runs in its own goroutine and should return when the subscription ends.
type SubscriptionHandler func(*Subscription)

type serverTool struct {
	tool    Tool
	handler ToolHandler
}

// Server is an MCP server over a newline-delimited JSON-RPC stream.
//
// It serves both protocol eras: a request whose params carry modern _meta is
// answered statelessly, an initialize request opens the legacy handshake.
type Server struct {
	info         Implementation
	versions     []string
	instructions string
	cacheScope   string
	ttlMs        int64
	onSubscribe  SubscriptionHandler

	mu    sync.RWMutex
	tools map[string]serverTool
	subs  map[string]*Subscription
}

// NewServer returns a server with the given name.
func NewServer(name string) *Server {
	return &Server{
		info:       Implementation{Name: name, Version: Version},
		versions:   append([]string(nil), DefaultSupportedVersions...),
		cacheScope: "public",
		ttlMs:      60000,
		tools:      make(map[string]serverTool),
		subs:       make(map[string]*Subscription),
	}
}

// SetVersion sets the server version reported to clients.
func (s *Server) SetVersion(version string) { s.info.Version = version }

// SetProtocolVersions sets the revisions the server serves. Modern revisions
// are answered through _meta, legacy ones through initialize.
func (s *Server) SetProtocolVersions(versions []string) {
	if len(versions) == 0 {
		return
	}
	s.versions = append([]string(nil), versions...)
}

// SetInstructions sets the natural-language guidance returned by server/discover.
func (s *Server) SetInstructions(instructions string) { s.instructions = instructions }

// SetDiscoverCacheScope sets the cacheScope of the DiscoverResult: "public"
// when the answer carries no user-specific data, "private" otherwise.
func (s *Server) SetDiscoverCacheScope(scope string) {
	if scope == "public" || scope == "private" {
		s.cacheScope = scope
	}
}

// SetDiscoverTTL sets how long clients may cache a DiscoverResult, in milliseconds.
func (s *Server) SetDiscoverTTL(ttlMs int64) {
	if ttlMs >= 0 {
		s.ttlMs = ttlMs
	}
}

// SetSubscriptionHandler installs the handler that owns subscriptions opened by
// subscriptions/listen. Without one, subscriptions are acknowledged and closed.
func (s *Server) SetSubscriptionHandler(handler SubscriptionHandler) {
	s.onSubscribe = handler
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

// RemoveTool drops a registered tool. It does not notify subscribers; call
// ToolsChanged after a batch of changes.
func (s *Server) RemoveTool(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tools[name]; !ok {
		return false
	}
	delete(s.tools, name)
	return true
}

// ToolsChanged notifies every open subscription that subscribes to tool list
// changes.
func (s *Server) ToolsChanged() {
	s.mu.RLock()
	subs := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		if sub.filter.ToolsListChanged {
			subs = append(subs, sub)
		}
	}
	s.mu.RUnlock()
	for _, sub := range subs {
		_ = sub.Notify(NotifyToolsListChanged, nil)
	}
}

// Serve reads requests from r and writes responses to w until r ends or ctx is
// cancelled. Closing the reader is the way to unblock a blocked read.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	out := &lockedWriter{w: bufio.NewWriter(w)}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	defer s.closeAllSubscriptions(true)

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
			if err := out.writeMessage(Response{
				JSONRPC: JSONRPCVersion,
				ID:      json.RawMessage("null"),
				Error:   NewError(CodeParseError, "parse error"),
			}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			s.handleNotification(request)
			continue
		}
		if request.Method == "subscriptions/listen" && requestMeta(request.Params) != nil {
			if sub := s.openSubscription(request, out.writeRaw); sub == nil {
				return fmt.Errorf("gmcp: cannot open a subscription stream")
			}
			continue
		}
		if err := out.writeMessage(s.handle(ctx, request)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, request Request) Response {
	if request.Method == "initialize" {
		return s.handleLegacy(ctx, request)
	}
	if meta := requestMeta(request.Params); meta != nil {
		return s.handleModern(ctx, request, meta)
	}
	return s.handleLegacy(ctx, request)
}

func (s *Server) handleNotification(request Request) {
	if request.Method != NotifyCancelled {
		return
	}
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return
	}
	s.cancelSubscription(params.RequestID)
}

func (s *Server) handleModern(ctx context.Context, request Request, meta *RequestMeta) Response {
	response := Response{JSONRPC: JSONRPCVersion, ID: request.ID}
	if !s.supportsVersion(meta.ProtocolVersion) {
		response.Error = UnsupportedProtocolVersionError(meta.ProtocolVersion, s.versions)
		return response
	}

	switch request.Method {
	case "ping":
		response.Result = s.decorate(map[string]any{})
	case "server/discover":
		response.Result = s.decorate(DiscoverResult{
			CacheScope:        s.cacheScope,
			Capabilities:      s.capabilities(),
			Instructions:      s.instructions,
			SupportedVersions: append([]string(nil), s.versions...),
			TTLMs:             s.ttlMs,
		})
	case "tools/list":
		response.Result = s.decorate(ListToolsResult{Tools: s.listTools()})
	case "tools/call":
		params := CallToolParams{}
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				response.Error = NewError(CodeInvalidParams, "invalid tools/call params")
				return response
			}
		}
		response.Result = s.decorate(s.callTool(ctx, params))
	default:
		response.Error = NewError(CodeMethodNotFound, "method not found: "+request.Method)
	}
	return response
}

func (s *Server) handleLegacy(ctx context.Context, request Request) Response {
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
			ProtocolVersion: s.legacyVersion(params.ProtocolVersion),
			Capabilities:    s.capabilities(),
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
		response.Result = mustJSON(s.callTool(ctx, params))

	default:
		response.Error = NewError(CodeMethodNotFound, "method not found: "+request.Method)
	}
	return response
}

// decorate adds the fields every modern result carries: resultType and the
// server identity in _meta.
func (s *Server) decorate(result any) json.RawMessage {
	raw := mustJSON(result)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return raw
	}
	object["resultType"] = ResultTypeComplete
	meta, _ := object["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, ok := meta[MetaServerInfo]; !ok {
		meta[MetaServerInfo] = s.info
	}
	object["_meta"] = meta
	return mustJSON(object)
}

func (s *Server) capabilities() ServerCapabilities {
	return ServerCapabilities{Tools: &ToolsCapability{}}
}

func (s *Server) supportsVersion(version string) bool {
	for _, supported := range s.versions {
		if supported == version {
			return true
		}
	}
	return false
}

// legacyVersion picks the revision answered by initialize: the newest legacy
// revision the server serves, preferring the requested one. Modern revisions
// are never negotiated through the handshake.
func (s *Server) legacyVersion(requested string) string {
	best := ""
	for _, version := range s.versions {
		if version > ProtocolLegacy {
			continue
		}
		if best == "" || version > best {
			best = version
		}
		if version == requested {
			return version
		}
	}
	if best != "" {
		return best
	}
	return ProtocolLegacy
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

func (s *Server) callTool(ctx context.Context, params CallToolParams) CallToolResult {
	s.mu.RLock()
	entry, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return toolError("unknown tool: " + params.Name)
	}

	arguments := params.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return toolError("arguments are not valid JSON")
	}

	result, err := entry.handler(ctx, arguments)
	if err != nil {
		return toolError(err.Error())
	}

	text, err := resultText(result)
	if err != nil {
		return toolError(err.Error())
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: text}}}
}

func toolError(message string) CallToolResult {
	return CallToolResult{IsError: true, Content: []Content{{Type: "text", Text: message}}}
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

// requestMeta returns the modern _meta of a request, or nil when the request
// carries none.
func requestMeta(params json.RawMessage) *RequestMeta {
	if len(params) == 0 {
		return nil
	}
	var envelope struct {
		Meta *RequestMeta `json:"_meta"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return nil
	}
	if envelope.Meta == nil || envelope.Meta.ProtocolVersion == "" {
		return nil
	}
	return envelope.Meta
}

// lockedWriter serializes writes from the request loop and from subscription
// goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (l *lockedWriter) writeMessage(message any) error {
	return l.writeRaw(mustJSON(message))
}

func (l *lockedWriter) writeRaw(message json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(message); err != nil {
		return err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return err
	}
	return l.w.Flush()
}
