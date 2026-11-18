#!/usr/bin/env python3
"""Fail when README's install pin and docs/log.md's newest release heading disagree.

WHY THIS COMPARISON AND NOT A TAG. The README pin had drifted to 0.20.0 while the newest release was
0.83.1 -- sixty-three minors, and BELOW the 0.26.1 boundary where docs/log.md records that the verbose
dump leaked credentials. So the README recommended a version our own log says not to run.

A check against the published tag cannot run on a pull_request, because the PR precedes its own tag. But
the two values this script compares are written in the SAME commit: the pin now moves in the
`docs(log): cut the X.Y.Z section` PR, which is already the last one merged before every tag. So the gate
asks only that they agree, and it can block at review time.

WHAT IT DELIBERATELY DOES NOT DO: verify either value against the registry. A cut that sets both to the
wrong version passes. That is one typo in one PR, against the year of silence this replaces -- and the
defect was never a wrong number, it was that nothing compared two documents.

EXIT 0 agree. 1 disagree. 2 VOID -- a value could not be read at all, which must NOT pass: "no pin found"
reading as "the pin is correct" is the exact failure this repo has hit eight times in one day.
"""
import re
import sys
from pathlib import Path

# `  --version 0.84.0` and `  --version 0.84.0 --namespace ...`. The pin sits on a CONTINUATION line
# after a backslash, which is why a line-oriented search for `oasgen-provider --version` missed it
# entirely and reported that this repo pinned nothing.
PIN = re.compile(r"^\s*--version\s+(\d+\.\d+\.\d+)\b", re.M)

# `## 2026-11-18 — 0.83.1`, with an EM DASH. Matched loosely on the separator so a hyphen does not VOID
# the check, but the version shape is exact.
HEADING = re.compile(r"^##\s+\d{4}-\d{2}-\d{2}\s*\S\s*(\d+\.\d+\.\d+)\s*$", re.M)


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    readme, log = root / "README.md", root / "docs" / "log.md"

    for p in (readme, log):
        if not p.is_file():
            print(f"VOID: {p} is missing, so nothing was compared")
            return 2

    pins = PIN.findall(readme.read_text(encoding="utf-8"))
    if not pins:
        print("VOID: no `--version X.Y.Z` found in README.md. This is NOT a pass: the pattern may have "
              "stopped matching, and an absent pin must never read as a correct one.")
        return 2
    if len(set(pins)) != 1:
        print(f"FAIL: README pins disagree with each other: {sorted(set(pins))}")
        return 1

    headings = HEADING.findall(log.read_text(encoding="utf-8"))
    if not headings:
        print("VOID: no `## <date> — <version>` heading found in docs/log.md. NOT a pass.")
        return 2

    pin, newest = pins[0], headings[0]
    if pin != newest:
        print(f"FAIL: README pins {pin} while docs/log.md's newest release section is {newest}.\n\n"
              f"The pin moves in the `docs(log): cut the X.Y.Z section` PR, which is the last one merged "
              f"before every tag. If you are cutting {newest}, bump the README in the same commit. If you "
              f"are adding a log section for an older release, this check is telling you the heading "
              f"order is wrong -- the newest must come first.")
        return 1

    print(f"OK: README pins {pin}, matching docs/log.md's newest release section. "
          f"{len(pins)} pin occurrence(s) checked, all agreeing.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
