# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Dual-era protocol support. A request carrying modern `_meta`
  (`io.modelcontextprotocol/protocolVersion`) is answered statelessly; an
  `initialize` request keeps the legacy handshake. Both work on one connection.
- `server/discover`, returning `capabilities`, `supportedVersions`, `ttlMs` and
  `cacheScope`, plus `SetInstructions`, `SetDiscoverTTL` and
  `SetDiscoverCacheScope` to shape it.
- `subscriptions/listen` on stdio and over SSE (`text/event-stream`,
  `X-Accel-Buffering: no`, keep-alive comments), with the mandated
  `notifications/subscriptions/acknowledged` before any event, subscription ids
  in `_meta`, and a completion result on graceful closure. Server side:
  `SetSubscriptionHandler`, the `*Subscription` handed to it
  (`Finish`/`Close`/`Notify`), and `Server.ToolsChanged`.
- Client: `Connect` with era probing (`server/discover` on stdio, `400` body
  inspection on HTTP), `Discover`, `Listen`, and `Era`.
- `HTTPClient.SetRequestMeta`, `Discover` and `Listen`.
- Typed `UnsupportedProtocolVersionError` (`-32022`) and `HeaderMismatchError`
  (`-32020`), `CodeHeaderMismatch`, `CodeUnsupportedProtocolVersion`,
  `NewErrorWithData`.

### Changed

- **Behaviour change:** `DefaultProtocolVersion` is now `2025-11-25`, the
  version the legacy `initialize` handshake negotiates. It never reports a
  modern revision. Callers that relied on the handshake answering `2026-07-28`
  must move to `_meta` through `Connect`.
- `ServerInfo`/`ClientInfo` are aliases of `Implementation`, which carries an
  optional `Description`; modern results report the server identity in
  `_meta.serverInfo` and carry `resultType`.
- Over HTTP, a modern request must carry `MCP-Protocol-Version` matching its
  `_meta`; a mismatch or a missing header is answered with `400` and
  `-32020`. A batch carrying modern `_meta` is rejected with `-32600`.
- The package is documented as serving both eras; `doc.go` and the READMEs
  describe the two transports instead of calling SSE unimplemented.

## [0.0.0-dev]

- Initial scaffold: JSON-RPC types, MCP tool server, newline-delimited client,
  initialize/list/call/notify and examples.
