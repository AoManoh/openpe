from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
from dataclasses import asdict, dataclass, replace
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

from .backup_transaction import (
    BackupTransaction,
    TransactionError,
    TransactionManifest,
    _path_key,
    load_transaction,
    manifest_bytes,
    validate_transaction_for_restore,
)
from .bundle import _atomic_write, _durable_mkdir, checksum
from .paths import IDEPaths


class OperationError(Exception):
    pass


@dataclass(frozen=True)
class OperationManifest:
    schema_version: int
    operation_id: str
    state: str
    kind: str
    transaction_id: str
    profile_id: str
    install_id: str
    product_commit: str
    payload_kind: str
    trusted_build_id: str
    trusted_bundle_original_sha256: str
    trusted_product_original_sha256: str
    previous_transaction_state: str
    bundle_before_sha256: str
    product_before_sha256: str
    bundle_target_sha256: str
    product_target_sha256: str
    bundle_backup_sha256: str
    product_backup_sha256: str
    transaction_before_sha256: str
    transaction_target_sha256: str
    created_at: str
    updated_at: str

    @classmethod
    def from_mapping(cls, value: Dict[str, Any]) -> "OperationManifest":
        try:
            manifest = cls(**value)
        except TypeError as exc:
            raise OperationError(f"invalid operation manifest: {exc}") from exc
        strings = tuple(
            item
            for key, item in asdict(manifest).items()
            if key != "schema_version"
        )
        if not isinstance(manifest.schema_version, int) or not all(
            isinstance(item, str) for item in strings
        ):
            raise OperationError("invalid operation manifest field types")
        if manifest.schema_version != 2:
            raise OperationError(
                f"unsupported operation schema {manifest.schema_version}"
            )
        if manifest.state not in {"active", "rolled_back", "completed"}:
            raise OperationError(f"invalid operation state {manifest.state!r}")
        if manifest.kind not in {"install", "refresh"}:
            raise OperationError(f"invalid operation kind {manifest.kind!r}")
        if manifest.payload_kind not in {"probe", "regular"}:
            raise OperationError(f"invalid payload kind {manifest.payload_kind!r}")
        for label, component in (
            ("operation id", manifest.operation_id),
            ("transaction id", manifest.transaction_id),
            ("product commit", manifest.product_commit),
        ):
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", component):
                raise OperationError(f"invalid {label}: {component!r}")
        hashes = (
            manifest.bundle_before_sha256,
            manifest.product_before_sha256,
            manifest.bundle_target_sha256,
            manifest.product_target_sha256,
            manifest.bundle_backup_sha256,
            manifest.product_backup_sha256,
            manifest.transaction_before_sha256,
            manifest.transaction_target_sha256,
        )
        if not all(re.fullmatch(r"[0-9a-f]{64}", item) for item in hashes):
            raise OperationError("operation checksums are incomplete or malformed")
        trusted_hashes = (
            manifest.trusted_bundle_original_sha256,
            manifest.trusted_product_original_sha256,
        )
        if manifest.payload_kind == "probe" and (
            not manifest.trusted_build_id
            or not all(re.fullmatch(r"[0-9a-f]{64}", item) for item in trusted_hashes)
        ):
            raise OperationError("probe operation provenance is incomplete")
        return manifest


@dataclass(frozen=True)
class PatchOperation:
    root: Path
    manifest_path: Path
    bundle_backup: Path
    product_backup: Path
    transaction_backup: Path
    transaction_target: Path
    transaction: BackupTransaction
    manifest: OperationManifest


def _timestamp() -> str:
    return datetime.now(tz=timezone.utc).isoformat().replace("+00:00", "Z")


def _sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _safe_id(value: str, label: str) -> str:
    value = value.strip()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", value):
        raise OperationError(f"invalid {label}: {value!r}")
    return value


