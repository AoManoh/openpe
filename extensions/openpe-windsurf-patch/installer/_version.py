"""Single source of truth for the installer package version.

Kept in a tiny module of its own so build tooling, the CLI banner, and the
unit tests can all import it without triggering the rest of the package.
"""

__version__ = "0.2.0.dev0"
