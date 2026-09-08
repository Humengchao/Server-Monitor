"""Extracts the macOS L10n table into JSON, to seed the C# dictionary.

One-off tool, kept so the extraction is reproducible when the Swift table
gains keys: `python extract-l10n.py <path-to-L10n.swift> <out.json>`.
The C# side is hand-maintained after that — a test asserts the two key sets
agree, which is what actually keeps them from drifting.
"""

import io
import json
import re
import sys

QUOTED = '"((?:[^"' + chr(92) + chr(92) + ']|' + chr(92) + chr(92) + '.)*)"'
ENTRY = re.compile(r"^\s*" + QUOTED + r":\s*\(" + QUOTED + r",\s*" + QUOTED + r"\),\s*$", re.M)


def main() -> int:
    source = sys.argv[1] if len(sys.argv) > 1 else "L10n.swift"
    destination = sys.argv[2] if len(sys.argv) > 2 else "l10n.json"
    text = io.open(source, encoding="utf-8").read()
    rows = ENTRY.findall(text)
    keys = [row[0] for row in rows]
    duplicates = sorted({k for k in keys if keys.count(k) > 1})
    print("entries:", len(rows))
    print("duplicates:", duplicates)
    io.open(destination, "w", encoding="utf-8").write(
        json.dumps(rows, ensure_ascii=False, indent=0)
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
