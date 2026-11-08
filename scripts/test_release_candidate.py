import re
import unittest
from pathlib import Path
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[1]
RELEASE_NOTES = ROOT / "docs" / "releases" / "v0.1.0.md"
LINK_PATTERN = re.compile(r"(?<!!)\[[^]]+\]\(([^)]+)\)")


class ReleaseCandidateTest(unittest.TestCase):
    def test_build_toolchain_has_one_exact_version(self):
        version = (ROOT / ".go-version").read_text(encoding="utf-8").strip()
        self.assertRegex(version, r"^\d+\.\d+\.\d+$")

        dockerfile = (ROOT / "Dockerfile").read_text(encoding="utf-8")
        docker_versions = re.findall(r"^FROM golang:([^\s]+)", dockerfile, re.MULTILINE)
        self.assertEqual(docker_versions, [version])

        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        setup_go_steps = workflow.count("actions/setup-go@")
        self.assertEqual(setup_go_steps, 4)
        self.assertEqual(workflow.count("go-version-file: '.go-version'"), setup_go_steps)
        self.assertNotRegex(workflow, r"(?m)^\s+go-version:\s")

        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn(f"Prerequisites: Go {version}", readme)

        go_mod = (ROOT / "go.mod").read_text(encoding="utf-8")
        language_version = re.search(r"(?m)^go (\d+\.\d+)\.\d+$", go_mod)
        self.assertIsNotNone(language_version)
        self.assertEqual(language_version.group(1), version.rsplit(".", 1)[0])

    def test_release_checklist_is_bounded_and_unclaimed(self):
        checklist = (ROOT / "docs/release-checklist.md").read_text(encoding="utf-8")
        version = (ROOT / ".go-version").read_text(encoding="utf-8").strip()
        for heading in (
            "## Frozen scope",
            "## Fresh-clone candidate check",
            "## Preparation gates",
            "## Immutable candidate gates",
            "## Tag and publish",
        ):
            self.assertIn(heading, checklist)
        self.assertEqual(checklist.count("- [x] "), 3)
        self.assertEqual(checklist.count("- [ ] "), 4)
        self.assertIn(f'test "$(cat .go-version)" = "{version}"', checklist)
        self.assertIn("does not declare v0.1.0 released", checklist)
        self.assertIn("exactly-once external effects", checklist)
        self.assertIn("provisions no cloud resources", checklist)

    def test_release_notes_are_bounded_and_links_resolve(self):
        notes = RELEASE_NOTES.read_text(encoding="utf-8")
        for heading in (
            "## Highlights",
            "## Run modes and upgrade notes",
            "## Compatibility and dependency review",
            "## Verification evidence",
            "## Security and deployment boundaries",
            "## Release decision",
        ):
            self.assertIn(heading, notes)

        self.assertIn("v0.1.0 is not tagged or published", notes)
        self.assertRegex(notes, r"the eventual tag\s+commit must pass its own workflow")
        self.assertIn("at-least-once execution semantics", notes)
        self.assertIn("cannot determine readiness", notes)

        local_links = []
        for target in LINK_PATTERN.findall(notes):
            parsed = urlsplit(target)
            if parsed.scheme in {"http", "https"}:
                continue
            self.assertEqual(parsed.scheme, "", f"unsupported link scheme: {target}")
            self.assertEqual(parsed.netloc, "", f"unexpected link host: {target}")
            path = (RELEASE_NOTES.parent / unquote(parsed.path)).resolve()
            self.assertTrue(path.is_relative_to(ROOT), f"link escapes repository: {target}")
            self.assertTrue(path.exists(), f"release-note link does not exist: {target}")
            local_links.append(path)
        self.assertGreaterEqual(len(local_links), 6)

        for forbidden in (
            "production-ready",
            "guarantees exactly-once",
            "99.9% availability",
        ):
            self.assertNotIn(forbidden, notes)

    def test_pgx_security_floor(self):
        go_mod = (ROOT / "go.mod").read_text(encoding="utf-8")
        match = re.search(
            r"(?m)^\s*github\.com/jackc/pgx/v5 v(\d+)\.(\d+)\.(\d+)$",
            go_mod,
        )
        self.assertIsNotNone(match)
        version = tuple(int(part) for part in match.groups())
        self.assertGreaterEqual(version, (5, 12, 0))

        go_sum = (ROOT / "go.sum").read_text(encoding="utf-8")
        self.assertNotIn("github.com/jackc/pgx/v5 v5.11.0 ", go_sum)


if __name__ == "__main__":
    unittest.main()
