package gmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HeaderProtocolVersion carries the revision of a modern request; it must match
// the request's _meta.
const HeaderProtocolVersion = "MCP-Protocol-Version"

// HTTPTransport carries MCP over the request/response form of Streamable HTTP:
// one POST per JSON-RPC request (or batch), one JSON-RPC response back.
//
// A subscriptions/listen request answers with an SSE stream instead, which is
// the only streaming path this package implements.
const (
	// HTTPMaxBody bounds one request body.
	HTTPMaxBody = 4 << 20
	// HTTPContentType is the content type of both directions.
	HTTPContentType = "application/json"
)

// HTTPHandler exposes the server over HTTP.
//
// A notification (a request without an id) is answered with 202 and no body,
// which is what the MCP Streamable HTTP transport expects.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRPCError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "method not allowed")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, HTTPMaxBody))
		if err != nil {
			writeRPCError(w, http.StatusBadRequest, CodeInvalidRequest, "read body: "+err.Error())
			return
		}
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			writeRPCError(w, http.StatusBadRequest, CodeInvalidRequest, "empty body")
			return
		}

		if body[0] == '[' {
			s.serveBatch(w, r, body)
			return
		}

		var request Request
		if err := json.Unmarshal(body, &request); err != nil {
			writeRPCError(w, http.StatusBadRequest, CodeParseError, "parse error")
			return
		}

		if meta := requestMeta(request.Params); meta != nil {
			if response, status := s.checkModernRequest(r, meta); response != nil {
				writeJSON(w, status, response)
				return
			}
			if request.Method == "subscriptions/listen" {
				s.serveListen(w, r, request)
				return
			}
		}

		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		response := s.handle(r.Context(), request)
		writeJSON(w, statusFor(response), response)
	})
}

// serveBatch keeps the legacy batch behaviour; a modern revision has no
// JSON-RPC batching, so a batch carrying modern _meta is rejected.
func (s *Server) serveBatch(w http.ResponseWriter, r *http.Request, body []byte) {
	var requests []Request
	if err := json.Unmarshal(body, &requests); err != nil {
		writeRPCError(w, http.StatusBadRequest, CodeParseError, "parse error")
		return
	}
	for _, request := range requests {
		if requestMeta(request.Params) != nil {
			writeRPCError(w, http.StatusBadRequest, CodeInvalidRequest,
				"JSON-RPC batches are not supported in "+ProtocolModern)
			return
		}
	}

	responses := make([]Response, 0, len(requests))
	for _, request := range requests {
		if len(request.ID) == 0 {
			continue // notifications need no answer
		}
		responses = append(responses, s.handle(r.Context(), request))
	}
	if len(responses) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, responses)
}

// checkModernRequest validates the HTTP-side requirement that the protocol
// header and the request body agree. It returns the error response to send, or
// nil when the request may proceed.
func (s *Server) checkModernRequest(r *http.Request, meta *RequestMeta) (*Response, int) {
	header := strings.TrimSpace(r.Header.Get(HeaderProtocolVersion))
	if header == "" {
		return errorResponse(NewError(CodeHeaderMismatch, "missing "+HeaderProtocolVersion+" header")), http.StatusBadRequest
	}
	if header != meta.ProtocolVersion {
		return errorResponse(NewError(CodeHeaderMismatch,
			"header and _meta disagree on the protocol version")), http.StatusBadRequest
	}
	if !s.supportsVersion(meta.ProtocolVersion) {
		return errorResponse(UnsupportedProtocolVersionError(meta.ProtocolVersion, s.versions)), http.StatusBadRequest
	}
	return nil, 0
}

func (s *Server) serveListen(w http.ResponseWriter, r *http.Request, request Request) {
	stream, err := newSSEWriter(w)
	if err != nil {
		writeRPCError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	defer stream.close()

	sub := s.openSubscription(request, stream.write)
	if sub == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			s.cancelSubscription(request.ID)
			return
		case <-sub.Done():
			return
		case <-ticker.C:
			stream.comment("keep-alive")
		}
	}
}

func statusFor(response Response) int {
	if response.Error == nil {
		return http.StatusOK
	}
	switch response.Error.Code {
	case CodeHeaderMismatch, CodeUnsupportedProtocolVersion:
		return http.StatusBadRequest
	default:
		return http.StatusOK
	}
}

func errorResponse(err *Error) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: json.RawMessage("null"), Error: err}
}

// HTTPClient is an MCP client over Streamable HTTP.
type HTTPClient struct {
	endpoint string
	client   *http.Client
	meta     RequestMeta

	mu     sync.Mutex
	nextID int64
}

// NewHTTPClient creates a client for an MCP endpoint URL.
func NewHTTPClient(endpoint string, client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &HTTPClient{endpoint: endpoint, client: client}
}

// SetRequestMeta makes every subsequent request modern: the metadata is merged
// into each request's params and the protocol version into the HTTP header.
func (c *HTTPClient) SetRequestMeta(meta RequestMeta) { c.meta = meta }

// Call performs one JSON-RPC round trip.
func (c *HTTPClient) Call(ctx context.Context, request Request) (Response, error) {
	httpRequest, err := c.newRequest(ctx, request)
	if err != nil {
		return Response{}, err
	}

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("gmcp: request: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, HTTPMaxBody))
	if err != nil {
		return Response{}, fmt.Errorf("gmcp: read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Response{}, fmt.Errorf("gmcp: %d %s: %s", response.StatusCode, response.Status,
			strings.TrimSpace(string(body)))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Response{}, nil
	}

	var decoded Response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Response{}, fmt.Errorf("gmcp: decode response: %w", err)
	}
	return decoded, nil
}

