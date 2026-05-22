// Package i18n provides shared language detection utilities for openPE
// adapters, the CLI, and the HTTP server.
//
// The current scope is intentionally narrow: it only answers whether a given
// language tag should produce English output, mirroring the historical
// per-adapter isEnglish helpers in preview/delivery/codex/cmd. It is NOT a
// full i18n / l10n framework, and it does not currently load message
// catalogs.
//
// New callers that need to branch between English and the default localized
// (Chinese) output should use IsEnglish instead of duplicating the switch
// statement.
package i18n

import "strings"

// IsEnglish reports whether the given language tag indicates English output.
//
// The accepted set is intentionally minimal and matches the historical
// behaviour of openPE adapters before the i18n package existed:
//
//   - "en"        (case-insensitive)
//   - "en-us"     (case-insensitive, e.g. "en-US")
//   - "english"   (case-insensitive)
//
// Surrounding whitespace is tolerated. All other values, including the empty
// string and locale variants such as "en-GB", return false so callers fall
// back to their default localized output.
//
// IsEnglish does not currently treat "en-GB", "en-AU" or other English
// regional locales as English; this matches the existing adapter behaviour
// and avoids surprising changes during the contract-baseline phase. Future
// adjustments should ship together with updated callers and an explicit test
// matrix change.
func IsEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}
