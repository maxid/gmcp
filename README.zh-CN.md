[English](README.md) | 简体中文

# gmcp

Go 的 Model Context Protocol 实现：基于换行分隔 JSON-RPC 2.0 流的服务端与客户端
（stdio 或任意 `io.Reader`/`io.Writer`）。

> 状态：**实验性脚手架**（v0.0.0-dev）。API 尚不稳定。

## 特性

- JSON-RPC 2.0 请求/响应/通知类型与错误码。
- MCP 的 `initialize`、`ping`、`tools/list`、`tools/call`。
- `Server`：工具注册与换行分隔帧。
- `Client`：`Initialize`、`ListTools`、`CallTool`、`Notify`。
- 工具失败以 `CallToolResult{IsError: true}` 返回，不当作传输错误。

## 示例

```bash
go run ./examples/echo
```

示例用 `net.Pipe` 把服务端与客户端接起来，不占用进程的 stdin/stdout。

## 安装

```bash
go get github.com/maxid/gmcp
```

## 传输

线上格式是“一行一条 JSON-RPC 消息”。这足够 stdio 与进程内管道；streamable HTTP
与 SSE 传输在规划中。

## 许可证

MIT。见 [LICENSE](LICENSE)。
