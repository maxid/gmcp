package gmcp

import "encoding/json"

// Protocol revisions this package speaks.
const (
	// ProtocolModern is the stateless revision: version, identity and client
	// capabilities travel in each request's _meta.
	ProtocolModern = "2026-07-28"
	// ProtocolLegacy is the newest handshake-based revision.
	ProtocolLegacy = "2025-11-25"
)

// DefaultProtocolVersion is the version answered by the legacy initialize
// handshake. It is never a modern revision: a modern client negotiates through
// _meta instead.
const DefaultProtocolVersion = ProtocolLegacy

// DefaultSupportedVersions lists the revisions a server serves unless
// SetProtocolVersions says otherwise.
var DefaultSupportedVersions = []string{ProtocolModern, ProtocolLegacy}

// Reserved _meta keys.
const (
	MetaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	MetaClientInfo      = "io.modelcontextprotocol/clientInfo"
	MetaClientCaps      = "io.modelcontextprotocol/clientCapabilities"
	MetaServerInfo      = "io.modelcontextprotocol/serverInfo"
	MetaSubscriptionID  = "io.modelcontextprotocol/subscriptionId"
	MetaLogLevel        = "io.modelcontextprotocol/logLevel"
)

// Result types.
const (
	ResultTypeComplete      = "complete"
	ResultTypeInputRequired = "input_required"
)

// Implementation identifies a client or server. Both name and version are
// required on the wire.
type Implementation struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// ClientInfo identifies a client in the legacy handshake.
type ClientInfo = Implementation

// ServerInfo identifies a server in the legacy handshake.
type ServerInfo = Implementation

// InitializeParams is the client's legacy initialize request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

// InitializeResult is the server's legacy initialize response.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities advertises the features a server implements.
type ServerCapabilities struct {
	Tools      *ToolsCapability           `json:"tools,omitempty"`
	Prompts    *ListChangedCapability     `json:"prompts,omitempty"`
	Resources  *ListChangedCapability     `json:"resources,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// ToolsCapability advertises tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ListChangedCapability advertises a feature whose list can change.
type ListChangedCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// RequestMeta is the _meta object carried by a modern request.
type RequestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         *Implementation `json:"io.modelcontextprotocol/clientInfo,omitempty"`
	ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities,omitempty"`
	LogLevel           string          `json:"io.modelcontextprotocol/logLevel,omitempty"`
}

// ResultMeta is the _meta object carried by a modern result.
type ResultMeta struct {
	ServerInfo     *Implementation `json:"io.modelcontextprotocol/serverInfo,omitempty"`
	SubscriptionID json.RawMessage `json:"io.modelcontextprotocol/subscriptionId,omitempty"`
}

// DiscoverResult is the server/discover response.
type DiscoverResult struct {
	Meta              *ResultMeta        `json:"_meta,omitempty"`
	CacheScope        string             `json:"cacheScope"`
	Capabilities      ServerCapabilities `json:"capabilities"`
	Instructions      string             `json:"instructions,omitempty"`
	ResultType        string             `json:"resultType"`
	SupportedVersions []string           `json:"supportedVersions"`
	TTLMs             int64              `json:"ttlMs"`
}

// Tool describes one callable tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListToolsResult is the tools/list response.
type ListToolsResult struct {
	Meta       *ResultMeta `json:"_meta,omitempty"`
	Tools      []Tool      `json:"tools"`
	ResultType string      `json:"resultType,omitempty"`
}

// CallToolParams is the tools/call request.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Content is one piece of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallToolResult is the tools/call response.
type CallToolResult struct {
	Meta       *ResultMeta `json:"_meta,omitempty"`
	Content    []Content   `json:"content"`
	IsError    bool        `json:"isError,omitempty"`
	ResultType string      `json:"resultType,omitempty"`
}

// SubscriptionFilter opts a client in to notification types. Every field is
// opt-in: the server must not send a type the client did not request.
type SubscriptionFilter struct {
	ToolsListChanged      bool     `json:"toolsListChanged,omitempty"`
	PromptsListChanged    bool     `json:"promptsListChanged,omitempty"`
	ResourcesListChanged  bool     `json:"resourcesListChanged,omitempty"`
	ResourceSubscriptions []string `json:"resourceSubscriptions,omitempty"`
}

// SubscriptionsListenParams are the subscriptions/listen request params.
type SubscriptionsListenParams struct {
	Meta          *RequestMeta       `json:"_meta,omitempty"`
	Notifications SubscriptionFilter `json:"notifications"`
}

// SubscriptionsListenResult ends a subscription gracefully.
type SubscriptionsListenResult struct {
	Meta       *ResultMeta `json:"_meta"`
	ResultType string      `json:"resultType"`
}

// SubscriptionsAcknowledgedParams acknowledge an established subscription.
type SubscriptionsAcknowledgedParams struct {
	Meta          *ResultMeta        `json:"_meta"`
	Notifications SubscriptionFilter `json:"notifications"`
}

// Notification method names for subscription streams.
const (
	NotifyToolsListChanged     = "notifications/tools/list_changed"
	NotifyPromptsListChanged   = "notifications/prompts/list_changed"
	NotifyResourcesListChanged = "notifications/resources/list_changed"
	NotifyResourceUpdated      = "notifications/resources/updated"
	NotifySubscriptionsAck     = "notifications/subscriptions/acknowledged"
	NotifyCancelled            = "notifications/cancelled"
)

// ResourceUpdatedParams are the notifications/resources/updated params.
type ResourceUpdatedParams struct {
	Meta *ResultMeta `json:"_meta,omitempty"`
	URI  string      `json:"uri"`
}
