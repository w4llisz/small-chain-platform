import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.release_bundle import (
    BINARY_PACKAGES,
    DOCUMENT_SOURCES,
    METADATA_PATH,
    canonical_json,
    expected_root,
    metadata,
    write_deterministic_archive,
    write_manifest,
)
from scripts.release_bundle_smoke import SmokeError, extract_archive, read_archive


REPOSITORY = "w4llisz/small-chain-platform"
SHA = "a" * 40
VERSION = "v0.1.0"
TOOLCHAIN = "go1.27.3"
ROOT = Path(__file__).resolve().parents[1]


class ReleaseBundleSmokeTest(unittest.TestCase):
    def make_fixture(self) -> tuple[tempfile.TemporaryDirectory, Path]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name) / expected_root(VERSION)
        (root / "bin").mkdir(parents=True)
        for relative in BINARY_PACKAGES:
            path = root / relative
            path.write_bytes(f"fake:{relative}".encode())
            path.chmod(0o755)
        for relative in DOCUMENT_SOURCES:
            (root / relative).write_text(f"fixture {relative}\n", encoding="utf-8")
        (root / METADATA_PATH).write_bytes(
            canonical_json(metadata(REPOSITORY, SHA, VERSION, TOOLCHAIN))
        )
        write_manifest(root)
        archive = Path(temporary.name) / "release.tar.gz"
        write_deterministic_archive(root, archive)
        return temporary, archive

    def test_valid_archive_is_extracted_into_new_directory(self):
        temporary, archive = self.make_fixture()
        digest, content = read_archive(archive, REPOSITORY, SHA, VERSION)
        destination = Path(temporary.name) / "runtime"
        root = extract_archive(content, destination, VERSION)
        self.assertEqual(64, len(digest))
        self.assertTrue((root / "bin" / "small-chain").is_file())
        self.assertEqual(0o755, (root / "bin" / "healthcheck").stat().st_mode & 0o777)
        self.assertEqual([], list(root.rglob("*.go")))

    def test_existing_runtime_directory_is_rejected(self):
        temporary, archive = self.make_fixture()
        _digest, content = read_archive(archive, REPOSITORY, SHA, VERSION)
        destination = Path(temporary.name) / "runtime"
        destination.mkdir()
        with self.assertRaisesRegex(SmokeError, "must not already exist"):
            extract_archive(content, destination, VERSION)

    def test_link_entry_is_rejected_before_extraction(self):
        temporary, archive = self.make_fixture()
        replacement = Path(temporary.name) / "linked.tar.gz"
        root = expected_root(VERSION)
        with tarfile.open(archive, "r:gz") as original, tarfile.open(
            replacement, "w:gz"
        ) as changed:
            for member in original.getmembers():
                stream = original.extractfile(member)
                if member.name == f"{root}/LICENSE":
                    linked = tarfile.TarInfo(member.name)
                    linked.type = tarfile.SYMTYPE
                    linked.linkname = "target"
                    changed.addfile(linked)
                else:
                    data = stream.read() if stream is not None else b""
                    changed.addfile(member, io.BytesIO(data))
        raw = bytearray(replacement.read_bytes())
        raw[4:8] = (0).to_bytes(4, "little")
        replacement.write_bytes(raw)
        with self.assertRaisesRegex(SmokeError, "regular files"):
            read_archive(replacement, REPOSITORY, SHA, VERSION)

    def test_workflow_consumes_uploaded_archive_without_setup_go(self):
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        packaged = workflow.split("  packaged-smoke:\n", 1)[1]
        self.assertIn("    needs: [release-bundle]\n", packaged)
        self.assertIn('gh run download "$GITHUB_RUN_ID"', packaged)
        self.assertIn('make release-bundle-smoke RELEASE_SHA="$GITHUB_SHA"', packaged)
        self.assertNotIn("actions/setup-go", packaged)


if __name__ == "__main__":
    unittest.main()
