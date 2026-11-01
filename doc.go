// Package gmcp implements the Model Context Protocol over JSON-RPC 2.0.
//
// It serves both protocol eras. A request whose params carry modern _meta is
// answered statelessly; an initialize request opens the legacy handshake. The
// same server can answer both on one connection.
//
// Transports: newline-delimited streams (stdio or any io.Reader/io.Writer) and
// Streamable HTTP, where subscriptions/listen answers with an SSE stream.
//
// Status: experimental scaffold (v0.0.0-dev). The API is not stable yet.
package gmcp