def _operation_root(transaction: BackupTransaction, operation_id: str) -> Path:
    base = (transaction.root / "operations").resolve()
    root = (base / _path_key(operation_id)).resolve()
    try:
        contained = os.path.commonpath((str(base), str(root))) == str(base)
    except ValueError as exc:
        raise OperationError(f"invalid operation path: {exc}") from exc
    if not contained:
        raise OperationError("operation path escapes transaction root")
    return root


def _write_manifest(operation: PatchOperation) -> PatchOperation:
    data = json.dumps(
        asdict(operation.manifest),
        ensure_ascii=False,
        indent=2,
        sort_keys=False,
    ).encode("utf-8") + b"\n"
    try:
        _atomic_write(operation.manifest_path, data, mode=0o600)
    except OSError as exc:
        raise OperationError(f"cannot write operation manifest: {exc}") from exc
    return operation


def _validate_identity(operation: PatchOperation, paths: IDEPaths) -> None:
    if paths.profile is None or paths.product is None:
        raise OperationError("current product is unsupported")
    manifest = operation.manifest
    expected = (
        (manifest.profile_id, paths.profile.profile_id, "profile"),
        (manifest.install_id, paths.install_id, "install"),
        (manifest.product_commit, paths.product.commit, "product commit"),
        (manifest.transaction_id, operation.transaction.manifest.transaction_id, "transaction"),
    )
    for recorded, current, label in expected:
        if recorded != current:
            raise OperationError(
                f"operation {label} mismatch: recorded={recorded!r}, current={current!r}"
            )


def _parse_transaction_bytes(data: bytes, label: str) -> TransactionManifest:
    try:
        value = json.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise OperationError(f"invalid {label} transaction manifest: {exc}") from exc
    if not isinstance(value, dict):
        raise OperationError(f"{label} transaction manifest must be a JSON object")
    try:
        return TransactionManifest.from_mapping(value)
    except TransactionError as exc:
        raise OperationError(f"invalid {label} transaction manifest: {exc}") from exc


def _read_recovery_bytes(
    operation: PatchOperation,
) -> Tuple[bytes, bytes, bytes, bytes]:
    try:
        bundle = operation.bundle_backup.read_bytes()
        product = operation.product_backup.read_bytes()
        transaction_before = operation.transaction_backup.read_bytes()
        transaction_target = operation.transaction_target.read_bytes()
    except OSError as exc:
        raise OperationError(f"cannot read operation recovery files: {exc}") from exc
    manifest = operation.manifest
    actual = (
        (_sha256_bytes(bundle), manifest.bundle_backup_sha256, "bundle backup"),
        (_sha256_bytes(product), manifest.product_backup_sha256, "product backup"),
        (
            _sha256_bytes(transaction_before),
            manifest.transaction_before_sha256,
            "transaction before",
        ),
        (
            _sha256_bytes(transaction_target),
            manifest.transaction_target_sha256,
            "transaction target",
        ),
    )
    for current, expected, label in actual:
        if current != expected:
            raise OperationError(f"operation {label} checksum mismatch")
    if manifest.bundle_backup_sha256 != manifest.bundle_before_sha256:
        raise OperationError("bundle recovery bytes do not match before state")
    if manifest.product_backup_sha256 != manifest.product_before_sha256:
        raise OperationError("product recovery bytes do not match before state")
    before = _parse_transaction_bytes(transaction_before, "before")
    target = _parse_transaction_bytes(transaction_target, "target")
    expected_previous_state = "prepared" if manifest.kind == "install" else "installed"
    if manifest.previous_transaction_state != expected_previous_state:
        raise OperationError("operation previous transaction state is invalid")
    identity = (
        (before.transaction_id, manifest.transaction_id, "before transaction id"),
        (before.profile_id, manifest.profile_id, "before profile"),
        (before.install_id, manifest.install_id, "before install"),
        (before.product_commit, manifest.product_commit, "before product commit"),
        (before.payload_kind, manifest.payload_kind, "before payload kind"),
        (before.trusted_build_id, manifest.trusted_build_id, "before trusted build"),
        (
            before.trusted_bundle_original_sha256,
            manifest.trusted_bundle_original_sha256,
            "before trusted bundle",
        ),
        (
            before.trusted_product_original_sha256,
            manifest.trusted_product_original_sha256,
            "before trusted product",
        ),
        (before.state, expected_previous_state, "before state"),
        (target.transaction_id, manifest.transaction_id, "target transaction id"),
        (target.profile_id, manifest.profile_id, "target profile"),
        (target.install_id, manifest.install_id, "target install"),
        (target.product_commit, manifest.product_commit, "target product commit"),
        (target.state, "installed", "target state"),
        (
            target.bundle_patched_sha256,
            manifest.bundle_target_sha256,
            "target bundle",
        ),
        (
            target.product_patched_sha256,
            manifest.product_target_sha256,
            "target product",
        ),
    )
    for current, expected, label in identity:
        if current != expected:
            raise OperationError(f"operation {label} mismatch")
    before_values = asdict(before)
    target_values = asdict(target)
    for key in before_values:
        if key in {
            "state",
            "bundle_patched_sha256",
            "product_patched_sha256",
            "updated_at",
        }:
            continue
        if before_values[key] != target_values[key]:
            raise OperationError(f"transaction target changed immutable field {key!r}")
    return bundle, product, transaction_before, transaction_target


