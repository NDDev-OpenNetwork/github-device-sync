#!/usr/bin/env python3
"""Test-only offline stand-in for `gh attestation verify`.

This helper validates the command shape used by the GDS consumer and returns a
statement bound to the exact local subject digest. It never validates real
provenance and MUST NOT be used outside isolated tests or local rehearsals.
"""

from __future__ import annotations

import hashlib
import json
import pathlib
import sys


VALUE_FLAGS = {
    "--bundle",
    "--custom-trusted-root",
    "--format",
    "--predicate-type",
    "--repo",
    "--signer-workflow",
    "--source-digest",
    "--source-ref",
}
BOOLEAN_FLAGS = {"--deny-self-hosted-runners"}


def fail(message: str) -> int:
    print(f"fake-gh-attestation: {message}", file=sys.stderr)
    return 2


def parse(arguments: list[str]) -> tuple[pathlib.Path, dict[str, str], set[str]]:
    if len(arguments) < 3 or arguments[:2] != ["attestation", "verify"]:
        raise ValueError("expected `attestation verify <subject>`")
    subject = pathlib.Path(arguments[2])
    values: dict[str, str] = {}
    booleans: set[str] = set()
    index = 3
    while index < len(arguments):
        flag = arguments[index]
        if flag in BOOLEAN_FLAGS:
            booleans.add(flag)
            index += 1
            continue
        if flag not in VALUE_FLAGS or index + 1 >= len(arguments):
            raise ValueError(f"unexpected or incomplete argument: {flag}")
        if flag in values:
            raise ValueError(f"duplicate argument: {flag}")
        values[flag] = arguments[index + 1]
        index += 2
    missing = sorted(VALUE_FLAGS - values.keys())
    if missing or booleans != BOOLEAN_FLAGS:
        raise ValueError(f"missing required arguments: {missing}")
    if values["--format"] != "json":
        raise ValueError("only JSON output is supported")
    return subject, values, booleans


def main() -> int:
    try:
        subject, values, _ = parse(sys.argv[1:])
        if not subject.is_file() or subject.is_symlink():
            raise ValueError("subject must be a regular non-symlink file")
        for flag in ("--bundle", "--custom-trusted-root"):
            path = pathlib.Path(values[flag])
            if not path.is_file() or path.is_symlink() or path.stat().st_size < 1:
                raise ValueError(f"{flag} must name a non-empty regular file")
        digest = hashlib.sha256(subject.read_bytes()).hexdigest()
    except (OSError, ValueError) as error:
        return fail(str(error))

    json.dump(
        [
            {
                "verificationResult": {
                    "statement": {
                        "predicateType": values["--predicate-type"],
                        "subject": [{"digest": {"sha256": digest}}],
                    }
                }
            }
        ],
        sys.stdout,
        separators=(",", ":"),
    )
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
