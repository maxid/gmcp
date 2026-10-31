English | [简体中文](README.zh-CN.md)

# gmcp

A Model Context Protocol implementation for Go: a server and a client over
newline-delimited JSON-RPC 2.0 streams (stdio or any `io.Reader`/`io.Writer`).

> Status: **experimental scaffold** (v0.0.0-dev). The API is not stable yet.

## Features

- JSON-RPC 2.0 request/response/notification types and error codes.
- MCP `initialize`, `ping`, `tools/list` and `tools/call`.
- `Server` with tool registration and newline-delimited framing.
- `Client` with `Initialize`, `ListTools`, `CallTool` and `Notify`.
- Tool failures are returned as `CallToolResult{IsError: true}`, not transport
  errors.

## Example

```bash
go run ./examples/echo
```

The example wires a server and a client through `net.Pipe`, so it runs without
touching the process's stdin/stdout.

## Install

```bash
go get github.com/maxid/gmcp
```

## Transport

The wire format is one JSON-RPC message per line. This is enough for stdio and
in-process pipes. Streamable HTTP and SSE transports are planned.

## License

MIT. See [LICENSE](LICENSE).
