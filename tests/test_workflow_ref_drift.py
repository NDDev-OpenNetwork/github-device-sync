from __future__ import annotations

import contextlib
import io
import json
import unittest
import unittest.mock
from pathlib import Path

import yaml

from scripts import refresh_required_check_facts as facts
from scripts import report_workflow_ref_drift as reporter


ROOT = Path(__file__).resolve().parents[1]
DEPENDABOT = ROOT / ".github" / "dependabot.yml"


class WithheldPinCoverageTests(unittest.TestCase):
    """Dependabot no longer watches the ci-workflows family, so this must.

    Everything here runs offline. Comparing against upstream needs a token and a
    network, but the parts that can rot silently -- which pins are withheld, and
    whether anything still watches them -- do not.
    """

    def test_dependabot_withholds_the_family_both_tools_cover(self) -> None:
        """The ignore rule and these tools must agree on one dependency family.

        If they drift apart the pins are either watched twice or not at all, and
        the second case is indistinguishable from a healthy repository.
        """
        document = yaml.safe_load(DEPENDABOT.read_text(encoding="utf-8"))
        actions = [
            update
            for update in document["updates"]
            if update["package-ecosystem"] == "github-actions"
        ]
        self.assertEqual(1, len(actions))
        ignored = {entry["dependency-name"] for entry in actions[0].get("ignore", [])}
        self.assertIn(f"{reporter.UPSTREAM}/*", ignored)
        self.assertEqual(reporter.UPSTREAM, facts.UPSTREAM)

    def test_every_withheld_pin_is_declared_somewhere_the_reporter_reads(self) -> None:
        """A pin the reporter cannot see is a pin nothing watches."""
        declared = reporter.declared_pins()
        self.assertNotEqual({}, declared)
        for path, entry in declared.items():
            with self.subTest(path=path):
                self.assertNotEqual([], entry["declared_in"])

    def test_the_anchor_pin_is_reported_against_its_canonical_declaration(self) -> None:
        """`go-ci.yml` reaches CI through a projection; the anchor owns it.

        Reporting it only from the projection would let the reporter agree with a
        derived file while the canonical one said something else.
        """
        anchor = yaml.safe_load(
            (ROOT / ".gds" / "repository.yaml").read_text(encoding="utf-8")
        )
        pinned = anchor["ci"]["workflow_ref"]
        entry = reporter.declared_pins()[".github/workflows/go-ci.yml"]
        self.assertIn(".gds/repository.yaml", entry["declared_in"])
        self.assertIn(pinned.rsplit("@", 1)[1], entry["refs"])

    def test_the_facts_cache_covers_exactly_the_required_check_callers(self) -> None:
        """Scope must come from the same policy the generator walks."""
        self.assertEqual(0, facts.main(["--check"]))
        cached = {
            (item["path"], item["commit"])
            for item in json.loads(facts.FACTS.read_text(encoding="utf-8"))[
                "reusable_workflows"
            ]
        }
        self.assertEqual(set(facts.declared_pins()), cached)

    def test_a_pin_declared_at_two_refs_is_reported_as_inconsistent(self) -> None:
        """Two callers disagreeing about a ref is drift, not a ref to compare."""
        entry = {
            "refs": {"a" * 40: ["one.yml"], "b" * 40: ["two.yml"]},
            "declared_in": ["one.yml", "two.yml"],
        }
        result = reporter.classify(".github/workflows/x.yml", entry, "c" * 40)
        self.assertEqual("inconsistent", result["state"])


class DriftClassificationTests(unittest.TestCase):
    def test_only_a_changed_upstream_workflow_demands_action(self) -> None:
        """Being behind is not by itself a failure; a changed file is.

        Upstream releases far more often than any single workflow changes.
        Failing on every upstream commit would make this reporter noise, and
        noise gets muted -- the state the Dependabot ignore was meant to avoid.
        """
        head = "b" * 40
        pinned = "a" * 40
        for label, ref, digests, expected_state in (
            ("current", head, {}, "current"),
            ("behind but unchanged", pinned, {pinned: "x", head: "x"}, "behind"),
            ("behind and changed", pinned, {pinned: "x", head: "y"}, "stale"),
        ):
            with self.subTest(label=label):
                entry = {"refs": {ref: ["w.yml"]}, "declared_in": ["w.yml"]}
                with unittest.mock.patch.object(
                    reporter, "content_digest", lambda _p, r: digests[r]
                ):
                    result = reporter.classify(".github/workflows/w.yml", entry, head)
                self.assertEqual(expected_state, result["state"])

    def test_an_unreadable_upstream_is_reported_separately_from_drift(self) -> None:
        """A failed read must not be indistinguishable from a clean result."""

        def refuse(_endpoint: str) -> object:
            raise RuntimeError("no token")

        with unittest.mock.patch.object(reporter, "api", refuse):
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(2, reporter.main([]))


class FactsCacheTests(unittest.TestCase):
    def test_cached_jobs_carry_only_what_a_check_context_depends_on(self) -> None:
        """Extra upstream job keys must not churn this file.

        `fail-fast`, `runs-on` and the rest cannot change a check name, so
        caching them would rewrite the cache on upstream edits that change
        nothing an operator needs to review.
        """
        raw = (
            b"jobs:\n"
            b"  codeql:\n"
            b"    name: CodeQL (${{ matrix.language }})\n"
            b"    runs-on: ubuntu-latest\n"
            b"    strategy:\n"
            b"      fail-fast: false\n"
            b"      matrix:\n"
            b"        language: ${{ fromJSON(inputs.languages) }}\n"
        )
        self.assertEqual(
            {
                "codeql": {
                    "name": "CodeQL (${{ matrix.language }})",
                    "strategy": {"matrix": {"language": "${{ fromJSON(inputs.languages) }}"}},
                }
            },
            facts.job_facts(raw),
        )

    def test_a_workflow_without_jobs_fails_closed(self) -> None:
        for unusable in (b"name: x\n", b"jobs: []\n", b"[]\n"):
            with self.subTest(unusable=unusable):
                with self.assertRaises(ValueError):
                    facts.job_facts(unusable)

    def test_rendering_preserves_the_tracked_compact_shape(self) -> None:
        """A refresh must show the line that changed, not rewrite the file."""
        rendered = facts.render(json.loads(facts.FACTS.read_text(encoding="utf-8")))
        self.assertEqual(facts.FACTS.read_text(encoding="utf-8"), rendered)


if __name__ == "__main__":
    unittest.main()
