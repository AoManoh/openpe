package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marker delimits an injection inside a host bundle. Markers are designed
// to be:
//
//   - Easy for humans to spot inside a multi-MB minified JS file.
//   - Strict enough that two unrelated tools do not accidentally cross-
//     recognise each other's markers.
//   - Idempotent: a second Inject MUST detect an existing marker and
//     replace its body in place rather than nesting markers.
type Marker struct {
	Begin string
	End   string
}

// DefaultMarker returns the canonical openPE injection markers used by all
// IDE installers.
func DefaultMarker() Marker {
	return Marker{
		Begin: "/* === OPENPE-INJECT-BEGIN === */",
		End:   "/* === OPENPE-INJECT-END === */",
	}
}

// Validate ensures both delimiters are non-empty and unequal.
func (m Marker) Validate() error {
	if strings.TrimSpace(m.Begin) == "" || strings.TrimSpace(m.End) == "" {
		return errors.New("marker: begin/end required")
	}
	if m.Begin == m.End {
		return errors.New("marker: begin and end must differ")
	}
	return nil
}

// BundlePatcher is the generic surface for read / backup / inject / restore
// on an Electron bundle. All IDE installers share this implementation;
// only path resolution differs per IDE.
type BundlePatcher interface {
	// HasMarker reports whether the bundle currently contains both
	// marker delimiters.
	HasMarker(bundlePath string, marker Marker) (bool, error)
	// Inject writes payload into the bundle, replacing any existing marker
	// region in place. Atomic on success.
	Inject(bundlePath, payload string, marker Marker) error
	// Restore copies backupPath onto bundlePath atomically.
	Restore(bundlePath, backupPath string) error
	// Backup copies bundlePath into backupDir, returning the absolute path
	// of the new backup file. Existing backups are kept (timestamp suffix).
	Backup(bundlePath, backupDir string) (backupFile string, err error)
	// Checksum returns the lower-case hex SHA-256 of path.
	Checksum(path string) (string, error)
}

// FilePatcher is the default file-system based BundlePatcher implementation.
// It performs all mutations atomically via temp file + rename and refuses
// to inject empty payloads or malformed markers.
type FilePatcher struct{}

// NewFilePatcher returns the default FilePatcher.
func NewFilePatcher() FilePatcher {
	return FilePatcher{}
}

// HasMarker reports whether bundlePath contains exactly one well-formed
// marker region. The semantics mirror the Python installer's
// bundle.has_marker byte for byte: zero pairs is false, exactly one ordered
// pair is true, and duplicated or out-of-order delimiters are an ERROR — the
// two implementations claim to be mirrors, and the old Contains-only check
// answered true for bundles the Python side rejects as malformed.
func (FilePatcher) HasMarker(bundlePath string, marker Marker) (bool, error) {
	if err := marker.Validate(); err != nil {
		return false, err
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return false, err
	}
	_, present, err := parseMarker(data, marker)
	return present, err
}

// Inject writes payload into bundlePath wrapped by marker. When an existing
// marker region is found, its body is replaced in place; otherwise the new
// block is appended. The write is atomic via temp file + rename. A payload
// that itself contains the marker delimiters is rejected: injecting it
// would produce duplicated markers that every later HasMarker/inject pass
// refuses to touch.
func (FilePatcher) Inject(bundlePath, payload string, marker Marker) error {
	if strings.TrimSpace(payload) == "" {
		return errors.New("inject: payload is empty")
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	if strings.Contains(payload, marker.Begin) || strings.Contains(payload, marker.End) {
		return errors.New("inject: payload must not contain the marker delimiters")
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	existing, _, err := parseMarker(data, marker)
	if err != nil {
		return err
	}
	block := buildInjectBlock(payload, marker)
	var output []byte
	if existing != nil {
		output = append(output, data[:existing.start]...)
		output = append(output, block...)
		output = append(output, data[existing.end:]...)
	} else {
		output = append(output, data...)
		if len(output) > 0 && output[len(output)-1] != '\n' {
			output = append(output, '\n')
		}
		output = append(output, block...)
	}
	return atomicWriteFile(bundlePath, output, 0o644)
}

// Restore copies backupPath onto bundlePath atomically.
func (FilePatcher) Restore(bundlePath, backupPath string) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return atomicWriteFile(bundlePath, data, 0o644)
}

// Backup copies bundlePath into backupDir with a timestamp suffix and
// returns the absolute path of the new file. The backup is written with
// mode 0600 to discourage tampering.
func (FilePatcher) Backup(bundlePath, backupDir string) (string, error) {
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(bundlePath)
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	target := filepath.Join(backupDir, fmt.Sprintf("%s.%s-%s.original", base, timestamp, hex.EncodeToString(nonce[:])))
	if err := atomicWriteFile(target, data, 0o600); err != nil {
		return "", err
	}
	return target, nil
}

// Checksum returns the lower-case hex SHA-256 of path.
func (FilePatcher) Checksum(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type markerSpan struct {
	start int
	end   int
}

func parseMarker(data []byte, marker Marker) (*markerSpan, bool, error) {
	begin := []byte(marker.Begin)
	endMarker := []byte(marker.End)
	beginCount := bytes.Count(data, begin)
	endCount := bytes.Count(data, endMarker)
	if beginCount == 0 && endCount == 0 {
		return nil, false, nil
	}
	if beginCount != 1 || endCount != 1 {
		return nil, false, fmt.Errorf("marker: expected one begin/end pair, got %d/%d", beginCount, endCount)
	}
	beginIdx := bytes.Index(data, begin)
	endIdx := bytes.Index(data, endMarker)
	if beginIdx > endIdx {
		return nil, false, errors.New("marker: end appears before begin")
	}
	end := endIdx + len(endMarker)
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return &markerSpan{start: beginIdx, end: end}, true, nil
}

func buildInjectBlock(payload string, marker Marker) []byte {
	var b bytes.Buffer
	b.WriteString(marker.Begin)
	b.WriteByte('\n')
	b.WriteString(payload)
	if !strings.HasSuffix(payload, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(marker.End)
	b.WriteByte('\n')
	return b.Bytes()
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	return os.Rename(tmpName, path)
}
