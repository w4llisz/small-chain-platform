import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.release_bundle import (
    BINARY_PACKAGES,
    BundleError,
    DOCUMENT_SOURCES,
    METADATA_PATH,
    canonical_json,
    expected_root,
    metadata,
    verify_bundle,
    write_deterministic_archive,
    write_manifest,
)


REPOSITORY = "w4llisz/small-chain-platform"
SHA = "a" * 40
VERSION = "v0.1.0"
TOOLCHAIN = "go1.27.3"
ROOT = Path(__file__).resolve().parents[1]


def valid_inspector(path: Path) -> dict[str, object]:
    package = BINARY_PACKAGES[f"bin/{path.name}"]
    return {
        "build": {"CGO_ENABLED": "0", "GOARCH": "amd64", "GOOS": "linux"},
        "go_version": TOOLCHAIN,
        "module": "github.com/w4llisz/small-chain-platform",
        "package": package,
    }


class ReleaseBundleTest(unittest.TestCase):
    def make_fixture(self) -> tuple[tempfile.TemporaryDirectory, Path, Path]:
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
        return temporary, root, archive

    def test_valid_bundle_passes(self):
        _temporary, _root, archive = self.make_fixture()
        digest = verify_bundle(
            archive,
            REPOSITORY,
            SHA,
            VERSION,
            inspector=valid_inspector,
        )
        self.assertEqual(64, len(digest))

    def test_archive_is_deterministic(self):
        temporary, root, first = self.make_fixture()
        second = Path(temporary.name) / "second.tar.gz"
        write_deterministic_archive(root, second)
        self.assertEqual(first.read_bytes(), second.read_bytes())

    def test_wrong_commit_binding_is_rejected(self):
        _temporary, _root, archive = self.make_fixture()
        with self.assertRaisesRegex(BundleError, "expected candidate"):
            verify_bundle(
                archive,
                REPOSITORY,
                "b" * 40,
                VERSION,
                inspector=valid_inspector,
            )

    def test_manifest_detects_file_corruption(self):
        _temporary, root, archive = self.make_fixture()
        (root / "bin" / "small-chain").write_bytes(b"changed after manifest")
        write_deterministic_archive(root, archive)
        with self.assertRaisesRegex(BundleError, "SHA-256 mismatch"):
            verify_bundle(
                archive,
                REPOSITORY,
                SHA,
                VERSION,
                inspector=valid_inspector,
            )

    def test_extra_entry_is_rejected(self):
        _temporary, root, archive = self.make_fixture()
        (root / "unexpected.txt").write_text("unexpected", encoding="utf-8")
        write_deterministic_archive(root, archive)
        with self.assertRaisesRegex(BundleError, "fixed release layout"):
            verify_bundle(
                archive,
                REPOSITORY,
                SHA,
                VERSION,
                inspector=valid_inspector,
            )

    def test_nondeterministic_gzip_timestamp_is_rejected(self):
        _temporary, _root, archive = self.make_fixture()
        raw = bytearray(archive.read_bytes())
        raw[4:8] = (1).to_bytes(4, "little")
        archive.write_bytes(raw)
        with self.assertRaisesRegex(BundleError, "zero timestamp"):
            verify_bundle(
                archive,
                REPOSITORY,
                SHA,
                VERSION,
                inspector=valid_inspector,
            )

    def test_symbolic_link_entry_is_rejected(self):
        temporary, bundle_root, archive = self.make_fixture()
        root = bundle_root.name
        replacement = Path(temporary.name) / "symlink.tar.gz"
        with tarfile.open(replacement, "w:gz") as bundle:
            for path in sorted(item for item in bundle_root.rglob("*") if item.is_file()):
                relative = path.relative_to(bundle_root).as_posix()
                entry = tarfile.TarInfo(f"{root}/{relative}")
                if relative == "LICENSE":
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = "target"
                    bundle.addfile(entry)
                else:
                    data = path.read_bytes()
                    entry.size = len(data)
                    entry.mode = 0o755 if relative in BINARY_PACKAGES else 0o644
                    bundle.addfile(entry, io.BytesIO(data))
        raw = bytearray(replacement.read_bytes())
        raw[4:8] = (0).to_bytes(4, "little")
        replacement.write_bytes(raw)
        with self.assertRaisesRegex(BundleError, "regular files"):
            verify_bundle(
                replacement,
                REPOSITORY,
                SHA,
                VERSION,
                inspector=valid_inspector,
            )

    def test_embedded_module_metadata_is_required(self):
        _temporary, _root, archive = self.make_fixture()

        def wrong_module(path: Path) -> dict[str, object]:
            details = valid_inspector(path)
            details["module"] = "example.invalid/substitution"
            return details

        with self.assertRaisesRegex(BundleError, "embedded Go metadata"):
            verify_bundle(
                archive,
                REPOSITORY,
                SHA,
                VERSION,
                inspector=wrong_module,
            )

    def test_workflow_builds_only_after_candidate_attestation(self):
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("  release-bundle:\n", workflow)
        self.assertIn("    needs: [candidate-attestation]\n", workflow)
        self.assertIn('make release-bundle RELEASE_SHA="$GITHUB_SHA"', workflow)
        self.assertIn("name: release-bundle-${{ github.sha }}", workflow)
        self.assertIn("compression-level: 0", workflow)


if __name__ == "__main__":
    unittest.main()
