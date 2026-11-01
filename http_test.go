package gmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maxid/gmcp"
)

func newServer(t *testing.T) *gmcp.Server {
	t.Helper()
	server := gmcp.NewServer("gable-test")
	if err := server.AddTool(
		gmcp.Tool{Name: "echo", Description: "echo the argument"},
		func(_ context.Context, args json.RawMessage) (any, error) {
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(args, &payload)
			return map[string]any{"text": payload.Text}, nil
		},
	); err != nil {
		t.Fatalf("add tool: %v", err)
	}
	return server
}

func TestHTTPServesInitializeAndTools(t *testing.T) {
	handler := newServer(t).HTTPHandler()

	t.Run("initialize", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test","version":"1"}}}`
		response := post(t, handler, body)

		if response.Error != nil {
			t.Fatalf("error = %+v", response.Error)
		}
		var result gmcp.InitializeResult
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.ServerInfo.Name != "gable-test" {
			t.Fatalf("server info = %+v", result.ServerInfo)
		}
	})

	t.Run("tools/list", func(t *testing.T) {
		response := post(t, handler, `{"jsonrpc":"2.0","id":"2","method":"tools/list"}`)
		var result gmcp.ListToolsResult
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(result.Tools) != 1 || result.Tools[0].Name != "echo" {
			t.Fatalf("tools = %+v", result.Tools)
		}
	})

	t.Run("tools/call", func(t *testing.T) {
		response := post(t, handler, `{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)
		var result gmcp.CallToolResult
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "hi") {
			t.Fatalf("content = %+v", result.Content)
		}
	})
}

// A notification gets 202 and no body, which is what the transport specifies.
func TestHTTPAcceptsNotifications(t *testing.T) {
	handler := newServer(t).HTTPHandler()

	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", recorder.Body.String())
	}
}

func TestHTTPBatch(t *testing.T) {
	handler := newServer(t).HTTPHandler()
	body := `[
		{"jsonrpc":"2.0","id":"1","method":"tools/list"},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":"2","method":"ping"}
	]`

	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var responses []gmcp.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &responses); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Two requests carried ids; the notification is not answered.
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(responses))
	}
}

func TestHTTPRejectsBadInput(t *testing.T) {
	handler := newServer(t).HTTPHandler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString("not json")))
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", recorder.Code)
	}
}

// The HTTP client speaks to the HTTP handler: the two halves agree on the envelope.
func TestHTTPClientRoundTrip(t *testing.T) {
	server := httptest.NewServer(newServer(t).HTTPHandler())
	defer server.Close()

	client := gmcp.NewHTTPClient(server.URL, nil)
	response, err := client.Call(context.Background(), gmcp.Request{
		JSONRPC: gmcp.JSONRPCVersion,
		ID:      json.RawMessage(`"1"`),
		Method:  "tools/list",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var result gmcp.ListToolsResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("tools = %+v", result.Tools)
	}
}

func post(t *testing.T, handler http.Handler, body string) gmcp.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response gmcp.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}
