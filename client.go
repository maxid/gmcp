package gmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Era names the protocol generation a client settled on.
type Era string

const (
	// EraUnknown means no era has been settled yet.
	EraUnknown Era = ""
	// EraModern means version, identity and capabilities travel per request.
	EraModern Era = "modern"
	// EraLegacy means the connection opened with an initialize handshake.
	EraLegacy Era = "legacy"
)

// Options configures Connect.
type Options struct {
	// Info identifies this client. Name and Version default to the library's.
	Info ClientInfo
	// Capabilities is the client capabilities object sent with every modern
	// request. An empty value means no optional capabilities.
	Capabilities json.RawMessage
	// Version is the preferred modern revision; it defaults to ProtocolModern.
	Version string
	// Era forces a generation instead of probing the server.
	Era Era
}

// Client speaks MCP over a newline-delimited JSON-RPC stream.
type Client struct {
	reader *bufio.Reader
	writer io.Writer

	writeMu sync.Mutex
	nextID  int64

	mu           sync.Mutex
	era          Era
	version      string
	info         Implementation
	capabilities json.RawMessage
	pending      map[string]chan Response
	subs         map[string]*ClientSubscription
	readErr      error
	done         chan struct{}
	stopOnce     sync.Once
}

// NewClient returns a client reading from r and writing to w. Reading runs in
// the background, so calls may be concurrent and subscriptions may arrive while
// another call is in flight.
func NewClient(r io.Reader, w io.Writer) *Client {
	client := &Client{
		reader:       bufio.NewReader(r),
		writer:       w,
		version:      ProtocolModern,
		info:         Implementation{Name: "gmcp-client", Version: Version},
		capabilities: json.RawMessage(`{}`),
		pending:      make(map[string]chan Response),
		subs:         make(map[string]*ClientSubscription),
		done:         make(chan struct{}),
	}
	go client.readLoop()
	return client
}

// Era reports the protocol generation this client settled on.
func (c *Client) Era() Era {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.era
}

// Connect settles the protocol generation. With Options.Era unset it probes for
// a modern server and falls back to the legacy handshake.
func (c *Client) Connect(ctx context.Context, options Options) (DiscoverResult, error) {
	c.mu.Lock()
	if options.Info.Name != "" {
		c.info = options.Info
	}
	if c.info.Version == "" {
		c.info.Version = Version
	}
	if len(options.Capabilities) > 0 {
		c.capabilities = options.Capabilities
	}
	if options.Version != "" {
		c.version = options.Version
	}
	c.mu.Unlock()

	switch options.Era {
	case EraLegacy:
		return DiscoverResult{}, c.connectLegacy(ctx)
	case EraModern:
		discover, err := c.Discover(ctx)
		if err != nil {
			return DiscoverResult{}, err
		}
		c.setEra(EraModern)
		return discover, nil
	default:
		return c.connectAuto(ctx)
	}
}

func (c *Client) connectAuto(ctx context.Context) (DiscoverResult, error) {
	probeCtx, cancel := probeContext(ctx)
	discover, err := c.Discover(probeCtx)
	cancel()
	if err == nil {
		c.setEra(EraModern)
		return discover, nil
	}

	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case CodeUnsupportedProtocolVersion:
			if version := supportedModernVersion(rpcErr); version != "" {
				c.mu.Lock()
				c.version = version
				c.mu.Unlock()
				if retried, retryErr := c.Discover(ctx); retryErr == nil {
					c.setEra(EraModern)
					return retried, nil
				}
			}
			return DiscoverResult{}, err
		case CodeHeaderMismatch:
			return DiscoverResult{}, err
		}
	}

	if legacyErr := c.connectLegacy(ctx); legacyErr != nil {
		return DiscoverResult{}, legacyErr
	}
	return DiscoverResult{}, nil
}

func (c *Client) connectLegacy(ctx context.Context) error {
	if _, err := c.Initialize(ctx, c.clientInfo()); err != nil {
		return err
	}
	c.setEra(EraLegacy)
	return nil
}

// Discover asks the server for its capabilities and supported revisions.
func (c *Client) Discover(ctx context.Context) (DiscoverResult, error) {
	var result DiscoverResult
	if err := c.call(ctx, EraModern, "server/discover", nil, &result); err != nil {
		return DiscoverResult{}, err
	}
	return result, nil
}