def _load_operation(
    paths: IDEPaths,
    product_commit: str,
    transaction_id: str,
    operation_id: str,
) -> PatchOperation:
    transaction = load_transaction(paths, product_commit, transaction_id)
    operation_id = _safe_id(operation_id, "operation id")
    root = _operation_root(transaction, operation_id)
    manifest_path = root / "operation.json"
    if manifest_path.is_symlink():
        raise OperationError("operation manifest must not be a symlink")
    try:
        value = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise OperationError(f"cannot read operation {operation_id}: {exc}") from exc
    if not isinstance(value, dict):
        raise OperationError("operation manifest must be a JSON object")
    manifest = OperationManifest.from_mapping(value)
    if manifest.operation_id != operation_id:
        raise OperationError("operation id does not match manifest path")
    if manifest.transaction_id != transaction_id:
        raise OperationError("transaction id does not match operation path")
    operation = PatchOperation(
        root=root,
        manifest_path=manifest_path,
        bundle_backup=root / "bundle.before",
        product_backup=root / "product.before",
        transaction_backup=root / "transaction.before.json",
        transaction_target=root / "transaction.target.json",
        transaction=transaction,
        manifest=manifest,
    )
    for path in (
        operation.bundle_backup,
        operation.product_backup,
        operation.transaction_backup,
        operation.transaction_target,
    ):
        if path.is_symlink():
            raise OperationError("operation recovery files must not be symlinks")
    _validate_identity(operation, paths)
    _read_recovery_bytes(operation)
    return operation


