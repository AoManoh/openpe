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

from .bundle import _atomic_write, _durable_mkdir, checksum
from .paths import IDEPaths


class TransactionError(Exception):
    pass


def _safe_component(value: str, label: str) -> str:
    if not isinstance(value, str):
        raise TransactionError(f"invalid {label} type")
    value = value.strip()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", value):
        raise TransactionError(f"invalid {label}: {value!r}")
    return value


def _path_key(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]


def _transaction_root(paths: IDEPaths, product_commit: str, transaction_id: str) -> Path:
    base = paths.backup_dir.expanduser().resolve()
    root = (base / _path_key(product_commit) / _path_key(transaction_id)).resolve()
    try:
        contained = os.path.commonpath((str(base), str(root))) == str(base)
    except ValueError as exc:
        raise TransactionError(f"invalid transaction path: {exc}") from exc
    if not contained:
        raise TransactionError("transaction path escapes backup root")
    return root


@dataclass(frozen=True)
class TransactionManifest:
    schema_version: int
    transaction_id: str
    state: str
    profile_id: str
    install_id: str
    app_root: str
    product_name_short: str
    product_application_name: str
    product_version: str
    product_commit: str
    installer_version: str
    payload_kind: str
    trusted_build_id: str
    trusted_bundle_original_sha256: str
    trusted_product_original_sha256: str
    bundle_backup_name: str
    product_backup_name: str
    bundle_original_sha256: str
    product_original_sha256: str
    bundle_patched_sha256: str
    product_patched_sha256: str
    created_at: str
    updated_at: str

    @classmethod
    def from_mapping(cls, value: Dict[str, Any]) -> "TransactionManifest":
        try:
            manifest = cls(**value)
        except TypeError as exc:
            raise TransactionError(f"invalid transaction manifest: {exc}") from exc
        string_values = (
            manifest.transaction_id,
            manifest.state,
            manifest.profile_id,
            manifest.install_id,
            manifest.app_root,
            manifest.product_name_short,
            manifest.product_application_name,
            manifest.product_version,
            manifest.product_commit,
            manifest.installer_version,
            manifest.payload_kind,
            manifest.trusted_build_id,
            manifest.trusted_bundle_original_sha256,
            manifest.trusted_product_original_sha256,
            manifest.bundle_backup_name,
            manifest.product_backup_name,
            manifest.bundle_original_sha256,
            manifest.product_original_sha256,
            manifest.bundle_patched_sha256,
            manifest.product_patched_sha256,
            manifest.created_at,
            manifest.updated_at,
        )
        if not isinstance(manifest.schema_version, int) or not all(
            isinstance(item, str) for item in string_values
        ):
            raise TransactionError("invalid transaction manifest field types")
        if manifest.schema_version != 2:
            raise TransactionError(
                f"unsupported transaction schema {manifest.schema_version}"
            )
        _safe_component(manifest.transaction_id, "transaction id")
        _safe_component(manifest.product_commit, "product commit")
        if manifest.bundle_backup_name != "bundle.original":
            raise TransactionError("unexpected bundle backup name")
        if manifest.product_backup_name != "product.original":
            raise TransactionError("unexpected product backup name")
        if manifest.state not in {"prepared", "installed", "restoring", "restored"}:
            raise TransactionError(f"invalid transaction state {manifest.state!r}")
        if manifest.payload_kind not in {"probe", "regular"}:
            raise TransactionError(f"invalid payload kind {manifest.payload_kind!r}")
        trusted_hashes = (
            manifest.trusted_bundle_original_sha256,
            manifest.trusted_product_original_sha256,
        )
        if manifest.payload_kind == "probe" and (
            not manifest.trusted_build_id
            or not all(re.fullmatch(r"[0-9a-f]{64}", item) for item in trusted_hashes)
        ):
            raise TransactionError("probe transaction trusted build provenance is incomplete")
        if manifest.payload_kind == "regular" and any(trusted_hashes) and not all(
            re.fullmatch(r"[0-9a-f]{64}", item) for item in trusted_hashes
        ):
            raise TransactionError("regular transaction trusted hashes are malformed")
        return manifest


