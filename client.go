package gmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Client speaks MCP over a newline-delimited JSON-RPC stream.
//
// Calls are serialized: one request is in flight at a time. That is enough for
// the stdio transport and keeps response correlation simple.
type Client struct {
	reader *bufio.Reader
	writer io.Writer

	mu     sync.Mutex
	nextID int64
}

// NewClient returns a client reading from r and writing to w.
func NewClient(r io.Reader, w io.Writer) *Client {
	return &Client{reader: bufio.NewReader(r), writer: w}
}

// Initialize performs the MCP initialize handshake.
func (c *Client) Initialize(ctx context.Context, info ClientInfo) (InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: DefaultProtocolVersion,
		ClientInfo:      info,
	}
	var result InitializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if err := c.Notify(ctx, "notifications/initialized", nil); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

// ListTools returns the server's tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result ListToolsResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// CallTool invokes a tool.
func (c *Client) CallTool(ctx context.Context, name string, arguments any) (CallToolResult, error) {
	raw, err := json.Marshal(arguments)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("gmcp: marshal tool arguments: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	params := CallToolParams{Name: name, Arguments: raw}
	var result CallToolResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return CallToolResult{}, err
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	request := Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(id),
		Method:  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("gmcp: marshal %s params: %w", method, err)
		}
		request.Params = raw
	}

	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("gmcp: marshal request: %w", err)
	}
	if _, err := fmt.Fprintf(c.writer, "%s\n", raw); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return io.EOF
			}
			if err != io.EOF {
				return err
			}
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}

		var response Response
		if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
			return fmt.Errorf("gmcp: decode response: %w", err)
		}
		if len(response.ID) == 0 {
			// Notification from the server; keep reading.
			continue
		}
		if string(response.ID) != id {
			continue
		}
		if response.Error != nil {
			return response.Error
		}
		if out != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return fmt.Errorf("gmcp: decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Notify sends a JSON-RPC notification. No response is expected.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notification := Notification{JSONRPC: JSONRPCVersion, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("gmcp: marshal %s params: %w", method, err)
		}
		notification.Params = raw
	}
	raw, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("gmcp: marshal notification: %w", err)
	}
	_, err = fmt.Fprintf(c.writer, "%s\n", raw)
	return err
}
