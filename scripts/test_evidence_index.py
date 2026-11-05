import re
import unittest
from pathlib import Path
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[1]
INDEX = ROOT / "docs" / "evidence.md"
LINK_PATTERN = re.compile(r"(?<!!)\[[^]]+\]\(([^)]+)\)")


class PortfolioEvidenceIndexTest(unittest.TestCase):
    def test_every_local_link_resolves_inside_repository(self):
        content = INDEX.read_text(encoding="utf-8")
        local_links = []
        for target in LINK_PATTERN.findall(content):
            parsed = urlsplit(target)
            if parsed.scheme in {"http", "https"}:
                continue
            self.assertEqual(parsed.scheme, "", f"unsupported link scheme: {target}")
            self.assertEqual(parsed.netloc, "", f"unexpected link host: {target}")
            path = (INDEX.parent / unquote(parsed.path)).resolve()
            self.assertTrue(path.is_relative_to(ROOT), f"link escapes repository: {target}")
            self.assertTrue(path.exists(), f"evidence link does not exist: {target}")
            local_links.append(path)
        self.assertGreaterEqual(len(local_links), 30)

    def test_claim_map_keeps_limits_and_completion_checklist(self):
        content = INDEX.read_text(encoding="utf-8")
        self.assertIn("| Implemented claim |", content)
        self.assertIn("| Limit / non-claim |", content)
        section = content.split("## Claim-to-evidence map", 1)[1].split("\n## ", 1)[0]
        rows = [line for line in section.splitlines() if line.startswith("| ")]
        self.assertEqual(len(rows), 10)
        for row in rows[2:]:
            cells = [cell.strip() for cell in row.strip("|").split("|")]
            self.assertEqual(len(cells), 5)
            self.assertTrue(all(cells), f"empty claim evidence cell: {row}")
            self.assertTrue(cells[-1].endswith("."), f"missing explicit limit: {row}")
        self.assertEqual(content.count("- [x] "), 6)
        for forbidden in ("99.9% availability", "guarantees exactly-once", "production-ready"):
            self.assertNotIn(forbidden, content)


if __name__ == "__main__":
    unittest.main()
