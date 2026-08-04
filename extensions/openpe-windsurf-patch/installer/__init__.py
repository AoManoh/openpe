"""Profile-gated experimental IDE bundle patch installer package.

The canonical console entry is ``openpe-ide-patch``. The legacy
``openpe-windsurf-patch`` entry remains a host-bound compatibility shim.
All bundle mutation profiles are disabled until runtime and crash-recovery
verification is complete. See the project README for the risk boundary.
"""

from ._version import __version__

__all__ = ["__version__"]