@dataclass(frozen=True)
class BackupTransaction:
    root: Path
    manifest_path: Path
    bundle_backup: Path
    product_backup: Path
    manifest: TransactionManifest


def _timestamp() -> str:
    return datetime.now(tz=timezone.utc).isoformat().replace("+00:00", "Z")


def manifest_bytes(manifest: TransactionManifest) -> bytes:
    return json.dumps(
        asdict(manifest),
        ensure_ascii=False,
        indent=2,
        sort_keys=False,
    ).encode("utf-8") + b"\n"


def _write_manifest(transaction: BackupTransaction) -> BackupTransaction:
    data = manifest_bytes(transaction.manifest)
    try:
        _atomic_write(transaction.manifest_path, data, mode=0o600)
    except OSError as exc:
        raise TransactionError(f"cannot write transaction manifest: {exc}") from exc
    return transaction


def create_transaction(
    paths: IDEPaths,
    installer_version: str,
    transaction_id: Optional[str] = None,
    payload_kind: str = "regular",
    trusted_build_id: str = "",
    trusted_bundle_original_sha256: str = "",
    trusted_product_original_sha256: str = "",
) -> BackupTransaction:
    if paths.profile is None or paths.product is None:
        raise TransactionError("cannot create transaction for an unsupported product")
    if payload_kind not in {"probe", "regular"}:
        raise TransactionError(f"invalid payload kind {payload_kind!r}")
    trusted_hashes = (
        trusted_bundle_original_sha256,
        trusted_product_original_sha256,
    )
    if payload_kind == "probe" and (
        not trusted_build_id
        or not all(re.fullmatch(r"[0-9a-f]{64}", item) for item in trusted_hashes)
    ):
        raise TransactionError("probe transaction trusted build provenance is incomplete")
    transaction_id = transaction_id or (
        datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        + "-"
        + secrets.token_hex(4)
    )
    transaction_id = _safe_component(transaction_id, "transaction id")
    product_commit = _safe_component(paths.product.commit, "product commit")
    root = _transaction_root(paths, product_commit, transaction_id)
    if root.exists():
        raise TransactionError(f"transaction already exists: {root}")
    _durable_mkdir(root, mode=0o700)
    bundle_backup = root / "bundle.original"
    product_backup = root / "product.original"
    _atomic_write(bundle_backup, paths.bundle_file.read_bytes(), mode=0o600)
    _atomic_write(product_backup, paths.product_file.read_bytes(), mode=0o600)
    now = _timestamp()
    manifest = TransactionManifest(
        schema_version=2,
        transaction_id=transaction_id,
        state="prepared",
        profile_id=paths.profile.profile_id,
        install_id=paths.install_id,
        app_root=str(paths.app_root),
        product_name_short=paths.product.name_short,
        product_application_name=paths.product.application_name,
        product_version=paths.product.version,
        product_commit=paths.product.commit,
        installer_version=installer_version,
        payload_kind=payload_kind,
        trusted_build_id=trusted_build_id,
        trusted_bundle_original_sha256=trusted_bundle_original_sha256,
        trusted_product_original_sha256=trusted_product_original_sha256,
        bundle_backup_name=bundle_backup.name,
        product_backup_name=product_backup.name,
        bundle_original_sha256=checksum(bundle_backup),
        product_original_sha256=checksum(product_backup),
        bundle_patched_sha256="",
        product_patched_sha256="",
        created_at=now,
        updated_at=now,
    )
    transaction = BackupTransaction(
        root=root,
        manifest_path=root / "manifest.json",
        bundle_backup=bundle_backup,
        product_backup=product_backup,
        manifest=manifest,
    )
    return _write_manifest(transaction)


