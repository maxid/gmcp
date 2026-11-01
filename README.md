English | [简体中文](README.zh-CN.md)

# gmcp

A Model Context Protocol implementation for Go: a server and a client over
JSON-RPC 2.0, on newline-delimited streams (stdio or any `io.Reader`/`io.Writer`)
and on Streamable HTTP.

> Status: **experimental scaffold** (v0.0.0-dev). The API is not stable yet.

## Protocol eras

gmcp speaks both eras of the protocol and picks the right one per request:

- **Modern** (`2026-07-28`): no handshake and no session. Every request carries
  its protocol version, client identity and client capabilities in `_meta`;
  `server/discover` reports the revisions and capabilities a server offers.
- **Legacy** (`2025-11-25` and earlier): the `initialize` handshake. A
  dual-era server answers it with a legacy revision — never a modern one.

An unknown revision is rejected with `UnsupportedProtocolVersionError`
(`-32022`) listing the revisions the server does serve.

## Features

- JSON-RPC 2.0 request/response/notification types and error codes.
- `initialize`, `ping`, `server/discover`, `tools/list`, `tools/call`.
- `subscriptions/listen`: long-lived change notifications, acknowledged before
  the first event, multiplexed by subscription id.
- `Server` with tool registration, era dispatch and subscription handling.
- `Client` with era probing (`Connect`), `Initialize`, `ListTools`, `CallTool`
  and `Listen`.
- Tool failures are returned as `CallToolResult{IsError: true}`, not transport
  errors.

## Example

```bash
go run ./examples/echo
```

The example wires a server and a client through `net.Pipe`, so it runs without
touching the process's stdin/stdout.

## Client

```go
client := gmcp.NewClient(stdin, stdout)
discover, err := client.Connect(ctx, gmcp.Options{
    Info: gmcp.ClientInfo{Name: "my-agent", Version: "1.0.0"},
})
// client.Era() is now gmcp.EraModern or gmcp.EraLegacy.
```

`Connect` probes with `server/discover` and falls back to the `initialize`
handshake when the server answers like a legacy one. `Initialize` stays
available for callers that know they want the handshake.

## HTTP transport

```go
mux.Handle("/mcp", server.HTTPHandler())
client := gmcp.NewHTTPClient("https://app/mcp", nil)
client.SetRequestMeta(gmcp.RequestMeta{
    ProtocolVersion: gmcp.ProtocolModern,
    ClientInfo:      &gmcp.Implementation{Name: "app", Version: "1.0.0"},
})
```

- A modern request must carry `MCP-Protocol-Version`, matching its `_meta`;
  a mismatch or a missing header is answered with `400` and
  `HeaderMismatchError` (`-32020`).
- A request is answered with `application/json`; a `subscriptions/listen`
  request is answered with `text/event-stream` and stays open.
- Notifications are answered with `202` and no body.
- JSON-RPC batches are legacy-only: a batch carrying modern `_meta` is
  rejected with `-32600`.

## Subscriptions

```go
server.SetSubscriptionHandler(func(sub *gmcp.Subscription) {
    defer sub.Finish()
    <-sub.Done()
    // sub.ToolsListChanged() / sub.ResourceUpdated(uri) / ...
})

sub, err := client.Listen(ctx, gmcp.SubscriptionFilter{ToolsListChanged: true})
for notification := range sub.Notifications() {
    // ...
}
```

The server acknowledges with the subset of the filter it can honor, then sends
only those notification types. A client that falls behind on a stream loses the
notifications it did not read.

## Install

```bash
go get github.com/maxid/gmcp
```

## Transport

On a stream, the wire format is one JSON-RPC message per line — enough for
stdio and in-process pipes. On HTTP, one message per POST.

## License

MIT. See [LICENSE](LICENSE).
