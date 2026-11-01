[English](CHANGELOG.md) | 简体中文

# 变更日志

本文件记录本项目的重要变更。

## [Unreleased]

### 新增

- 双时代协议支持。params 带 modern `_meta`（`io.modelcontextprotocol/protocolVersion`）
  的请求按无状态处理；`initialize` 请求继续走 legacy 握手。两者可在同一连接上并存。
- `server/discover`：返回 `capabilities`、`supportedVersions`、`ttlMs`、`cacheScope`；
  配套 `SetInstructions`、`SetDiscoverTTL`、`SetDiscoverCacheScope`。
- stdio 与 SSE 上的 `subscriptions/listen`（`text/event-stream`、`X-Accel-Buffering: no`、
  保活注释），先发规范要求的 `notifications/subscriptions/acknowledged` 再发事件，
  通知带订阅 id，优雅结束时回结果。服务端：`SetSubscriptionHandler`、
  `*Subscription` 的 `Finish`/`Close`/`Notify`，以及 `Server.ToolsChanged`。
- 客户端：带时代探测的 `Connect`（stdio 用 `server/discover`，HTTP 看 `400` body）、
  `Discover`、`Listen`、`Era`。
- `HTTPClient.SetRequestMeta`、`Discover`、`Listen`。
- 类型化的 `UnsupportedProtocolVersionError`（`-32022`）与 `HeaderMismatchError`（`-32020`），
  以及 `CodeHeaderMismatch`、`CodeUnsupportedProtocolVersion`、`NewErrorWithData`。

### 变更

- **行为变更：** `DefaultProtocolVersion` 改为 `2025-11-25`，即 legacy `initialize` 协商的版本。
  握手不再下发 modern 版本。依赖握手返回 `2026-07-28` 的调用方需改用 `Connect` 走 `_meta`。
- `ServerInfo`/`ClientInfo` 成为 `Implementation` 的别名，后者多了可选 `Description`；
  modern 结果在 `_meta.serverInfo` 报告服务端身份，并带 `resultType`。
- HTTP 上 modern 请求必须带与 `_meta` 一致的 `MCP-Protocol-Version`；缺头或不一致返回
  `400` 与 `-32020`。带 modern `_meta` 的批量请求以 `-32600` 拒绝。
- 包文档改为“两代都支持”；`doc.go` 与中英 README 改写为描述两种传输，不再写 SSE 未实现。

## [0.0.0-dev]

- 初始脚手架：JSON-RPC 类型、MCP tool server、按行分隔的 client、initialize/list/call/notify 与示例。