def prepare_operation(
    transaction: BackupTransaction,
    paths: IDEPaths,
    kind: str,
    target_bundle: bytes,
    target_product: bytes,
    operation_id: Optional[str] = None,
) -> PatchOperation:
    if kind not in {"install", "refresh"}:
        raise OperationError(f"unsupported operation kind {kind!r}")
    expected_state = "prepared" if kind == "install" else "installed"
    if transaction.manifest.state != expected_state:
        raise OperationError(
            f"{kind} requires transaction state {expected_state!r}, got {transaction.manifest.state!r}"
        )
    if kind == "refresh":
        try:
            validate_transaction_for_restore(transaction, paths)
        except TransactionError as exc:
            raise OperationError(str(exc)) from exc
    if not target_bundle or not target_product:
        raise OperationError("operation target bundle/product must not be empty")
    try:
        live_bundle = paths.bundle_file.read_bytes()
        live_product = paths.product_file.read_bytes()
        transaction_before = transaction.manifest_path.read_bytes()
    except OSError as exc:
        raise OperationError(f"cannot snapshot operation inputs: {exc}") from exc
    expected_transaction_before = manifest_bytes(transaction.manifest)
    if transaction_before != expected_transaction_before:
        raise OperationError("disk transaction manifest changed before operation prepare")
    before_bundle_sha = _sha256_bytes(live_bundle)
    before_product_sha = _sha256_bytes(live_product)
    target_bundle_sha = _sha256_bytes(target_bundle)
    target_product_sha = _sha256_bytes(target_product)
    expected_bundle_sha = (
        transaction.manifest.bundle_original_sha256
        if kind == "install"
        else transaction.manifest.bundle_patched_sha256
    )
    expected_product_sha = (
        transaction.manifest.product_original_sha256
        if kind == "install"
        else transaction.manifest.product_patched_sha256
    )
    if before_bundle_sha != expected_bundle_sha or before_product_sha != expected_product_sha:
        raise OperationError("live pair does not match transaction before state")
    now = _timestamp()
    target_transaction_manifest = replace(
        transaction.manifest,
        state="installed",
        bundle_patched_sha256=target_bundle_sha,
        product_patched_sha256=target_product_sha,
        updated_at=now,
    )
    transaction_target = manifest_bytes(target_transaction_manifest)
    operation_id = _safe_id(
        operation_id
        or datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        + "-"
        + secrets.token_hex(4),
        "operation id",
    )
    root = _operation_root(transaction, operation_id)
    if root.exists():
        raise OperationError(f"operation already exists: {root}")
    _durable_mkdir(root, mode=0o700)
    bundle_backup = root / "bundle.before"
    product_backup = root / "product.before"
    transaction_backup = root / "transaction.before.json"
    transaction_target_path = root / "transaction.target.json"
    _atomic_write(bundle_backup, live_bundle, mode=0o600)
    _atomic_write(product_backup, live_product, mode=0o600)
    _atomic_write(transaction_backup, transaction_before, mode=0o600)
    _atomic_write(transaction_target_path, transaction_target, mode=0o600)
    operation = PatchOperation(
        root=root,
        manifest_path=root / "operation.json",
        bundle_backup=bundle_backup,
        product_backup=product_backup,
        transaction_backup=transaction_backup,
        transaction_target=transaction_target_path,
        transaction=transaction,
        manifest=OperationManifest(
            schema_version=2,
            operation_id=operation_id,
            state="active",
            kind=kind,
            transaction_id=transaction.manifest.transaction_id,
            profile_id=transaction.manifest.profile_id,
            install_id=transaction.manifest.install_id,
            product_commit=transaction.manifest.product_commit,
            payload_kind=transaction.manifest.payload_kind,
            trusted_build_id=transaction.manifest.trusted_build_id,
            trusted_bundle_original_sha256=(
                transaction.manifest.trusted_bundle_original_sha256
            ),
            trusted_product_original_sha256=(
                transaction.manifest.trusted_product_original_sha256
            ),
            previous_transaction_state=transaction.manifest.state,
            bundle_before_sha256=before_bundle_sha,
            product_before_sha256=before_product_sha,
            bundle_target_sha256=target_bundle_sha,
            product_target_sha256=target_product_sha,
            bundle_backup_sha256=checksum(bundle_backup),
            product_backup_sha256=checksum(product_backup),
            transaction_before_sha256=checksum(transaction_backup),
            transaction_target_sha256=checksum(transaction_target_path),
            created_at=now,
            updated_at=now,
        ),
    )
    operation = _write_manifest(operation)
    _read_recovery_bytes(operation)
    return operation


def _conditional_write(path: Path, expected_sha256: str, target: bytes) -> None:
    try:
        live = path.read_bytes()
    except OSError as exc:
        raise OperationError(f"cannot read live file {path}: {exc}") from exc
    if _sha256_bytes(live) != expected_sha256:
        raise OperationError(f"live file changed before conditional write: {path}")
    try:
        _atomic_write(path, target, mode=0o644)
    except OSError as exc:
        raise OperationError(f"conditional write failed for {path}: {exc}") from exc
    try:
        target_sha = checksum(path)
    except OSError as exc:
        raise OperationError(f"cannot verify conditional write for {path}: {exc}") from exc
    if target_sha != _sha256_bytes(target):
        raise OperationError(f"conditional write verification failed for {path}")


