"""The version ledger must cover the exact declared submodule set.

The validator used to carry its own two-name tuple of modules. That made it a
third inventory of a fact `.gds/repository.yaml` and `.gitmodules` already own,
and it went wrong the way a hand-maintained copy always goes wrong: a third
module was pinned and simply never entered the check. PR #174 is the observable
proof -- all three gitlinks moved, and CI reported drift for exactly the two the
tuple happened to name.

These tests pin the derivation itself, not today's module names. Every case
builds a self-contained fixture tree, so adding or retiring a real module
changes nothing here.
"""

from __future__ import annotations

import contextlib
import io
import json
import tempfile
import unittest
import unittest.mock
from pathlib import Path

from scripts import validate_version_ledger as ledger


MODULES = ("alpha", "beta")
PINS = {
    "alpha": "1" * 40,
    "beta": "2" * 40,
}


def anchor_document(names: tuple[str, ...], extra: str = "") -> str:
    relationships = "\n".join(
        f"""  - type: "git-submodule-consumer"
    target: "repo_{name.upper()}"
    gitmodules_name: "modules/{name}\""""
        for name in names
    )
    return f"schema_version: 1\n\nrelationships:\n{relationships}\n{extra}"


def gitmodules_document(names: tuple[str, ...]) -> str:
    return "\n".join(
        f'[submodule "modules/{name}"]\n\tpath = modules/{name}\n'
        f"\turl = https://example.invalid/{name}.git"
        for name in names
    )


def ledger_document(rows: tuple[tuple[str, str], ...]) -> str:
    body = "\n".join(f"| {name} | `x` | gitlink `{oid}` | decision |" for name, oid in rows)
    return (
        "# Estate version ledger\n\n"
        "## Submodule gitlinks\n\n"
        "Prose that names alpha and beta without being a row.\n\n"
        "| Component | Selected | Owner | Decision |\n"
        "|---|---|---|---|\n"
        f"{body}\n\n"
        "## Browser stack\n\n"
        "| Component | Selected |\n|---|---|\n| chrome | `1` |\n"
    )


def default_rows() -> tuple[tuple[str, str], ...]:
    return tuple((name, PINS[name]) for name in MODULES)


def run(
    *,
    anchor: str | None = None,
    gitmodules: str | None = None,
    ledger_text: str | None = None,
    pins: dict[str, str] | None = None,
) -> tuple[int, str]:
    """Run the validator against a self-contained fixture tree."""
    with tempfile.TemporaryDirectory() as raw:
        root = Path(raw)
        (root / ".gds").mkdir()
        (root / ".gds" / "repository.yaml").write_text(
            anchor if anchor is not None else anchor_document(MODULES),
            encoding="utf-8",
        )
        (root / ".gitmodules").write_text(
            gitmodules if gitmodules is not None else gitmodules_document(MODULES),
            encoding="utf-8",
        )
        (root / "docs" / "runbooks").mkdir(parents=True)
        (root / "docs" / "version-ledger.md").write_text(
            ledger_text if ledger_text is not None else ledger_document(default_rows()),
            encoding="utf-8",
        )
        contract = root / "contract.json"
        contract.write_text(json.dumps({"harnesses": {}}), encoding="utf-8")
        # release_commits only needs the directory to exist to consider the
        # dimension readable; an empty tag list is a legitimate answer.
        for name in MODULES:
            (root / "modules" / name / ".git").mkdir(parents=True)

        stderr = io.StringIO()
        patches = unittest.mock.patch.multiple(
            ledger,
            ROOT=root,
            LEDGER=root / "docs" / "version-ledger.md",
            ANCHOR=root / ".gds" / "repository.yaml",
            GITMODULES=root / ".gitmodules",
            CONTRACT=contract,
        )
        pinned = unittest.mock.patch.object(
            ledger, "gitlinks", lambda: dict(PINS if pins is None else pins)
        )
        with patches, pinned, contextlib.redirect_stderr(stderr):
            code = ledger.main()
        return code, stderr.getvalue()


class CanonicalSetTests(unittest.TestCase):
    def test_a_consistent_fixture_passes(self) -> None:
        code, output = run()
        self.assertEqual(0, code, output)

    def test_prose_mentioning_a_module_is_not_mistaken_for_its_row(self) -> None:
        """Row lookup is table-scoped, so mentions elsewhere cannot stand in.

        The previous lookup scanned the whole document for a line starting with
        `|` that contained the name, which made correctness depend on no other
        table ever mentioning a module.
        """
        rows = ((MODULES[0], PINS[MODULES[0]]),)
        code, output = run(ledger_text=ledger_document(rows))
        self.assertEqual(1, code)
        self.assertIn(f"{MODULES[1]}: no submodule row in the ledger", output)


