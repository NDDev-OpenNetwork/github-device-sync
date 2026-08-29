#!/usr/bin/env python3
"""Generate the harness identity lists in documentation from the registry.

`docs/contracts/harness-adapters-v1.md` used to claim that a script named
`validate_harness_docs.py` failed whenever the doc list, the registry and
`core/harness.CanonicalIDs` disagreed, "so the three cannot drift apart
silently as they did before". That script did not exist. The documentation
described a guard nobody had built, and the exact drift it promised to prevent
happened: the doc listed seventeen identities while the code had seven.

So this generates rather than validates. The registry is the one place a
harness identity is written by hand; every list of identities in prose is
derived from it, and `--check` fails when a derived block is stale. A document
cannot disagree with the code if it is not written by hand.

Usage:
    generate_harness_docs.py            rewrite derived blocks in place
    generate_harness_docs.py --check    exit 1 if any block is stale
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = ROOT / "harnesses" / "capability-registry.yaml"
CANONICAL_GO = ROOT / "core" / "harness" / "registry.go"

BEGIN = "<!-- generated:harness-ids -->"
END = "<!-- /generated:harness-ids -->"


def registry_ids() -> list[str]:
    """Read ids in file order. Deliberately not a YAML parse: the registry is
    the source of truth for order as well as membership, and a parser would
    silently reorder or tolerate a duplicate key."""
    text = REGISTRY.read_text(encoding="utf-8")
    ids = re.findall(r'^  - id: "([^"]+)"$', text, re.M)
    if not ids:
        raise SystemExit(f"no harness ids found in {REGISTRY}")
    if len(ids) != len(set(ids)):
        raise SystemExit(f"duplicate harness id in {REGISTRY}: {ids}")
    return ids


def canonical_go_ids() -> list[str]:
    text = CANONICAL_GO.read_text(encoding="utf-8")
    block = re.search(r"var CanonicalIDs = \[\]string\{(.*?)\n\}", text, re.S)
    if block is None:
        raise SystemExit(f"CanonicalIDs not found in {CANONICAL_GO}")
    return re.findall(r'"([^"]+)"', block.group(1))


def render(ids: list[str], style: str) -> str:
    if style == "fence":
        return "```text\n" + "".join(f"{i}\n" for i in ids) + "```"
    if style == "bullets":
        body = "".join(f"- `{i}`;\n" for i in ids[:-1])
        return body + f"- `{ids[-1]}`."
    raise SystemExit(f"unknown style {style!r}")


# path -> style of the generated block it carries
TARGETS = {
    "docs/contracts/harness-adapters-v1.md": "fence",
    "harnesses/README.md": "bullets",
}


def apply(ids: list[str], check: bool) -> int:
    stale: list[str] = []
    for relative, style in TARGETS.items():
        path = ROOT / relative
        text = path.read_text(encoding="utf-8")
        pattern = re.compile(
            re.escape(BEGIN) + r"\n.*?\n" + re.escape(END), re.S
        )
        if not pattern.search(text):
            raise SystemExit(
                f"{relative} has no generated block; add {BEGIN} / {END} markers"
            )
        wanted = f"{BEGIN}\n{render(ids, style)}\n{END}"
        updated = pattern.sub(lambda _: wanted, text)
        if updated == text:
            continue
        if check:
            stale.append(relative)
        else:
            path.write_text(updated, encoding="utf-8")
            print(f"regenerated {relative}")
    if stale:
        print(
            "stale generated harness blocks: " + ", ".join(stale) +
            "\nrun scripts/generate_harness_docs.py",
            file=sys.stderr,
        )
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    ids = registry_ids()
    go_ids = canonical_go_ids()
    if ids != go_ids:
        print(
            "registry and core/harness.CanonicalIDs disagree\n"
            f"  registry:     {ids}\n"
            f"  CanonicalIDs: {go_ids}",
            file=sys.stderr,
        )
        return 1
    return apply(ids, args.check)


if __name__ == "__main__":
    raise SystemExit(main())
