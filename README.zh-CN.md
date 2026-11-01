[English](README.md) | 简体中文

# gmcp

Go 的 Model Context Protocol 实现：基于 JSON-RPC 2.0，既有换行分隔的流（stdio 或任意
`io.Reader`/`io.Writer`），也有 Streamable HTTP。

> 状态：**实验性脚手架**（v0.0.0-dev）。API 尚不稳定。

## 协议时代

gmcp 同时支持两代协议，按请求判定：

- **Modern**（`2026-07-28`）：无握手、无会话。每个请求在 `_meta` 里带上协议版本、
  客户端身份与客户端能力；`server/discover` 返回服务端支持的版本与能力。
- **Legacy**（`2025-11-25` 及更早）：`initialize` 握手。dual-era 服务端只协商 legacy
  版本，绝不通过握手下发 modern 版本。

不认识的版本以 `UnsupportedProtocolVersionError`（`-32022`）拒绝，并列出服务端支持的版本。

## 特性

- JSON-RPC 2.0 请求/响应/通知类型与错误码。
- `initialize`、`ping`、`server/discover`、`tools/list`、`tools/call`。
- `subscriptions/listen`：长连接变更通知，先确认后发事件，按 subscription id 解复用。
- `Server`：工具注册、按时代分派、订阅处理。
- `Client`：时代探测（`Connect`）、`Initialize`、`ListTools`、`CallTool`、`Listen`。
- 工具失败以 `CallToolResult{IsError: true}` 返回，不当作传输错误。

## 示例

```bash
go run ./examples/echo
```

示例用 `net.Pipe` 把服务端与客户端接起来，不占用进程的 stdin/stdout。

## 客户端

```go
client := gmcp.NewClient(stdin, stdout)
discover, err := client.Connect(ctx, gmcp.Options{
    Info: gmcp.ClientInfo{Name: "my-agent", Version: "1.0.0"},
})
// 之后 client.Era() 为 gmcp.EraModern 或 gmcp.EraLegacy。
```

`Connect` 先用 `server/discover` 探测，服务端表现得像 legacy 时回退到 `initialize` 握手。
明确要走握手的调用方仍可直接用 `Initialize`。

## HTTP 传输

```go
mux.Handle("/mcp", server.HTTPHandler())
client := gmcp.NewHTTPClient("https://app/mcp", nil)
client.SetRequestMeta(gmcp.RequestMeta{
    ProtocolVersion: gmcp.ProtocolModern,
    ClientInfo:      &gmcp.Implementation{Name: "app", Version: "1.0.0"},
})
```

- modern 请求必须带 `MCP-Protocol-Version` 且与 `_meta` 一致；缺头或不一致返回 `400`
  与 `HeaderMismatchError`（`-32020`）。
- 普通请求以 `application/json` 应答；`subscriptions/listen` 以 `text/event-stream` 应答并保持打开。
- 通知以 `202` 应答且无 body。
- JSON-RPC 批量只在 legacy 可用：带 modern `_meta` 的批量请求以 `-32600` 拒绝。

## 订阅

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

服务端先确认它能支持的过滤器子集，之后只发这些类型。流上跟不上的客户端会丢掉未读取的通知。

## 安装

```bash
go get github.com/maxid/gmcp
```

## 传输

在流上，线上格式是“一行一条 JSON-RPC 消息”，足够 stdio 与进程内管道；在 HTTP 上，一次 POST 一条消息。

## 许可证

MIT。见 [LICENSE](LICENSE)。
