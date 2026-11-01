package gmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// modernParams builds a params object carrying modern _meta.
func modernParams(version string, extra map[string]any) json.RawMessage {
	meta := map[string]any{
		MetaProtocolVersion: version,
		MetaClientInfo:      map[string]any{"name": "test-client", "version": "1.0.0"},
		MetaClientCaps:      map[string]any{},
	}
	params := map[string]any{"_meta": meta}
	for key, value := range extra {
		params[key] = value
	}
	raw, _ := json.Marshal(params)
	return raw
}

func rawServer(t *testing.T, server *Server) (net.Conn, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, serverConn, serverConn)
		close(done)
	}()
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
	return clientConn, cleanup
}

func roundTripRaw(t *testing.T, conn net.Conn, request Request) Response {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return response
}

func TestModernRequestWithoutHandshake(t *testing.T) {
	conn, cleanup := rawServer(t, newTestServer(t))
	defer cleanup()

	response := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  "tools/list",
		Params:  modernParams(ProtocolModern, nil),
	})
	if response.Error != nil {
		t.Fatalf("error = %+v", response.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["resultType"] != ResultTypeComplete {
		t.Fatalf("resultType = %v", result["resultType"])
	}
	meta, _ := result["_meta"].(map[string]any)
	info, _ := meta[MetaServerInfo].(map[string]any)
	if info["name"] != "test-server" || info["version"] == "" {
		t.Fatalf("_meta.serverInfo = %v", meta)
	}
}

func TestUnsupportedVersionIsReported(t *testing.T) {
	conn, cleanup := rawServer(t, newTestServer(t))
	defer cleanup()

	response := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  "tools/list",
		Params:  modernParams("1900-01-01", nil),
	})
	if response.Error == nil || response.Error.Code != CodeUnsupportedProtocolVersion {
		t.Fatalf("error = %+v", response.Error)
	}
	data, _ := response.Error.Data.(map[string]any)
	if data["requested"] != "1900-01-01" {
		t.Fatalf("data = %v", data)
	}
	supported, _ := data["supported"].([]any)
	if len(supported) == 0 {
		t.Fatalf("supported = %v", data["supported"])
	}
}

func TestDiscoverDescribesTheServer(t *testing.T) {
	server := newTestServer(t)
	server.SetInstructions("echo only")
	conn, cleanup := rawServer(t, server)
	defer cleanup()

	response := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  "server/discover",
		Params:  modernParams(ProtocolModern, nil),
	})
	if response.Error != nil {
		t.Fatalf("error = %+v", response.Error)
	}

	var discover DiscoverResult
	if err := json.Unmarshal(response.Result, &discover); err != nil {
		t.Fatal(err)
	}
	if discover.ResultType != ResultTypeComplete {
		t.Fatalf("resultType = %q", discover.ResultType)
	}
	if discover.CacheScope != "public" || discover.TTLMs == 0 {
		t.Fatalf("cache = %q %d", discover.CacheScope, discover.TTLMs)
	}
	if len(discover.SupportedVersions) != 2 || discover.SupportedVersions[0] != ProtocolModern {
		t.Fatalf("supportedVersions = %v", discover.SupportedVersions)
	}
	if discover.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}
	if discover.Instructions != "echo only" {
		t.Fatalf("instructions = %q", discover.Instructions)
	}
}

func TestLegacyInitializeNeverNegotiatesModern(t *testing.T) {
	conn, cleanup := rawServer(t, newTestServer(t))
	defer cleanup()

	params, _ := json.Marshal(InitializeParams{
		ProtocolVersion: ProtocolModern,
		ClientInfo:      ClientInfo{Name: "legacy", Version: "1"},
	})
	response := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  params,
	})
	if response.Error != nil {
		t.Fatalf("error = %+v", response.Error)
	}

	var result InitializeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != ProtocolLegacy {
		t.Fatalf("protocolVersion = %q, want %q", result.ProtocolVersion, ProtocolLegacy)
	}
}

func TestBothErasOnOneConnection(t *testing.T) {
	conn, cleanup := rawServer(t, newTestServer(t))
	defer cleanup()

	params, _ := json.Marshal(InitializeParams{
		ProtocolVersion: ProtocolLegacy,
		ClientInfo:      ClientInfo{Name: "legacy", Version: "1"},
	})
	legacy := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  params,
	})
	if legacy.Error != nil {
		t.Fatalf("initialize error = %+v", legacy.Error)
	}

	modern := roundTripRaw(t, conn, Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("2"),
		Method:  "tools/call",
		Params:  modernParams(ProtocolModern, map[string]any{"name": "echo", "arguments": map[string]any{"x": 1}}),
	})
	if modern.Error != nil {
		t.Fatalf("modern error = %+v", modern.Error)
	}

	var result CallToolResult
	if err := json.Unmarshal(modern.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != `{"x":1}` {
		t.Fatalf("result = %+v", result)
	}
}

func TestConnectProbesAndSettlesModern(t *testing.T) {
	client, _, cleanup := startPair(t, newTestServer(t))
	defer cleanup()

	discover, err := client.Connect(context.Background(), Options{Info: ClientInfo{Name: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.Era() != EraModern {
		t.Fatalf("era = %q", client.Era())
	}
	if discover.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestConnectFallsBackToLegacy(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var request Request
			if err := json.Unmarshal([]byte(line), &request); err != nil {
				continue
			}
			if len(request.ID) == 0 {
				continue
			}
			response := Response{JSONRPC: JSONRPCVersion, ID: request.ID}
			if request.Method == "initialize" {
				response.Result = mustJSON(InitializeResult{
					ProtocolVersion: ProtocolLegacy,
					Capabilities:    ServerCapabilities{Tools: &ToolsCapability{}},
					ServerInfo:      ServerInfo{Name: "legacy-only", Version: "1"},
				})
			} else {
				response.Error = NewError(CodeMethodNotFound, "method not found: "+request.Method)
			}
			raw, _ := json.Marshal(response)
			if _, err := serverConn.Write(append(raw, '\n')); err != nil {
				return
			}
		}
	}()

	client := NewClient(clientConn, clientConn)
	discover, err := client.Connect(context.Background(), Options{Info: ClientInfo{Name: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	if client.Era() != EraLegacy {
		t.Fatalf("era = %q", client.Era())
	}
	if len(discover.SupportedVersions) != 0 {
		t.Fatalf("discover = %+v", discover)
	}

	_ = clientConn.Close()
	_ = serverConn.Close()
	<-done
}
