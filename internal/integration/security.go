package integration

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
)

// TokenByteLength is the entropy size used for bearer tokens. 32 bytes
// (256 bits) is well beyond the 128-bit floor recommended by OWASP for
// session identifiers.
const TokenByteLength = 32

// TokenStringLength is the length of the hex-encoded token string.
const TokenStringLength = TokenByteLength * 2

// GenerateToken returns a fresh hex-encoded bearer token sourced from
// crypto/rand. The returned string is TokenStringLength characters long
// and contains only lower-case hex digits.
func GenerateToken() (string, error) {
	buf := make([]byte, TokenByteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// TokensEqual compares two bearer tokens in constant time, returning true
// only when both are non-empty, the same length, and identical. Empty
// tokens never match anything (defence against accidental zero-value
// comparisons).
func TokensEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidateTokenShape ensures a token string looks like one produced by
// GenerateToken. Used to reject obviously malformed values before any
// timing-sensitive comparison.
func ValidateTokenShape(token string) error {
	if token == "" {
		return errors.New("token: empty")
	}
	if len(token) != TokenStringLength {
		return errors.New("token: unexpected length")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return errors.New("token: not hex-encoded")
	}
	return nil
}