def apply_operation(
    operation: PatchOperation,
    paths: IDEPaths,
    target_bundle: bytes,
    target_product: bytes,
) -> None:
    if operation.manifest.state != "active":
        raise OperationError("only active operations can be applied")
    _validate_identity(operation, paths)
    _read_recovery_bytes(operation)
    manifest = operation.manifest
    if _sha256_bytes(target_bundle) != manifest.bundle_target_sha256:
        raise OperationError("target bundle does not match operation journal")
    if _sha256_bytes(target_product) != manifest.product_target_sha256:
        raise OperationError("target product does not match operation journal")
    try:
        bundle_before = checksum(paths.bundle_file)
        product_before = checksum(paths.product_file)
    except OSError as exc:
        raise OperationError(f"cannot validate live pair before operation: {exc}") from exc
    if bundle_before != manifest.bundle_before_sha256:
        raise OperationError("live bundle is not operation before state")
    if product_before != manifest.product_before_sha256:
        raise OperationError("live product is not operation before state")
    _conditional_write(paths.bundle_file, manifest.bundle_before_sha256, target_bundle)
    _conditional_write(paths.product_file, manifest.product_before_sha256, target_product)
    if checksum(paths.bundle_file) != manifest.bundle_target_sha256:
        raise OperationError("live bundle is not operation target state")
    if checksum(paths.product_file) != manifest.product_target_sha256:
        raise OperationError("live product is not operation target state")


def finalize_operation_transaction(
    operation: PatchOperation,
    paths: IDEPaths,
) -> BackupTransaction:
    if operation.manifest.state != "active":
        raise OperationError("only active operations can be finalized")
    _validate_identity(operation, paths)
    _, _, _, transaction_target = _read_recovery_bytes(operation)
    manifest = operation.manifest
    try:
        bundle_sha = checksum(paths.bundle_file)
        product_sha = checksum(paths.product_file)
        transaction_sha = checksum(operation.transaction.manifest_path)
    except OSError as exc:
        raise OperationError(f"cannot inspect operation target state: {exc}") from exc
    if bundle_sha != manifest.bundle_target_sha256:
        raise OperationError("cannot finalize operation: bundle target mismatch")
    if product_sha != manifest.product_target_sha256:
        raise OperationError("cannot finalize operation: product target mismatch")
    if transaction_sha != manifest.transaction_before_sha256:
        raise OperationError("cannot finalize operation: transaction before mismatch")
    _conditional_write(
        operation.transaction.manifest_path,
        manifest.transaction_before_sha256,
        transaction_target,
    )
    transaction = load_transaction(
        paths,
        manifest.product_commit,
        manifest.transaction_id,
    )
    if checksum(transaction.manifest_path) != manifest.transaction_target_sha256:
        raise OperationError("finalized transaction target verification failed")
    return transaction


