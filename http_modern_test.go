package gmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maxid/gmcp"
)

func modernBody(version, method string) string {
	meta := map[string]any{
		gmcp.MetaProtocolVersion: version,
		gmcp.MetaClientInfo:      map[string]any{"name": "test", "version": "1"},
		gmcp.MetaClientCaps:      map[string]any{},
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  map[string]any{"_meta": meta},
	})
	return string(body)
}

func postWithHeader(t *testing.T, handler http.Handler, body, version string) (*httptest.ResponseRecorder, gmcp.Response) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	if version != "" {
		request.Header.Set(gmcp.HeaderProtocolVersion, version)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	var response gmcp.Response
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder, response
}

func TestHTTPRequiresMatchingProtocolHeader(t *testing.T) {
	handler := newServer(t).HTTPHandler()
	body := modernBody(gmcp.ProtocolModern, "tools/list")

	t.Run("missing header", func(t *testing.T) {
		recorder, response := postWithHeader(t, handler, body, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		if response.Error == nil || response.Error.Code != gmcp.CodeHeaderMismatch {
			t.Fatalf("error = %+v", response.Error)
		}
	})

	t.Run("mismatched header", func(t *testing.T) {
		recorder, response := postWithHeader(t, handler, body, gmcp.ProtocolLegacy)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		if response.Error == nil || response.Error.Code != gmcp.CodeHeaderMismatch {
			t.Fatalf("error = %+v", response.Error)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		body := modernBody("1900-01-01", "tools/list")
		recorder, response := postWithHeader(t, handler, body, "1900-01-01")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		if response.Error == nil || response.Error.Code != gmcp.CodeUnsupportedProtocolVersion {
			t.Fatalf("error = %+v", response.Error)
		}
	})
}

func TestHTTPServesModernRequest(t *testing.T) {
	handler := newServer(t).HTTPHandler()

	recorder, response := postWithHeader(t, handler, modernBody(gmcp.ProtocolModern, "tools/list"), gmcp.ProtocolModern)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if response.Error != nil {
		t.Fatalf("error = %+v", response.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["resultType"] != gmcp.ResultTypeComplete {
		t.Fatalf("resultType = %v", result["resultType"])
	}
	if _, ok := result["_meta"]; !ok {
		t.Fatal("modern result is missing _meta")
	}
}

func TestHTTPRejectsModernBatch(t *testing.T) {
	handler := newServer(t).HTTPHandler()

	single := modernBody(gmcp.ProtocolModern, "tools/list")
	body := "[" + single + "]"
	recorder, response := postWithHeader(t, handler, body, gmcp.ProtocolModern)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if response.Error == nil || response.Error.Code != gmcp.CodeInvalidRequest {
		t.Fatalf("error = %+v", response.Error)
	}
}

func TestHTTPSubscriptionStreams(t *testing.T) {
	server := newServer(t)
	server.SetSubscriptionHandler(func(sub *gmcp.Subscription) { <-sub.Done() })

	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	client := gmcp.NewHTTPClient(httpServer.URL, nil)
	client.SetRequestMeta(gmcp.RequestMeta{
		ProtocolVersion:    gmcp.ProtocolModern,
		ClientInfo:         &gmcp.Implementation{Name: "test", Version: "1"},
		ClientCapabilities: json.RawMessage(`{}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, err := client.Listen(ctx, gmcp.SubscriptionFilter{ToolsListChanged: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()

	if !sub.Filter().ToolsListChanged {
		t.Fatalf("granted filter = %+v", sub.Filter())
	}

	server.ToolsChanged()
	select {
	case notification := <-sub.Notifications():
		if notification.Method != gmcp.NotifyToolsListChanged {
			t.Fatalf("method = %q", notification.Method)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification arrived")
	}
}
