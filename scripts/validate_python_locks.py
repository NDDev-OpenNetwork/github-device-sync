#!/usr/bin/env python3
"""Validate complete, hash-locked Python dependency graphs."""

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
REQUIREMENTS = ROOT / "requirements"
PIN = re.compile(r"^([A-Za-z0-9_.-]+)==([^\s\\]+)")
HASH = re.compile(r"--hash=sha256:[0-9a-f]{64}(?:\s*\\)?$")


def normalized(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def direct_pins(path: Path, seen: set[Path] | None = None) -> dict[str, str]:
    seen = set() if seen is None else seen
    path = path.resolve()
    if path in seen:
        raise ValueError(f"recursive requirements include: {path}")
    seen.add(path)
    result: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("-r "):
            result.update(direct_pins(path.parent / line[3:].strip(), seen))
            continue
        match = PIN.fullmatch(line)
        if match is None:
            raise ValueError(f"direct dependency is not exactly pinned: {path}: {line}")
        result[normalized(match.group(1))] = match.group(2)
    seen.remove(path)
    return result


def locked(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    current: str | None = None
    hashes = 0
    for raw in path.read_text(encoding="utf-8").splitlines():
        match = PIN.match(raw)
        if match:
            if current is not None and hashes == 0:
                raise ValueError(f"locked dependency has no hash: {path}: {current}")
            current = normalized(match.group(1))
            if current in result:
                raise ValueError(f"duplicate locked dependency: {path}: {current}")
            result[current] = match.group(2)
            hashes = 0
            continue
        if HASH.search(raw):
            if current is None:
                raise ValueError(f"orphan dependency hash: {path}")
            hashes += 1
    if current is None or hashes == 0:
        raise ValueError(f"lock is empty or final dependency has no hash: {path}")
    return result


def validate(source: str, lock: str) -> dict[str, str]:
    expected = direct_pins(REQUIREMENTS / source)
    actual = locked(REQUIREMENTS / lock)
    for name, version in expected.items():
        if actual.get(name) != version:
            raise ValueError(f"lock is stale for {name}: expected {version}, got {actual.get(name)}")
    orphans = sorted(set(actual) - set(expected))
    if orphans:
        print(f"note: {len(orphans)} transitive dependencies in {lock} not in {source}: {', '.join(orphans[:5])}{'...' if len(orphans) > 5 else ''}", file=sys.stderr)
    return actual


def main() -> int:
    try:
        schema = validate("schema-validator.in", "schema-validator.txt")
        tests = validate("test.in", "test.txt")
        for name, version in schema.items():
            if tests.get(name) != version:
                raise ValueError(f"test lock diverges from schema lock for {name}")
    except (OSError, ValueError) as error:
        print(f"python lock validation failed: {error}", file=sys.stderr)
        return 2
    print(f"validated {len(schema)} schema and {len(tests)} test locked dependencies")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
