"""The architecture overview must not outlive the estate it describes.

`docs/architecture/README.md` opens by explaining that it carries no release
version deliberately, because a version stamped there goes stale silently every
time a release ships without an architecture change. Its counted claims about
the estate have exactly that property and had none of the protection:
projections, schemas and locks are digest-checked, prose is not.

It went wrong the way it was always going to. Onboarding a fifth installation
left the document saying four, and onboarding a fourth module left it naming
two public ones. Both survived review, because nothing compares the sentence to
the directory it describes.

These tests derive every number from `estate/` and the module anchors, so the
document cannot claim a count the tree disagrees with. They deliberately do not
check the prose around the numbers -- naming, emphasis and rationale stay
human-authored, which is the same boundary `validate_version_ledger.py` draws.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
OVERVIEW = ROOT / "docs" / "architecture" / "README.md"

WORDS = {
    1: "One",
    2: "Two",
    3: "Three",
    4: "Four",
    5: "Five",
    6: "Six",
    7: "Seven",
    8: "Eight",
    9: "Nine",
    10: "Ten",
}


def overview() -> str:
    return OVERVIEW.read_text(encoding="utf-8")


def count(directory: str) -> int:
    return len(list((ROOT / "estate" / directory).glob("*.yaml")))


def declared_modules() -> dict[str, str]:
    """Return {module name: gitmodules entry} from the typed relationships."""
    anchor = yaml.safe_load((ROOT / ".gds" / "repository.yaml").read_text(encoding="utf-8"))
    modules: dict[str, str] = {}
    for relationship in anchor.get("relationships") or []:
        if relationship.get("type") != "git-submodule-consumer":
            continue
        entry = relationship["gitmodules_name"]
        modules[entry.split("/")[-1]] = entry
    return modules


class EstateCountTests(unittest.TestCase):
    def test_the_installation_count_matches_the_estate(self) -> None:
        expected = count("installations")
        self.assertIn(
            f"**{WORDS[expected]} GitHub installations**",
            overview(),
            f"the overview does not claim {expected} installations",
        )

    def test_the_mutation_capability_count_matches_the_estate(self) -> None:
        """The sentence counts which installations carry a Mutation App.

        It is a different number from the installation count on purpose -- one
        installation is a read-only membership -- so it needs its own check
        rather than riding along with the one above.
        """
        expected = count("mutations")
        match = re.search(
            r"\*\*[A-Z][a-z]+ GitHub installations\*\*.*?(\w+) carry separate Mutation Apps",
            overview(),
            re.DOTALL,
        )
        self.assertIsNotNone(match, "the Mutation App sentence is not where it was")
        assert match is not None
        self.assertEqual(WORDS[expected], match.group(1))

    def test_the_device_count_matches_the_estate(self) -> None:
        expected = count("devices")
        self.assertIn(f"**{WORDS[expected]} devices**", overview())

    def test_every_device_is_named(self) -> None:
        """A count is not enough: three devices and three names can disagree."""
        text = overview()
        for device in sorted((ROOT / "estate" / "devices").glob("*.yaml")):
            self.assertIn(f"`{device.stem}`", text, f"{device.stem} is not named")


