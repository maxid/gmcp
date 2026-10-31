package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"

	"github.com/maxid/gmcp"
)

func main() {
	server := gmcp.NewServer("gmcp-echo")
	server.SetVersion(gmcp.Version)
	err := server.AddTool(gmcp.Tool{
		Name:        "echo",
		Description: "returns the arguments it received",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, args json.RawMessage) (any, error) {
		return string(args), nil
	})
	if err != nil {
		log.Fatal(err)
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx, serverConn, serverConn) }()

	client := gmcp.NewClient(clientConn, clientConn)
	info, err := client.Initialize(ctx, gmcp.ClientInfo{Name: "example", Version: "0.0.0-dev"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("server:", info.ServerInfo.Name, "protocol:", info.ProtocolVersion)

	tools, err := client.ListTools(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, tool := range tools {
		fmt.Println("tool:", tool.Name, "-", tool.Description)
	}

	result, err := client.CallTool(ctx, "echo", map[string]any{"hello": "world"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("result:", result.Content[0].Text)
}