// Discover asks the server for its capabilities and supported revisions.
func (c *HTTPClient) Discover(ctx context.Context) (DiscoverResult, error) {
	response, err := c.Call(ctx, Request{
		JSONRPC: JSONRPCVersion,
		ID:      c.newID(),
		Method:  "server/discover",
	})
	if err != nil {
		return DiscoverResult{}, err
	}
	if response.Error != nil {
		return DiscoverResult{}, response.Error
	}
	var result DiscoverResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return DiscoverResult{}, fmt.Errorf("gmcp: decode discover: %w", err)
	}
	return result, nil
}

// Listen opens a subscriptions/listen stream and returns once the server
// acknowledged it.
func (c *HTTPClient) Listen(ctx context.Context, filter SubscriptionFilter) (*HTTPSubscription, error) {
	id := c.newID()
	params := mustJSON(SubscriptionsListenParams{Notifications: filter})
	request, err := c.newRequest(ctx, Request{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "subscriptions/listen",
		Params:  params,
	})
	if err != nil {
		return nil, err
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("gmcp: listen: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, HTTPMaxBody))
		_ = response.Body.Close()
		return nil, fmt.Errorf("gmcp: listen: %d %s: %s", response.StatusCode, response.Status,
			strings.TrimSpace(string(body)))
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), sseContentType) {
		_ = response.Body.Close()
		return nil, fmt.Errorf("gmcp: listen: expected %s, got %q", sseContentType,
			response.Header.Get("Content-Type"))
	}

	sub := &HTTPSubscription{
		id:     id,
		events: make(chan Notification, 16),
		done:   make(chan struct{}),
		body:   response.Body,
	}
	if err := sub.read(ctx, filter); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	return sub, nil
}

// HTTPSubscription is one open subscription stream.
type HTTPSubscription struct {
	id     json.RawMessage
	filter SubscriptionFilter
	events chan Notification
	done   chan struct{}
	body   io.ReadCloser
	once   sync.Once
}

// ID is the JSON-RPC id that opened the stream.
func (sub *HTTPSubscription) ID() json.RawMessage { return sub.id }

// Filter is the notification set the server agreed to honor.
func (sub *HTTPSubscription) Filter() SubscriptionFilter { return sub.filter }

// Notifications yields the stream's notifications; it is closed when the
// subscription ends.
func (sub *HTTPSubscription) Notifications() <-chan Notification { return sub.events }

// Done is closed when the subscription ends.
func (sub *HTTPSubscription) Done() <-chan struct{} { return sub.done }

// Close ends the subscription. On this transport, closing the stream is itself
// the cancellation signal.
func (sub *HTTPSubscription) Close() error {
	sub.finish()
	return sub.body.Close()
}

// read consumes the acknowledgement, then keeps the stream flowing in a goroutine.
func (sub *HTTPSubscription) read(ctx context.Context, requested SubscriptionFilter) error {
	reader := newSSEReader(sub.body)

	payload, err := reader.next()
	if err != nil {
		return fmt.Errorf("gmcp: listen: %w", err)
	}
	var ack Notification
	if err := json.Unmarshal(payload, &ack); err != nil {
		return fmt.Errorf("gmcp: listen acknowledgement: %w", err)
	}
	if ack.Method != NotifySubscriptionsAck {
		return fmt.Errorf("gmcp: listen: expected %q, got %q", NotifySubscriptionsAck, ack.Method)
	}
	var params SubscriptionsAcknowledgedParams
	if len(ack.Params) > 0 {
		if err := json.Unmarshal(ack.Params, &params); err != nil {
			return fmt.Errorf("gmcp: listen acknowledgement params: %w", err)
		}
	}
	sub.filter = params.Notifications

	go func() {
		defer sub.finish()
		for {
			payload, err := reader.next()
			if err != nil {
				return
			}
			var notification Notification
			if err := json.Unmarshal(payload, &notification); err != nil {
				continue
			}
			if notification.Method == "" {
				return // the completion result ends the stream
			}
			select {
			case sub.events <- notification:
			case <-sub.done:
				return
			case <-ctx.Done():
				_ = sub.body.Close()
				return
			}
		}
	}()
	return nil
}

func (sub *HTTPSubscription) finish() {
	sub.once.Do(func() {
		close(sub.done)
		close(sub.events)
	})
}

func (c *HTTPClient) newID() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return json.RawMessage(strconv.FormatInt(c.nextID, 10))
}

func (c *HTTPClient) newRequest(ctx context.Context, request Request) (*http.Request, error) {
	if meta := c.meta; meta.ProtocolVersion != "" {
		request.Params = withRequestMeta(request.Params, meta)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("gmcp: marshal request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", HTTPContentType)
	httpRequest.Header.Set("Accept", HTTPContentType+", "+sseContentType)
	if c.meta.ProtocolVersion != "" {
		httpRequest.Header.Set(HeaderProtocolVersion, c.meta.ProtocolVersion)
	}
	return httpRequest, nil
}

// withRequestMeta merges a request's params with the modern metadata every
// modern request must carry.
func withRequestMeta(params json.RawMessage, meta RequestMeta) json.RawMessage {
	object := map[string]json.RawMessage{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &object); err != nil {
			object = map[string]json.RawMessage{}
		}
	}
	delete(object, "_meta")
	object["_meta"] = mustJSON(meta)
	return mustJSON(object)
}

// writeJSON writes a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", HTTPContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeRPCError writes a JSON-RPC error envelope.
func writeRPCError(w http.ResponseWriter, status int, code int, message string) {
	writeJSON(w, status, Response{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("null"),
		Error:   NewError(code, message),
	})
}