class DriftTests(unittest.TestCase):
    def test_a_single_module_drifting_fails_for_that_module(self) -> None:
        """Each declared module is covered, not just the ones a tuple listed.

        This is the exact PR #174 shape: one gitlink moves and its row does not.
        Run once per module so the assertion cannot pass because the set happens
        to contain the one being checked.
        """
        for drifted in MODULES:
            with self.subTest(module=drifted):
                pins = dict(PINS) | {drifted: "9" * 40}
                code, output = run(pins=pins)
                self.assertEqual(1, code)
                self.assertIn(
                    f"{drifted}: ledger row does not name the gitlink {'9' * 40}",
                    output,
                )
                for other in MODULES:
                    if other != drifted:
                        self.assertNotIn(f"{other}: ledger row", output)

    def test_a_missing_row_fails(self) -> None:
        code, output = run(ledger_text=ledger_document(default_rows()[:1]))
        self.assertEqual(1, code)
        self.assertIn(f"{MODULES[1]}: no submodule row in the ledger", output)

    def test_a_row_for_an_undeclared_module_fails(self) -> None:
        rows = default_rows() + (("gamma", "3" * 40),)
        code, output = run(ledger_text=ledger_document(rows))
        self.assertEqual(1, code)
        self.assertIn("gamma: ledger row for a module that is not declared", output)

    def test_a_duplicate_row_fails(self) -> None:
        rows = default_rows() + ((MODULES[0], PINS[MODULES[0]]),)
        code, output = run(ledger_text=ledger_document(rows))
        self.assertEqual(1, code)
        self.assertIn(f"{MODULES[0]}: 2 submodule rows in the ledger", output)

    def test_a_gitlink_without_a_typed_relationship_fails(self) -> None:
        pins = dict(PINS) | {"gamma": "3" * 40}
        code, output = run(pins=pins)
        self.assertEqual(1, code)
        self.assertIn(
            "gamma: gitlink under modules/ without a typed relationship", output
        )

    def test_a_missing_submodule_section_fails(self) -> None:
        code, output = run(ledger_text="# Estate version ledger\n\n## Browser stack\n")
        self.assertEqual(1, code)
        self.assertIn("has no '## Submodule gitlinks' section", output)


class SourceDisagreementTests(unittest.TestCase):
    """Neither source is authoritative alone; both directions are reported."""

    def test_a_typed_relationship_without_a_gitmodules_entry_fails(self) -> None:
        code, output = run(gitmodules=gitmodules_document(MODULES[:1]))
        self.assertEqual(1, code)
        self.assertIn(
            f"modules/{MODULES[1]}: typed relationship without a .gitmodules entry",
            output,
        )

    def test_a_gitmodules_entry_without_a_typed_relationship_fails(self) -> None:
        code, output = run(anchor=anchor_document(MODULES[:1]))
        self.assertEqual(1, code)
        self.assertIn(
            f"modules/{MODULES[1]}: .gitmodules entry without a typed relationship",
            output,
        )

    def test_a_path_that_disagrees_with_its_entry_name_fails(self) -> None:
        aliased = gitmodules_document(MODULES).replace(
            f"path = modules/{MODULES[0]}", f"path = elsewhere/{MODULES[0]}"
        )
        code, output = run(gitmodules=aliased)
        self.assertEqual(1, code)
        self.assertIn("does not match its entry name", output)

    def test_one_target_consumed_under_two_names_fails(self) -> None:
        collided = anchor_document(MODULES).replace(
            f'target: "repo_{MODULES[1].upper()}"',
            f'target: "repo_{MODULES[0].upper()}"',
        )
        code, output = run(anchor=collided)
        self.assertEqual(1, code)
        self.assertIn("consumed as both", output)

    def test_a_relationship_without_a_gitmodules_name_fails(self) -> None:
        nameless = (
            'schema_version: 1\n\nrelationships:\n'
            '  - type: "git-submodule-consumer"\n'
            '    target: "repo_ALPHA"\n'
        )
        code, output = run(anchor=nameless)
        self.assertEqual(1, code)
        self.assertIn("declares no gitmodules_name", output)


class RealRepositoryTests(unittest.TestCase):
    def test_every_declared_module_is_derived_from_both_sources(self) -> None:
        """The real tree must agree with itself before any row is compared."""
        modules, failures = ledger.declared_submodules()
        self.assertEqual([], failures)
        self.assertTrue(modules, "no declared submodules were derived")
        for name, entry in modules.items():
            self.assertTrue(entry.endswith(name))


if __name__ == "__main__":
    unittest.main()
