// Package useragent centralizes the HTTP User-Agent openPE presents to model
// gateways. It is a leaf package so both provider clients (openai, anthropic)
// can import it without creating a cycle with the parent providers package.
package useragent

// Value identifies openPE truthfully to gateways. Some public new-api
// gateways run client-restriction plugins that reject Go's default
// "Go-http-client/*" UA as an anonymous bot (observed 2026-07-02 on
// muyuan.do: HTTP 403 channel:client_restricted "does not allow the current
// client (detected: Go-http-client/2.0)"). Sending a real product UA keeps
// requests identifiable and unblocked without impersonating another client.
// Kept to a bare product token (no URL comment): a URL inside the UA adds no
// value for gateway operators and can trip WAF/injection heuristics.
const Value = "openpe"