def load_transaction(
    paths: IDEPaths,
    product_commit: str,
    transaction_id: str,
) -> BackupTransaction:
    product_commit = _safe_component(product_commit, "product commit")
    transaction_id = _safe_component(transaction_id, "transaction id")
    root = _transaction_root(paths, product_commit, transaction_id)
    manifest_path = root / "manifest.json"
    if manifest_path.is_symlink():
        raise TransactionError("transaction manifest must not be a symlink")
    try:
        value = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise TransactionError(f"cannot read transaction {transaction_id}: {exc}") from exc
    if not isinstance(value, dict):
        raise TransactionError("transaction manifest must be a JSON object")
    manifest = TransactionManifest.from_mapping(value)
    if manifest.transaction_id != transaction_id:
        raise TransactionError("transaction id does not match manifest path")
    if manifest.product_commit != product_commit:
        raise TransactionError("product commit does not match manifest path")
    bundle_backup = root / manifest.bundle_backup_name
    product_backup = root / manifest.product_backup_name
    if bundle_backup.is_symlink() or product_backup.is_symlink():
        raise TransactionError("transaction backups must not be symlinks")
    return BackupTransaction(
        root=root,
        manifest_path=manifest_path,
        bundle_backup=bundle_backup,
        product_backup=product_backup,
        manifest=manifest,
    )


