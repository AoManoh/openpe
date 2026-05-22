package integration

import (
	"bytes"
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

// HasMarker reports whether bundlePath contains both marker delimiters.
func (FilePatcher) HasMarker(bundlePath string, marker Marker) (bool, error) {
	if err := marker.Validate(); err != nil {
		return false, err
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte(marker.Begin)) && bytes.Contains(data, []byte(marker.End)), nil
}

// Inject writes payload into bundlePath wrapped by marker. When an existing
// marker region is found, its body is replaced in place; otherwise the new
// block is appended. The write is atomic via temp file + rename.
func (FilePatcher) Inject(bundlePath, payload string, marker Marker) error {
	if strings.TrimSpace(payload) == "" {
		return errors.New("inject: payload is empty")
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	block := buildInjectBlock(payload, marker)
	var output []byte
	if existing := locateExistingMarker(data, marker); existing != nil {
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
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	target := filepath.Join(backupDir, fmt.Sprintf("%s.%s.original", base, timestamp))
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

func locateExistingMarker(data []byte, marker Marker) *markerSpan {
	beginIdx := bytes.Index(data, []byte(marker.Begin))
	if beginIdx < 0 {
		return nil
	}
	tail := data[beginIdx:]
	relEnd := bytes.Index(tail, []byte(marker.End))
	if relEnd < 0 {
		return nil
	}
	end := beginIdx + relEnd + len(marker.End)
	// extend the span to include a trailing newline so repeated Inject
	// calls do not accumulate blank lines.
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return &markerSpan{start: beginIdx, end: end}
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
