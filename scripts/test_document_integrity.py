"""Reject transport truncation accidentally committed as documentation."""

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TRUNCATION = re.compile(
    r"(?m)^Warning: truncated output\b|^Total output lines: \d+\s*$"
    r"|(?:…|\.{3})\d+ tokens truncated(?:…|\.{3})"
)


def assert_complete_document(text: str) -> None:
    match = TRUNCATION.search(text)
    if match:
        raise ValueError(f"transport truncation marker at offset {match.start()}")


class DocumentIntegrityTest(unittest.TestCase):
    def test_rejects_transport_markers(self):
        samples = (
            'Warning: truncated output (original token count: 5143)\n# Small Chain\n',
            'Total output lines: 303\n# Small Chain\n',
            '| release …143 tokens truncated…n.md |',
            '| release ...143 tokens truncated...n.md |',
        )
        for sample in samples:
            with self.subTest(sample=sample):
                with self.assertRaisesRegex(ValueError, 'transport truncation'):
                    assert_complete_document(sample)

    def test_allows_normal_discussion_and_ellipsis(self):
        assert_complete_document('# Notes\nOutput is truncated at the byte limit.\n… next steps …\n')

    def test_repository_documents_have_no_transport_markers(self):
        documents = [ROOT / 'README.md', *sorted((ROOT / 'docs').rglob('*.md'))]
        for path in documents:
            with self.subTest(path=str(path.relative_to(ROOT))):
                assert_complete_document(path.read_text(encoding='utf-8'))

    def test_readme_map_preserves_review_and_operator_entry_points(self):
        text = (ROOT / 'README.md').read_text(encoding='utf-8')
        self.assertTrue(text.startswith('# Small Chain\n'))
        table = text.split('## Repository map\n', 1)[1].split('\n## ', 1)[0]
        paths = re.findall(r'^\| `([^`]+)` \|', table, re.MULTILINE)
        for required in (
            'scripts/candidate_evidence_summary.py',
            'scripts/verify_candidate_evidence_summary.py',
            'docs/measurements/', 'docs/incidents/',
            'docs/api.md', 'docs/runbook.md', 'docs/verification.md',
        ):
            self.assertEqual(paths.count(required), 1, required)
        for entry in paths:
            self.assertTrue((ROOT / entry).exists(), entry)


if __name__ == '__main__':
    unittest.main()
