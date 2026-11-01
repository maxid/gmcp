# Roadmap

- [x] JSON-RPC 2.0 types and error codes
- [x] MCP initialize / ping / tools/list / tools/call
- [x] Server and client over newline-delimited streams
- [x] Streamable HTTP transport (request/response + batch for legacy)
- [x] Dual-era protocol support: modern `_meta` requests, `server/discover`,
      `UnsupportedProtocolVersionError`, `MCP-Protocol-Version` validation
- [x] `subscriptions/listen` on stdio and over SSE, with acknowledgements and
      graceful closure
- [ ] Resources and prompts
- [ ] Multi-round-trip requests (`input_required` results)
- [ ] Authentication hooks
- [ ] v1.0 API stabilization
