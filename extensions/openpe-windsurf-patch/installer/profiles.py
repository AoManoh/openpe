from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, Optional, Tuple


class ProfileError(Exception):
    pass


@dataclass(frozen=True)
class ProductIdentity:
    name_short: str
    application_name: str
    data_folder_name: str
    old_data_folder_name: str
    url_protocol: str
    old_url_protocol: str
    version: str
    commit: str

    @classmethod
    def from_mapping(cls, value: Dict[str, Any]) -> "ProductIdentity":
        def text(key: str) -> str:
            item = value.get(key, "")
            return item.strip() if isinstance(item, str) else ""

        return cls(
            name_short=text("nameShort"),
            application_name=text("applicationName"),
            data_folder_name=text("dataFolderName"),
            old_data_folder_name=text("oldDataFolderName"),
            url_protocol=text("urlProtocol"),
            old_url_protocol=text("oldUrlProtocol"),
            version=text("version"),
            commit=text("commit"),
        )


@dataclass(frozen=True)
class SupportedBuild:
    system: str
    version: str
    commit: str
    bundle_sha256: str
    product_sha256: str

    def matches(self, system: str, identity: ProductIdentity) -> bool:
        return (
            self.system == system
            and self.version == identity.version
            and self.commit == identity.commit
        )


@dataclass(frozen=True)
class HostProfile:
    profile_id: str
    cli_name: str
    display_name: str
    name_short: str
    application_name: str
    data_folder_name: str
    url_protocol: str
    supported_builds: Tuple[SupportedBuild, ...]
    client: str
    mode: str
    cors_origins: Tuple[str, ...]
    history_source: str
    process_names: Tuple[str, ...]
    updater_names: Tuple[str, ...]
    mutable_systems: Tuple[str, ...]
    runtime_verified: bool

    def matches(self, identity: ProductIdentity) -> bool:
        return (
            identity.name_short.casefold() == self.name_short.casefold()
            and identity.application_name.casefold() == self.application_name.casefold()
            and identity.data_folder_name.casefold() == self.data_folder_name.casefold()
            and identity.url_protocol.casefold() == self.url_protocol.casefold()
        )

    def supported_build(
        self,
        system: str,
        identity: ProductIdentity,
    ) -> Optional[SupportedBuild]:
        return next(
            (
                build
                for build in self.supported_builds
                if build.matches(system, identity)
            ),
            None,
        )

    def allows_mutation(self, system: str, identity: ProductIdentity) -> bool:
        return (
            self.runtime_verified
            and system in self.mutable_systems
            and self.supported_build(system, identity) is not None
        )


DEVIN_PROFILE = HostProfile(
    profile_id="devin-desktop",
    cli_name="devin",
    display_name="Devin Desktop",
    name_short="Devin",
    application_name="devin-desktop",
    data_folder_name=".devin",
    url_protocol="devin",
    supported_builds=(
        SupportedBuild(
            system="Windows",
            version="1.110.1",
            commit="0d4bf12ed4a7597cb8ae9016fe8474468aad98a2",
            bundle_sha256="d42cdb9d55a41ed61f244977d5d90498874f9aa93ddb525f1eae5e4aa44a564f",
            product_sha256="638c97a4e0f93a9633cda0585bf399bec8fc1e27248907e10a85caf75432a0ad",
        ),
    ),
    client="devin",
    mode="agent",
    cors_origins=("vscode-file://vscode-app",),
    history_source="none",
    process_names=("Devin.exe", "devin"),
    updater_names=(
        "DevinUpdater.exe",
        "devin-updater",
        "inno_updater.exe",
        "WindsurfGate.exe",
    ),
    mutable_systems=(),
    runtime_verified=False,
)

WINDSURF_PROFILE = HostProfile(
    profile_id="windsurf-legacy",
    cli_name="windsurf",
    display_name="Windsurf Cascade",
    name_short="Windsurf",
    application_name="windsurf",
    data_folder_name=".windsurf",
    url_protocol="windsurf",
    supported_builds=(
        SupportedBuild(
            system="Windows",
            version="1.110.1",
            commit="8636ab52872ef59a7227b8f7f386fd30d94b2249",
            bundle_sha256="d48ca0c19b6495f9519dbd96b0673840e7b5e54bca3ea9dddc4b0d52a044df57",
            product_sha256="d1168dd513a17f1e6329d237896afca88d6f1b58592ace55bfff73705718fa08",
        ),
    ),
    client="windsurf",
    mode="cascade",
    cors_origins=("null", "app://windsurf"),
    history_source="legacy_trajectory",
    process_names=("Windsurf.exe", "windsurf"),
    updater_names=("WindsurfUpdater.exe", "windsurf-updater"),
    mutable_systems=(),
    runtime_verified=False,
)

PROFILES = (DEVIN_PROFILE, WINDSURF_PROFILE)


def read_product_identity(path: Path) -> ProductIdentity:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ProfileError(f"cannot read product identity from {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ProfileError(f"product identity at {path} must be a JSON object")
    return ProductIdentity.from_mapping(value)


def detect_profile(identity: ProductIdentity) -> Optional[HostProfile]:
    matches = tuple(profile for profile in PROFILES if profile.matches(identity))
    if len(matches) > 1:
        raise ProfileError("product identity matches multiple host profiles")
    return matches[0] if matches else None


def require_profile(identity: ProductIdentity, requested: str = "auto") -> HostProfile:
    profile = detect_profile(identity)
    if profile is None:
        raise ProfileError(
            "unsupported product identity: "
            f"nameShort={identity.name_short!r}, applicationName={identity.application_name!r}"
        )
    if not identity.commit:
        raise ProfileError("product identity is missing commit")
    if not identity.version:
        raise ProfileError("product identity is missing version")
    requested = requested.strip().casefold() or "auto"
    if requested != "auto" and profile.cli_name != requested:
        raise ProfileError(
            f"requested host {requested!r} does not match detected {profile.cli_name!r}"
        )
    return profile
