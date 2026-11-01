package gmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server := NewServer("test-server")
	server.SetVersion("0.0.0-dev")
	if err := server.AddTool(Tool{
		Name:        "echo",
		Description: "echo the arguments",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, args json.RawMessage) (any, error) {
		return string(args), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.AddTool(Tool{Name: "fail", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("tool exploded")
		}); err != nil {
		t.Fatal(err)
	}
	return server
}

func startPair(t *testing.T, server *Server) (*Client, context.CancelFunc, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, serverConn, serverConn)
		close(done)
	}()
	client := NewClient(clientConn, clientConn)
	cleanup := func() {
		cancel()
		_ = clientConn.Close()
		_ = serverConn.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	}
	return client, cancel, cleanup
}

func TestInitializeListAndCall(t *testing.T) {
	client, _, cleanup := startPair(t, newTestServer(t))
	defer cleanup()

	init, err := client.Initialize(context.Background(), ClientInfo{Name: "test-client"})
	if err != nil {
		t.Fatal(err)
	}
	if init.ServerInfo.Name != "test-server" || init.ProtocolVersion == "" {
		t.Fatalf("initialize = %+v", init)
	}
	if init.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}

	result, err := client.CallTool(context.Background(), "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, `"x":1`) {
		t.Fatalf("result = %+v", result)
	}
}

func TestToolErrorIsAResult(t *testing.T) {
	client, _, cleanup := startPair(t, newTestServer(t))
	defer cleanup()

	result, err := client.CallTool(context.Background(), "fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "tool exploded" {
		t.Fatalf("result = %+v", result)
	}
}

func TestUnknownMethodReturnsJSONRPCError(t *testing.T) {
	client, _, cleanup := startPair(t, newTestServer(t))
	defer cleanup()

	err := client.call(context.Background(), EraLegacy, "does/not/exist", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestParseErrorResponse(t *testing.T) {
	server := newTestServer(t)
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		_ = server.Serve(context.Background(), serverConn, serverConn)
		close(done)
	}()

	if _, err := clientConn.Write([]byte("not-json\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(clientConn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != CodeParseError {
		t.Fatalf("response = %+v", response)
	}
	_ = serverConn.Close()
	_ = clientConn.Close()
	<-done
}

func TestAddToolValidation(t *testing.T) {
	server := NewServer("test")
	if err := server.AddTool(Tool{}, func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Fatal("expected empty name error")
	}
	if err := server.AddTool(Tool{Name: "x", InputSchema: json.RawMessage(`{`)}, func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Fatal("expected invalid schema error")
	}
	if err := server.AddTool(Tool{Name: "x"}, nil); err == nil {
		t.Fatal("expected nil handler error")
	}
	if err := server.AddTool(Tool{Name: "x"}, func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := server.AddTool(Tool{Name: "x"}, func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Fatal("expected duplicate error")
	}
}