// Initialize performs the legacy MCP initialize handshake. Modern clients use
// Connect instead; the handshake never negotiates a modern revision.
func (c *Client) Initialize(ctx context.Context, info ClientInfo) (InitializeResult, error) {
	if info.Name != "" {
		c.mu.Lock()
		c.info = info
		if c.info.Version == "" {
			c.info.Version = Version
		}
		c.mu.Unlock()
	}

	var result InitializeResult
	params := InitializeParams{ProtocolVersion: DefaultProtocolVersion, ClientInfo: c.clientInfo()}
	if err := c.call(ctx, EraLegacy, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if err := c.notify(ctx, EraLegacy, "notifications/initialized", nil); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

// ListTools returns the server's tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result ListToolsResult
	if err := c.call(ctx, c.Era(), "tools/list", nil, &result); err != nil {
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

	var result CallToolResult
	params := CallToolParams{Name: name, Arguments: raw}
	if err := c.call(ctx, c.Era(), "tools/call", params, &result); err != nil {
		return CallToolResult{}, err
	}
	return result, nil
}

// Listen opens a subscriptions/listen stream. It returns once the server
// acknowledged the subscription.
func (c *Client) Listen(ctx context.Context, filter SubscriptionFilter) (*ClientSubscription, error) {
	if c.Era() != EraModern {
		return nil, fmt.Errorf("gmcp: subscriptions require a modern connection")
	}

	id := c.newID()
	sub := &ClientSubscription{
		client: c,
		id:     id,
		events: make(chan Notification, 16),
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	c.registerSub(sub)

	params := mustJSON(SubscriptionsListenParams{Notifications: filter})
	request := Request{JSONRPC: JSONRPCVersion, ID: id, Method: "subscriptions/listen", Params: params}
	if err := c.writeRequest(EraModern, request); err != nil {
		c.unregisterSub(sub)
		return nil, err
	}

	select {
	case <-sub.ready:
		return sub, nil
	case <-ctx.Done():
		c.unregisterSub(sub)
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.failure()
	}
}

// Notify sends a client notification.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return c.notify(ctx, c.Era(), method, params)
}

// Close stops the client's reader. Open subscriptions end with it.
func (c *Client) Close() error {
	c.stop(nil)
	return nil
}

func (c *Client) call(ctx context.Context, era Era, method string, params any, out any) error {
	response, err := c.roundTrip(ctx, era, method, params, c.newID())
	if err != nil {
		return err
	}
	if len(response.Result) > 0 && out != nil {
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("gmcp: decode %s result: %w", method, err)
		}
	}
	return nil
}

func (c *Client) roundTrip(ctx context.Context, era Era, method string, params any, id json.RawMessage) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	var raw json.RawMessage
	if params != nil {
		raw = mustJSON(params)
	}
	request := Request{JSONRPC: JSONRPCVersion, ID: id, Method: method, Params: raw}

	waiter := make(chan Response, 1)
	c.mu.Lock()
	c.pending[string(id)] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
	}()

	if err := c.writeRequest(era, request); err != nil {
		return Response{}, err
	}

	select {
	case response := <-waiter:
		if response.Error != nil {
			return Response{}, response.Error
		}
		return response, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-c.done:
		return Response{}, c.failure()
	}
}

func (c *Client) writeRequest(era Era, request Request) error {
	if era == EraModern {
		request.Params = withRequestMeta(request.Params, c.requestMeta())
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("gmcp: marshal request: %w", err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("gmcp: write request: %w", err)
	}
	return nil
}

func (c *Client) notify(ctx context.Context, era Era, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notification := Notification{JSONRPC: JSONRPCVersion, Method: method}
	if params != nil {
		notification.Params = mustJSON(params)
	}
	if era == EraModern {
		notification.Params = withRequestMeta(notification.Params, c.requestMeta())
	}
	raw, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("gmcp: marshal notification: %w", err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("gmcp: write notification: %w", err)
	}
	return nil
}

func (c *Client) readLoop() {
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			c.stop(err)
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var inbound struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method,omitempty"`
			Params  json.RawMessage `json:"params,omitempty"`
			Result  json.RawMessage `json:"result,omitempty"`
			Error   *Error          `json:"error,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &inbound); err != nil {
			continue
		}
		if inbound.Method != "" {
			c.deliverNotification(inbound.Method, inbound.Params)
			continue
		}
		c.resolve(inbound.ID, Response{
			JSONRPC: JSONRPCVersion,
			ID:      inbound.ID,
			Result:  inbound.Result,
			Error:   inbound.Error,
		})
	}
}

func (c *Client) resolve(id json.RawMessage, response Response) {
	c.mu.Lock()
	waiter, pending := c.pending[string(id)]
	if !pending {
		if sub, ok := c.subs[string(id)]; ok {
			delete(c.subs, string(id))
			c.mu.Unlock()
			sub.finish(nil)
			return
		}
	}
	c.mu.Unlock()
	if pending {
		waiter <- response
	}
}

func (c *Client) deliverNotification(method string, params json.RawMessage) {
	var envelope struct {
		Meta struct {
			SubscriptionID json.RawMessage `json:"io.modelcontextprotocol/subscriptionId"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(params, &envelope)
	if len(envelope.Meta.SubscriptionID) == 0 {
		return
	}

	c.mu.Lock()
	sub, ok := c.subs[string(envelope.Meta.SubscriptionID)]
	c.mu.Unlock()
	if !ok {
		return
	}

	if method == NotifySubscriptionsAck {
		var ack SubscriptionsAcknowledgedParams
		_ = json.Unmarshal(params, &ack)
		sub.setGranted(ack.Notifications)
		return
	}
	sub.push(Notification{JSONRPC: JSONRPCVersion, Method: method, Params: params})
}

func (c *Client) registerSub(sub *ClientSubscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[string(sub.id)] = sub
}

func (c *Client) unregisterSub(sub *ClientSubscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, string(sub.id))
}

