"""Small wiring checks; publisher behavior belongs to its owning module."""
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
CALLER = ROOT / ".github/workflows/ci-feedback-events.yml"


class FeedbackContractTests(unittest.TestCase):
    def test_exact_completed_attempt_is_forwarded(self):
        text = CALLER.read_text()
        self.assertIn("workflows: [gds-ci]", text)
        self.assertIn("types: [completed]", text)
        self.assertIn("github.event.workflow_run.id", text)
        self.assertIn("github.event.workflow_run.run_attempt", text)
        self.assertNotIn("github.run_id", text)

    def test_reusable_reference_is_immutable(self):
        text = CALLER.read_text()
        self.assertRegex(text, r"uses: NDDev-OpenNetwork/github-actions/\.github/workflows/ci-feedback\.yml@[0-9a-f]{40}\n")
        self.assertNotIn("@main", text)

    def test_no_project_execution_or_secret_inheritance(self):
        text = CALLER.read_text()
        self.assertIn("  actions: read\n  issues: write", text)
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
