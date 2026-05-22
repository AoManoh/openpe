package integration

import "context"

// IDEPaths describes where a particular IDE keeps its Electron bundle and
// supporting files on the current machine. PathResolver implementations in
// each installer subproject return one of these.
type IDEPaths struct {
	// AppRoot is the IDE installation root, e.g. /Applications/Windsurf.app
	// on macOS or C:\Users\<u>\AppData\Local\Programs\Windsurf on Windows.
	AppRoot string
	// BundleFile is the absolute path to the Electron bundle that receives
	// the injection (typically workbench.desktop.main.js).
	BundleFile string
	// ProductFile is the absolute path to product.json, used to bypass the
	// Electron resource checksum guard.
	ProductFile string
	// BackupDir is the installer-local directory where the original bundle
	// is preserved before injection. Restoring from this directory must
	// recover the IDE to its pre-injection state byte-for-byte.
	BackupDir string
}

// InjectStatus reports the current state of an IDE injection. Returned by
// Injector.Status without performing any mutation.
type InjectStatus struct {
	// Injected is true when the live bundle currently contains the openPE
	// injection markers.
	Injected bool
	// InjectVersion is the version string recorded inside the marker meta,
	// empty when not injected.
	InjectVersion string
	// BackupExists is true when a usable backup file is present in the
	// installer-local backup directory.
	BackupExists bool
	// IDEVersion is the version reported by the IDE's product.json, or
	// empty when unavailable.
	IDEVersion string
	// LiveChecksum is the SHA-256 of the current on-disk bundle.
	LiveChecksum string
	// BackupChecksum is the SHA-256 of the most recent backup, or empty
	// when no backup exists.
	BackupChecksum string
}

// InstallOptions controls how an Injector performs its work. DisclaimerAccepted
// MUST be true for any mutating call to succeed.
type InstallOptions struct {
	// AppDirOverride lets the user point the installer at a non-default IDE
	// install location (e.g. portable installs or sandboxed test fixtures).
	AppDirOverride string
	// DryRun reports the actions that would be taken without touching disk.
	DryRun bool
	// DisclaimerAccepted MUST be set to true by the CLI front-end after the
	// user has explicitly acknowledged the experimental + EULA risk. Installers
	// must refuse to mutate state when this is false.
	DisclaimerAccepted bool
}

// Injector is the uniform surface every IDE patch installer satisfies. Each
// subproject (extensions/openpe-*-patch/) typically wraps a native Python
// installer; this Go interface is used by shared tooling, Go-side tests, and
// any future Go-native installers.
//
// Implementations must:
//
//   - Be safe to call concurrently from at most one goroutine (no internal
//     locking required; callers serialise install/uninstall).
//   - Treat Install/Uninstall as idempotent: re-running Install when already
//     injected must not nest markers; Uninstall when nothing is injected must
//     be a no-op success.
//   - Refuse Install when InstallOptions.DisclaimerAccepted is false.
type Injector interface {
	// Name returns the lower-case IDE identifier, e.g. "windsurf", "cursor".
	Name() string
	// ResolvePaths discovers the IDE's install paths on the current machine.
	// Honours opts.AppDirOverride when non-empty.
	ResolvePaths(ctx context.Context, opts InstallOptions) (*IDEPaths, error)
	// Status reports the current injection state without mutating anything.
	Status(ctx context.Context, opts InstallOptions) (*InjectStatus, error)
	// Install performs the injection. Must refuse unless
	// opts.DisclaimerAccepted is true.
	Install(ctx context.Context, opts InstallOptions) error
	// Uninstall restores the IDE to its pre-injection state from the backup.
	Uninstall(ctx context.Context, opts InstallOptions) error
}

// ErrDisclaimerRequired is returned by Injector.Install when the caller has
// not set InstallOptions.DisclaimerAccepted to true. Front-ends should display
// the experimental + EULA risk text and require an explicit user confirmation
// before retrying with DisclaimerAccepted = true.
var ErrDisclaimerRequired = disclaimerError{}

type disclaimerError struct{}

func (disclaimerError) Error() string {
	return "integration: disclaimer not accepted; installer requires explicit user opt-in to experimental + EULA risk"
}