def finalize_transaction(
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> BackupTransaction:
    manifest = replace(
        transaction.manifest,
        state="installed",
        bundle_patched_sha256=checksum(paths.bundle_file),
        product_patched_sha256=checksum(paths.product_file),
        updated_at=_timestamp(),
    )
    return _write_manifest(replace(transaction, manifest=manifest))


def _sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _read_backup_pair(transaction: BackupTransaction) -> Tuple[bytes, bytes]:
    try:
        bundle = transaction.bundle_backup.read_bytes()
        product = transaction.product_backup.read_bytes()
    except OSError as exc:
        raise TransactionError(f"cannot read transaction backup pair: {exc}") from exc
    if _sha256_bytes(bundle) != transaction.manifest.bundle_original_sha256:
        raise TransactionError("bundle backup checksum mismatch")
    if _sha256_bytes(product) != transaction.manifest.product_original_sha256:
        raise TransactionError("product backup checksum mismatch")
    return bundle, product


def _validate_transaction_identity_and_backups(
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> None:
    if paths.profile is None or paths.product is None:
        raise TransactionError("current product is unsupported")
    manifest = transaction.manifest
    hashes = (
        manifest.bundle_original_sha256,
        manifest.product_original_sha256,
        manifest.bundle_patched_sha256,
        manifest.product_patched_sha256,
    )
    if not all(re.fullmatch(r"[0-9a-f]{64}", value) for value in hashes):
        raise TransactionError("transaction checksums are incomplete or malformed")
    expected = (
        (manifest.profile_id, paths.profile.profile_id, "profile"),
        (manifest.install_id, paths.install_id, "install"),
        (
            str(Path(manifest.app_root).expanduser().resolve()),
            str(paths.app_root.expanduser().resolve()),
            "app root",
        ),
        (manifest.product_name_short, paths.product.name_short, "product name"),
        (
            manifest.product_application_name,
            paths.product.application_name,
            "application name",
        ),
        (manifest.product_version, paths.product.version, "product version"),
        (manifest.product_commit, paths.product.commit, "product commit"),
    )
    for recorded, current, label in expected:
        if recorded != current:
            raise TransactionError(
                f"transaction {label} mismatch: recorded={recorded!r}, current={current!r}"
            )
    _read_backup_pair(transaction)


def validate_transaction_for_restore(
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> None:
    _validate_transaction_identity_and_backups(transaction, paths)
    manifest = transaction.manifest
    if manifest.state != "installed":
        raise TransactionError(
            f"transaction state must be installed, got {manifest.state!r}"
        )
    try:
        live_bundle_sha = checksum(paths.bundle_file)
        live_product_sha = checksum(paths.product_file)
    except OSError as exc:
        raise TransactionError(f"cannot validate live transaction files: {exc}") from exc
    if live_bundle_sha != manifest.bundle_patched_sha256:
        raise TransactionError("live bundle checksum does not match transaction")
    if live_product_sha != manifest.product_patched_sha256:
        raise TransactionError("live product checksum does not match transaction")


def find_restoring_transaction(paths: IDEPaths) -> Optional[BackupTransaction]:
    transactions_dir = paths.backup_dir.parent.parent
    standard_layout = (
        paths.backup_dir.name == paths.install_id
        and transactions_dir.name == "transactions"
    )
    if standard_layout:
        scan_root = transactions_dir
        pattern = f"*/{paths.install_id}/*/*/manifest.json"
    else:
        scan_root = paths.backup_dir
        pattern = "*/*/manifest.json"
    if not scan_root.is_dir():
        return None
    matches = []
    for manifest_path in scan_root.glob(pattern):
        if manifest_path.is_symlink():
            raise TransactionError("transaction manifest must not be a symlink")
        try:
            value = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise TransactionError(
                f"cannot inspect transaction journal {manifest_path}: {exc}"
            ) from exc
        if not isinstance(value, dict):
            raise TransactionError(
                f"transaction journal must be a JSON object: {manifest_path}"
            )
        manifest = TransactionManifest.from_mapping(value)
        if manifest.state != "restoring":
            continue
        if standard_layout and manifest_path.parents[3].name != paths.backup_dir.parent.name:
            raise TransactionError(
                "restoring transaction exists for the same install under a different profile"
            )
        matches.append(
            load_transaction(
                paths,
                manifest.product_commit,
                manifest.transaction_id,
            )
        )
    if len(matches) > 1:
        raise TransactionError("multiple restoring transactions require manual recovery")
    return matches[0] if matches else None


def recover_restoring_transaction(
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> BackupTransaction:
    if transaction.manifest.state != "restoring":
        raise TransactionError("transaction is not in restoring state")
    _validate_transaction_identity_and_backups(transaction, paths)
    manifest = transaction.manifest
    try:
        live_bundle_sha = checksum(paths.bundle_file)
        live_product_sha = checksum(paths.product_file)
    except OSError as exc:
        raise TransactionError(f"cannot inspect restoring transaction: {exc}") from exc
    if live_bundle_sha not in {
        manifest.bundle_original_sha256,
        manifest.bundle_patched_sha256,
    }:
        raise TransactionError("restoring bundle is neither original nor patched state")
    if live_product_sha not in {
        manifest.product_original_sha256,
        manifest.product_patched_sha256,
    }:
        raise TransactionError("restoring product is neither original nor patched state")
    restore_file_pair(
        paths,
        transaction.bundle_backup,
        transaction.product_backup,
        manifest.bundle_original_sha256,
        manifest.product_original_sha256,
        live_bundle_sha,
        live_product_sha,
    )
    restored = replace(
        transaction,
        manifest=replace(manifest, state="restored", updated_at=_timestamp()),
    )
    return _write_manifest(restored)


def _conditional_write(path: Path, expected_sha256: str, target: bytes) -> None:
    try:
        current = path.read_bytes()
    except OSError as exc:
        raise TransactionError(f"cannot read live file {path}: {exc}") from exc
    if _sha256_bytes(current) != expected_sha256:
        raise TransactionError(f"live file changed before conditional write: {path}")
    try:
        _atomic_write(path, target, mode=0o644)
        written_sha = checksum(path)
    except OSError as exc:
        raise TransactionError(f"conditional write failed for {path}: {exc}") from exc
    if written_sha != _sha256_bytes(target):
        raise TransactionError(f"conditional write verification failed for {path}")


def restore_file_pair(
    paths: IDEPaths,
    bundle_source: Path,
    product_source: Path,
    expected_bundle_sha256: str = "",
    expected_product_sha256: str = "",
    expected_live_bundle_sha256: str = "",
    expected_live_product_sha256: str = "",
) -> None:
    try:
        live_bundle = paths.bundle_file.read_bytes()
        live_product = paths.product_file.read_bytes()
        source_bundle = bundle_source.read_bytes()
        source_product = product_source.read_bytes()
    except OSError as exc:
        raise TransactionError(f"cannot read restore pair: {exc}") from exc
    live_bundle_sha = _sha256_bytes(live_bundle)
    live_product_sha = _sha256_bytes(live_product)
    source_bundle_sha = _sha256_bytes(source_bundle)
    source_product_sha = _sha256_bytes(source_product)
    if expected_bundle_sha256 and source_bundle_sha != expected_bundle_sha256:
        raise TransactionError("bundle recovery source checksum mismatch")
    if expected_product_sha256 and source_product_sha != expected_product_sha256:
        raise TransactionError("product recovery source checksum mismatch")
    if expected_live_bundle_sha256 and live_bundle_sha != expected_live_bundle_sha256:
        raise TransactionError("live bundle changed before restore")
    if expected_live_product_sha256 and live_product_sha != expected_live_product_sha256:
        raise TransactionError("live product changed before restore")
    try:
        _conditional_write(paths.product_file, live_product_sha, source_product)
        _conditional_write(paths.bundle_file, live_bundle_sha, source_bundle)
    except TransactionError as exc:
        rollback_errors = []
        for label, path, before, before_sha, attempted_sha in (
            (
                "bundle",
                paths.bundle_file,
                live_bundle,
                live_bundle_sha,
                source_bundle_sha,
            ),
            (
                "product",
                paths.product_file,
                live_product,
                live_product_sha,
                source_product_sha,
            ),
        ):
            try:
                current_sha = checksum(path)
                if current_sha == before_sha:
                    continue
                if current_sha != attempted_sha:
                    raise TransactionError("unknown concurrent state")
                _conditional_write(path, attempted_sha, before)
            except (OSError, TransactionError) as rollback_exc:
                rollback_errors.append(f"{label}: {rollback_exc}")
        detail = "; ".join(rollback_errors)
        if detail:
            raise TransactionError(
                f"pair restore failed ({exc}); conditional rollback refused/failed ({detail})"
            ) from exc
        raise TransactionError(f"pair restore failed and was conditionally rolled back: {exc}") from exc
    try:
        restored_bundle_sha = checksum(paths.bundle_file)
        restored_product_sha = checksum(paths.product_file)
    except OSError as exc:
        raise TransactionError(f"cannot verify restored pair: {exc}") from exc
    if restored_bundle_sha != source_bundle_sha:
        raise TransactionError("restored bundle checksum mismatch")
    if restored_product_sha != source_product_sha:
        raise TransactionError("restored product checksum mismatch")


def restore_transaction(
    transaction: BackupTransaction,
    paths: IDEPaths,
) -> BackupTransaction:
    validate_transaction_for_restore(transaction, paths)
    try:
        live_bundle_sha = checksum(paths.bundle_file)
        live_product_sha = checksum(paths.product_file)
    except OSError as exc:
        raise TransactionError(f"cannot snapshot live files before restore: {exc}") from exc
    installed_manifest = transaction.manifest
    restoring = replace(
        transaction,
        manifest=replace(installed_manifest, state="restoring", updated_at=_timestamp()),
    )
    _write_manifest(restoring)
    restore_file_pair(
        paths,
        transaction.bundle_backup,
        transaction.product_backup,
        installed_manifest.bundle_original_sha256,
        installed_manifest.product_original_sha256,
        live_bundle_sha,
        live_product_sha,
    )
    restored = replace(
        transaction,
        manifest=replace(installed_manifest, state="restored", updated_at=_timestamp()),
    )
    return _write_manifest(restored)
