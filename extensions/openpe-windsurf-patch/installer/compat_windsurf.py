from __future__ import annotations

import sys
from typing import Optional, Sequence

from .__main__ import main as canonical_main


def main(argv: Optional[Sequence[str]] = None) -> int:
    values = list(argv if argv is not None else sys.argv[1:])
    return canonical_main(
        values,
        forced_host="windsurf",
        prog="openpe-windsurf-patch",
    )


if __name__ == "__main__":
    raise SystemExit(main())
