// Package integration defines the contract between openPE's local HTTP server
// and third-party IDE patch installers (Windsurf, Cursor, VS Code Composer, ...).
//
// The package intentionally does not import any IDE-specific code. Each IDE
// installer lives in its own sibling subproject under extensions/openpe-*-patch/
// and implements the contracts defined here.
//
// Concepts:
//
//   - LocalServerDescriptor — handshake metadata exchanged via openPE's local
//     server. Each installer reads this to learn the server's base URL and
//     bearer token.
//   - Token primitives — GenerateToken / TokensEqual / ValidateTokenShape
//     provide a small constant-time-safe surface for the auth middleware
//     and installers to share.
//   - InjectorContract — uniform install/uninstall/status surface every IDE
//     installer satisfies. The Go interface is used by shared tooling, tests,
//     and future Go-native installers; Python installers honour the same
//     conceptual contract documented here.
//   - BundlePatcher — generic Electron bundle marker + backup logic. Idempotent
//     marker placement, atomic file writes, and SHA-256 checksums.
//
// Stability: this package is internal/ but its design is the canonical
// reference for any future IDE patch installer subproject. Backwards
// incompatible changes must be coordinated with every existing installer.
//
// Disclaimer: IDE bundle patching is an experimental, opt-in capability that
// modifies third-party software and may violate the host IDE's EULA. Installers
// built on this package MUST require an explicit user disclaimer acceptance
// before performing any mutation.
package integration