def rollback_operation(operation: PatchOperation, paths: IDEPaths) -> PatchOperation:
    if operation.manifest.state != "active":
        raise OperationError("only active operations can be rolled back")
    _validate_identity(operation, paths)
    (
        before_bundle,
        before_product,
        before_transaction,
        _,
    ) = _read_recovery_bytes(operation)
    manifest = operation.manifest
    try:
        live_bundle_sha = checksum(paths.bundle_file)
        live_product_sha = checksum(paths.product_file)
        transaction_sha = checksum(operation.transaction.manifest_path)
    except OSError as exc:
        raise OperationError(f"cannot inspect live state for rollback: {exc}") from exc
    if live_bundle_sha not in {
        manifest.bundle_before_sha256,
        manifest.bundle_target_sha256,
    }:
        raise OperationError("unknown live bundle state; rollback refused")
    if live_product_sha not in {
        manifest.product_before_sha256,
        manifest.product_target_sha256,
    }:
        raise OperationError("unknown live product state; rollback refused")
    if transaction_sha not in {
        manifest.transaction_before_sha256,
        manifest.transaction_target_sha256,
    }:
        raise OperationError("unknown transaction manifest state; rollback refused")
    _conditional_write(
        operation.transaction.manifest_path,
        transaction_sha,
        before_transaction,
    )
    _conditional_write(paths.product_file, live_product_sha, before_product)
    _conditional_write(paths.bundle_file, live_bundle_sha, before_bundle)
    try:
        bundle_sha = checksum(paths.bundle_file)
        product_sha = checksum(paths.product_file)
        transaction_sha = checksum(operation.transaction.manifest_path)
        restored_transaction = load_transaction(
            paths,
            manifest.product_commit,
            manifest.transaction_id,
        )
    except (OSError, TransactionError) as exc:
        raise OperationError(f"cannot verify rolled back operation: {exc}") from exc
    if bundle_sha != manifest.bundle_before_sha256:
        raise OperationError("rolled back bundle verification failed")
    if product_sha != manifest.product_before_sha256:
        raise OperationError("rolled back product verification failed")
    if transaction_sha != manifest.transaction_before_sha256:
        raise OperationError("rolled back transaction verification failed")
    rolled_back = replace(
        operation,
        transaction=restored_transaction,
        manifest=replace(manifest, state="rolled_back", updated_at=_timestamp()),
    )
    return _write_manifest(rolled_back)


def complete_operation(
    operation: PatchOperation,
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> PatchOperation:
    if operation.manifest.state != "active":
        raise OperationError("only active operations can be completed")
    _validate_identity(operation, paths)
    _read_recovery_bytes(operation)
    manifest = operation.manifest
    if checksum(paths.bundle_file) != manifest.bundle_target_sha256:
        raise OperationError("cannot complete operation: bundle target mismatch")
    if checksum(paths.product_file) != manifest.product_target_sha256:
        raise OperationError("cannot complete operation: product target mismatch")
    if checksum(transaction.manifest_path) != manifest.transaction_target_sha256:
        raise OperationError("cannot complete operation: disk transaction target mismatch")
    disk_transaction = load_transaction(
        paths,
        manifest.product_commit,
        manifest.transaction_id,
    )
    if disk_transaction.manifest != transaction.manifest:
        raise OperationError("cannot complete operation: stale transaction object")
    completed = replace(
        operation,
        transaction=disk_transaction,
        manifest=replace(manifest, state="completed", updated_at=_timestamp()),
    )
    return _write_manifest(completed)


def find_active_operation(paths: IDEPaths) -> Optional[PatchOperation]:
    transactions_dir = paths.backup_dir.parent.parent
    standard_layout = (
        paths.backup_dir.name == paths.install_id
        and transactions_dir.name == "transactions"
    )
    if standard_layout:
        scan_root = transactions_dir
        pattern = f"*/{paths.install_id}/*/*/operations/*/operation.json"
    else:
        scan_root = paths.backup_dir
        pattern = "*/*/operations/*/operation.json"
    if not scan_root.is_dir():
        return None
    matches = []
    for manifest_path in scan_root.glob(pattern):
        if manifest_path.is_symlink():
            raise OperationError("operation manifest must not be a symlink")
        try:
            value = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise OperationError(f"cannot inspect operation journal {manifest_path}: {exc}") from exc
        if not isinstance(value, dict):
            raise OperationError(f"operation journal must be a JSON object: {manifest_path}")
        operation_manifest = OperationManifest.from_mapping(value)
        if operation_manifest.state != "active":
            continue
        if standard_layout and manifest_path.parents[5].name != paths.backup_dir.parent.name:
            raise OperationError(
                "active operation exists for the same install under a different profile"
            )
        matches.append(
            _load_operation(
                paths,
                operation_manifest.product_commit,
                operation_manifest.transaction_id,
                operation_manifest.operation_id,
            )
        )
    if len(matches) > 1:
        raise OperationError("multiple active patch operations require manual recovery")
    return matches[0] if matches else None
