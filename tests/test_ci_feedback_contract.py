"""Small wiring checks; publisher behavior belongs to its owning module."""
import pathlib
import json
import re
import yaml
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
CALLER = ROOT / ".github/workflows/ci-feedback-events.yml"


class FeedbackContractTests(unittest.TestCase):
    def test_exact_completed_attempt_is_forwarded(self):
        text = CALLER.read_text()
        selected = json.loads(re.search(r"workflows: (\[[^\n]+\])", text).group(1))
        expected = []
        for path in sorted((ROOT / ".github/workflows").glob("*.yml")):
            if path == CALLER:
                continue
            document = yaml.safe_load(path.read_text())
            events = document.get("on", document.get(True))
            if isinstance(events, str):
                events = [events]
            if set(events) - {"workflow_call"}:
                expected.append(document.get("name", path.name))
        self.assertEqual(selected, expected)
        self.assertNotIn("CI feedback", selected)
        self.assertIn("types: [completed]", text)
        self.assertIn("github.event.workflow_run.id", text)
        self.assertIn("github.event.workflow_run.run_attempt", text)
        self.assertNotIn("github.run_id", json.dumps(yaml.safe_load(text)["jobs"]["feedback"]["with"]))

    def test_reusable_reference_is_immutable(self):
        text = CALLER.read_text()
        self.assertRegex(text, r"uses: NDDev-OpenNetwork/github-actions/\.github/workflows/ci-feedback\.yml@[0-9a-f]{40}(?: +# commit:[0-9a-f]{40})?\n")
        self.assertNotIn("@main", text)

    def test_no_project_execution_or_secret_inheritance(self):
        text = CALLER.read_text()
        document = yaml.safe_load(text)
        self.assertEqual(document["permissions"], {})
        self.assertEqual(document["jobs"]["feedback"]["permissions"],
                         {"actions": "read", "issues": "write"})
        self.assertNotIn("steps", document["jobs"]["feedback"])
        self.assertNotIn("checkout", text)
        self.assertNotIn("secrets:", text)
        self.assertNotIn("runs-on:", text)
        self.assertNotIn("run:", text.replace("workflow_run:", ""))

    def test_cancelled_attempt_reaches_publisher_for_failed_job_inspection(self):
        text = CALLER.read_text()
        condition = next(line for line in text.splitlines() if line.strip().startswith("if:"))
        self.assertIn('"failure"', condition)
        self.assertIn('"cancelled"', condition)
        self.assertNotIn("continue-on-error", text)


if __name__ == "__main__":
    unittest.main()