func (c *Client) newID() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return json.RawMessage(strconv.FormatInt(c.nextID, 10))
}

func (c *Client) setEra(era Era) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.era = era
}

func (c *Client) requestMeta() RequestMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.info
	return RequestMeta{
		ProtocolVersion:    c.version,
		ClientInfo:         &info,
		ClientCapabilities: c.capabilities,
	}
}

func (c *Client) clientInfo() ClientInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *Client) stop(err error) {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		if err != nil && c.readErr == nil {
			c.readErr = err
		}
		subs := make([]*ClientSubscription, 0, len(c.subs))
		for _, sub := range c.subs {
			subs = append(subs, sub)
		}
		c.subs = map[string]*ClientSubscription{}
		c.mu.Unlock()

		close(c.done)
		for _, sub := range subs {
			sub.finish(c.failure())
		}
	})
}

func (c *Client) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return fmt.Errorf("gmcp: connection closed: %w", c.readErr)
	}
	return errors.New("gmcp: connection closed")
}

// probeContext bounds the era probe when the caller set no deadline, so a
// legacy server that ignores unknown methods cannot hang the connection.
func probeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 5*time.Second)
}

// supportedModernVersion picks a revision from an
// UnsupportedProtocolVersionError that this client can speak.
func supportedModernVersion(rpcErr *Error) string {
	payload, ok := rpcErr.Data.(map[string]any)
	if !ok {
		return ""
	}
	values, ok := payload["supported"].([]any)
	if !ok {
		return ""
	}
	for _, value := range values {
		version, ok := value.(string)
		if ok && version > ProtocolLegacy {
			return version
		}
	}
	return ""
}

// ClientSubscription is one open subscriptions/listen stream.
//
// Notifications are buffered; a subscriber that falls behind loses the events
// it did not read, because the reader cannot block on one stream.
type ClientSubscription struct {
	client *Client
	id     json.RawMessage

	mu     sync.Mutex
	filter SubscriptionFilter
	err    error

	pushMu sync.Mutex
	closed bool

	events chan Notification
	ready  chan struct{}
	done   chan struct{}
	once   sync.Once
}

// ID is the JSON-RPC id that opened the stream.
func (sub *ClientSubscription) ID() json.RawMessage { return sub.id }

// Filter is the notification set the server agreed to honor.
func (sub *ClientSubscription) Filter() SubscriptionFilter {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.filter
}

// Notifications yields the stream's notifications; it closes when the stream ends.
func (sub *ClientSubscription) Notifications() <-chan Notification { return sub.events }

// Done is closed when the subscription ends.
func (sub *ClientSubscription) Done() <-chan struct{} { return sub.done }

// Err reports why the stream ended, or nil for a graceful end.
func (sub *ClientSubscription) Err() error {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.err
}

// Close ends the subscription and tells the server it was the client's decision.
func (sub *ClientSubscription) Close() error {
	sub.client.unregisterSub(sub)
	sub.finish(nil)
	return sub.client.notify(context.Background(), EraModern, NotifyCancelled, map[string]any{
		"requestId": sub.id,
		"reason":    "client closed the subscription",
	})
}

func (sub *ClientSubscription) setGranted(filter SubscriptionFilter) {
	sub.mu.Lock()
	sub.filter = filter
	sub.mu.Unlock()
	sub.once.Do(func() { close(sub.ready) })
}

func (sub *ClientSubscription) push(notification Notification) {
	sub.pushMu.Lock()
	defer sub.pushMu.Unlock()
	if sub.closed {
		return
	}
	select {
	case sub.events <- notification:
	default:
	}
}

func (sub *ClientSubscription) finish(err error) {
	sub.mu.Lock()
	if sub.err == nil {
		sub.err = err
	}
	sub.mu.Unlock()

	sub.once.Do(func() { close(sub.ready) })

	sub.pushMu.Lock()
	if !sub.closed {
		sub.closed = true
		close(sub.done)
		close(sub.events)
	}
	sub.pushMu.Unlock()
}
