#!/usr/bin/env python3
"""Build and verify the bounded Linux amd64 release-candidate archive."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path, PurePosixPath
from typing import Callable


ROOT = Path(__file__).resolve().parents[1]
REPOSITORY_PATTERN = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
SHA_PATTERN = re.compile(r"[0-9a-f]{40}")
VERSION_PATTERN = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+")
MANIFEST_LINE_PATTERN = re.compile(r"([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9./-]*)")
MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
MAX_EXPANDED_BYTES = 128 * 1024 * 1024

BINARY_PACKAGES = {
    "bin/healthcheck": "github.com/w4llisz/small-chain-platform/cmd/healthcheck",
    "bin/migrate": "github.com/w4llisz/small-chain-platform/cmd/migrate",
    "bin/small-chain": "github.com/w4llisz/small-chain-platform/cmd/small-chain",
    "bin/small-chain-worker": "github.com/w4llisz/small-chain-platform/cmd/small-chain-worker",
}
DOCUMENT_SOURCES = {
    "LICENSE": ROOT / "LICENSE",
    "RELEASE-NOTES.md": ROOT / "docs" / "releases" / "v0.1.0.md",
}
METADATA_PATH = "BUILD-METADATA.json"
MANIFEST_PATH = "SHA256SUMS"
EXPECTED_FILES = tuple(
    sorted((*BINARY_PACKAGES, *DOCUMENT_SOURCES, METADATA_PATH, MANIFEST_PATH))
)


class BundleError(ValueError):
    """The bundle cannot support the requested release-candidate claim."""


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def canonical_json(value: object) -> bytes:
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise BundleError(f"duplicate JSON key: {key}")
        value[key] = item
    return value


def require_keys(value: object, expected: set[str], context: str) -> dict[str, object]:
    if not isinstance(value, dict):
        raise BundleError(f"{context} must be an object")
    observed = set(value)
    if observed != expected:
        raise BundleError(
            f"{context} keys differ: missing={sorted(expected - observed)}, "
            f"unexpected={sorted(observed - expected)}"
        )
    return value


def validate_inputs(repository: str, sha: str, version: str) -> None:
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise BundleError("repository must use owner/name form")
    if not SHA_PATTERN.fullmatch(sha):
        raise BundleError("SHA must be 40 lowercase hexadecimal characters")
    if not VERSION_PATTERN.fullmatch(version):
        raise BundleError("version must use vMAJOR.MINOR.PATCH form")


def expected_root(version: str) -> str:
    return f"small-chain-platform-{version}-linux-amd64"


def required_toolchain() -> str:
    version = (ROOT / ".go-version").read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise BundleError(".go-version must contain one exact Go patch version")
    return f"go{version}"


def run(command: list[str], *, env: dict[str, str] | None = None) -> str:
    process = subprocess.run(
        command,
        cwd=ROOT,
        env=env,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if process.returncode != 0:
        detail = process.stderr.strip() or process.stdout.strip()
        raise BundleError(f"command failed ({' '.join(command)}): {detail}")
    return process.stdout.strip()


def verify_source(sha: str) -> None:
    if run(["git", "rev-parse", "HEAD"]) != sha:
        raise BundleError("requested SHA does not match the checked-out HEAD")
    if run(["git", "status", "--porcelain"]):
        raise BundleError("release bundle requires a clean tracked and untracked tree")


def metadata(repository: str, sha: str, version: str, toolchain: str) -> dict[str, object]:
    return {
        "binaries": [
            {"package": package, "path": path}
            for path, package in sorted(BINARY_PACKAGES.items())
        ],
        "commit": sha,
        "repository": repository,
        "schema_version": 1,
        "target": {"cgo_enabled": False, "goarch": "amd64", "goos": "linux"},
        "toolchain": toolchain,
        "version": version,
    }


def write_manifest(bundle_root: Path) -> None:
    paths = sorted(
        path.relative_to(bundle_root).as_posix()
        for path in bundle_root.rglob("*")
        if path.is_file() and path.name != MANIFEST_PATH
    )
    lines = [f"{sha256_bytes((bundle_root / path).read_bytes())}  {path}\n" for path in paths]
    (bundle_root / MANIFEST_PATH).write_text("".join(lines), encoding="ascii")


def write_deterministic_archive(bundle_root: Path, archive: Path) -> None:
    archive.parent.mkdir(parents=True, exist_ok=True)
    with archive.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(
                fileobj=compressed,
                mode="w",
                format=tarfile.GNU_FORMAT,
            ) as bundle:
                for path in sorted(item for item in bundle_root.rglob("*") if item.is_file()):
                    relative = path.relative_to(bundle_root).as_posix()
                    data = path.read_bytes()
                    info = tarfile.TarInfo(f"{bundle_root.name}/{relative}")
                    info.size = len(data)
                    info.mode = 0o755 if relative in BINARY_PACKAGES else 0o644
                    info.uid = info.gid = 0
                    info.uname = info.gname = "root"
                    info.mtime = 0
                    bundle.addfile(info, fileobj=io.BytesIO(data))


def build_bundle(archive: Path, repository: str, sha: str, version: str) -> None:
    validate_inputs(repository, sha, version)
    verify_source(sha)
    toolchain = required_toolchain()
    if run(["go", "env", "GOVERSION"]) != toolchain:
        raise BundleError(f"release build requires {toolchain}")

    with tempfile.TemporaryDirectory() as temporary:
        bundle_root = Path(temporary) / expected_root(version)
        (bundle_root / "bin").mkdir(parents=True)
        environment = os.environ.copy()
        environment.update(
            CGO_ENABLED="0",
            GOARCH="amd64",
            GOOS="linux",
            GOTOOLCHAIN="local",
            SOURCE_DATE_EPOCH="0",
        )
        for relative, package in sorted(BINARY_PACKAGES.items()):
            output = bundle_root / relative
            run(
                [
                    "go",
                    "build",
                    "-mod=readonly",
                    "-trimpath",
                    "-buildvcs=false",
                    "-ldflags=-buildid=",
                    "-o",
                    str(output),
                    package,
                ],
                env=environment,
            )
            output.chmod(0o755)

        for relative, source in DOCUMENT_SOURCES.items():
            shutil.copyfile(source, bundle_root / relative)
        (bundle_root / METADATA_PATH).write_bytes(
            canonical_json(metadata(repository, sha, version, toolchain))
        )
        write_manifest(bundle_root)
        write_deterministic_archive(bundle_root, archive)


def parse_manifest(raw: bytes, expected_paths: set[str]) -> dict[str, str]:
    try:
        text = raw.decode("ascii")
    except UnicodeDecodeError as exc:
        raise BundleError("SHA256SUMS must be ASCII") from exc
    checksums: dict[str, str] = {}
    for line in text.splitlines():
        match = MANIFEST_LINE_PATTERN.fullmatch(line)
        if not match:
            raise BundleError("SHA256SUMS contains a malformed line")
        digest, path = match.groups()
        if path in checksums:
            raise BundleError(f"SHA256SUMS repeats {path}")
        checksums[path] = digest
    if list(checksums) != sorted(checksums):
        raise BundleError("SHA256SUMS paths must be sorted")
    if set(checksums) != expected_paths:
        raise BundleError("SHA256SUMS coverage differs from archive contents")
    expected_text = "".join(f"{checksums[path]}  {path}\n" for path in sorted(checksums))
    if text != expected_text:
        raise BundleError("SHA256SUMS is not in canonical form")
    return checksums


def inspect_go_binary(path: Path) -> dict[str, object]:
    output = run(["go", "version", "-m", str(path)])
    lines = output.splitlines()
    if not lines or ": " not in lines[0]:
        raise BundleError(f"cannot parse Go metadata for {path.name}")
    go_version = lines[0].rsplit(": ", 1)[1]
    package = module = ""
    build: dict[str, str] = {}
    for line in lines[1:]:
        fields = line.strip().split("\t")
        if fields[0] == "path" and len(fields) == 2:
            package = fields[1]
        elif fields[0] == "mod" and len(fields) >= 2:
            module = fields[1]
        elif fields[0] == "build" and len(fields) == 2 and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            build[key] = value
    return {"build": build, "go_version": go_version, "module": module, "package": package}


def verify_metadata(
    raw: bytes,
    *,
    repository: str,
    sha: str,
    version: str,
    toolchain: str,
) -> None:
    try:
        document = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise BundleError("BUILD-METADATA.json must be valid UTF-8 JSON") from exc
    if raw != canonical_json(document):
        raise BundleError("BUILD-METADATA.json is not canonical")
    if document != metadata(repository, sha, version, toolchain):
        raise BundleError("BUILD-METADATA.json does not match the expected candidate")


def verify_bundle(
    archive: Path,
    repository: str,
    sha: str,
    version: str,
    *,
    inspector: Callable[[Path], dict[str, object]] = inspect_go_binary,
) -> str:
    validate_inputs(repository, sha, version)
    toolchain = required_toolchain()
    if archive.stat().st_size > MAX_ARCHIVE_BYTES:
        raise BundleError("release archive exceeds the 64 MiB limit")
    archive_bytes = archive.read_bytes()
    if len(archive_bytes) < 10 or int.from_bytes(archive_bytes[4:8], "little") != 0:
        raise BundleError("gzip header must use the deterministic zero timestamp")
    archive_digest = sha256_bytes(archive_bytes)
    root = expected_root(version)
    expected_names = [f"{root}/{path}" for path in EXPECTED_FILES]
    content: dict[str, bytes] = {}

    try:
        with tarfile.open(archive, "r:gz") as bundle:
            members = bundle.getmembers()
            if [member.name for member in members] != expected_names:
                raise BundleError("archive entries differ from the fixed release layout")
            total = 0
            for member in members:
                relative = member.name.removeprefix(f"{root}/")
                if (
                    not member.isfile()
                    or member.name.startswith("/")
                    or ".." in PurePosixPath(member.name).parts
                ):
                    raise BundleError("archive entries must be bounded regular files")
                if member.uid != 0 or member.gid != 0 or member.mtime != 0:
                    raise BundleError(f"nondeterministic tar metadata for {relative}")
                expected_mode = 0o755 if relative in BINARY_PACKAGES else 0o644
                if member.mode != expected_mode:
                    raise BundleError(f"unexpected mode for {relative}")
                total += member.size
                if total > MAX_EXPANDED_BYTES:
                    raise BundleError("expanded release archive exceeds the 128 MiB limit")
                stream = bundle.extractfile(member)
                if stream is None:
                    raise BundleError(f"cannot read {relative}")
                content[relative] = stream.read()
    except (tarfile.TarError, gzip.BadGzipFile, EOFError) as exc:
        raise BundleError("release archive is not valid tar.gz") from exc

    verify_metadata(
        content[METADATA_PATH],
        repository=repository,
        sha=sha,
        version=version,
        toolchain=toolchain,
    )
    checksums = parse_manifest(
        content[MANIFEST_PATH], set(EXPECTED_FILES) - {MANIFEST_PATH}
    )
    for path, expected in checksums.items():
        if sha256_bytes(content[path]) != expected:
            raise BundleError(f"SHA-256 mismatch for {path}")

    with tempfile.TemporaryDirectory() as temporary:
        directory = Path(temporary)
        for relative, package in BINARY_PACKAGES.items():
            binary = directory / Path(relative).name
            binary.write_bytes(content[relative])
            details = inspector(binary)
            build = details.get("build")
            if (
                details.get("go_version") != toolchain
                or details.get("package") != package
                or details.get("module") != "github.com/w4llisz/small-chain-platform"
                or not isinstance(build, dict)
                or build.get("CGO_ENABLED") != "0"
                or build.get("GOARCH") != "amd64"
                or build.get("GOOS") != "linux"
            ):
                raise BundleError(f"embedded Go metadata differs for {relative}")

    return archive_digest


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in ("build", "verify"):
        subparser = subparsers.add_parser(command)
        subparser.add_argument("--archive", required=True, type=Path)
        subparser.add_argument("--repository", required=True)
        subparser.add_argument("--sha", required=True)
        subparser.add_argument("--version", required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.command == "build":
            build_bundle(args.archive, args.repository, args.sha, args.version)
        digest = verify_bundle(
            args.archive,
            args.repository,
            args.sha,
            args.version,
        )
    except (BundleError, OSError) as exc:
        raise SystemExit(f"release bundle failed: {exc}") from exc
    print(f"PASS: bounded release bundle verified (sha256:{digest})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
