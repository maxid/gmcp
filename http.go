package gmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPTransport carries MCP over the request/response form of Streamable HTTP: one
// POST per JSON-RPC request (or batch), one JSON-RPC response back.
//
// The streaming (SSE) form is not implemented: it exists for servers that push
// notifications mid-call, and this library's tools are request/response. A client that
// needs SSE keeps using the stdio transport or its own transport.
const (
	// HTTPMaxBody bounds one request body.
	HTTPMaxBody = 4 << 20
	// HTTPContentType is the content type of both directions.
	HTTPContentType = "application/json"
)

// HTTPHandler exposes the server over HTTP.
//
// A notification (a request without an id) is answered with 202 and no body, which is
// what the MCP Streamable HTTP transport expects.
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

		// A batch is a JSON array; a single call is an object.
		if body[0] == '[' {
			var requests []Request
			if err := json.Unmarshal(body, &requests); err != nil {
				writeRPCError(w, http.StatusBadRequest, CodeParseError, "parse error")
				return
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
			return
		}

		var request Request
		if err := json.Unmarshal(body, &request); err != nil {
			writeRPCError(w, http.StatusBadRequest, CodeParseError, "parse error")
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, s.handle(r.Context(), request))
	})
}

// HTTPClient is an MCP client over Streamable HTTP.
type HTTPClient struct {
	endpoint string
	client   *http.Client
}

// NewHTTPClient creates a client for an MCP endpoint URL.
func NewHTTPClient(endpoint string, client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 60 * 1e9} // 60s
	}
	return &HTTPClient{endpoint: endpoint, client: client}
}

// Call performs one JSON-RPC round trip.
func (c *HTTPClient) Call(ctx context.Context, request Request) (Response, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("gmcp: marshal request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpRequest.Header.Set("Content-Type", HTTPContentType)
	httpRequest.Header.Set("Accept", HTTPContentType)

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
