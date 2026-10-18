#!/usr/bin/env python3
"""Build and fail closed on extra deployment libraries. Run on the target OS."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

PROJECT = Path(__file__).resolve().parent.parent
TOOLCHAIN = "1.96.0"
TARGET = "aarch64-apple-darwin"
MINIMUM_OS = "14.0"


def output(*args):
    return subprocess.check_output(args, text=True, cwd=PROJECT)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inspect(binary):
    report = {"file": output("file", str(binary))}
    report["libraries"] = output("otool", "-L", str(binary))
    report["loadCommands"] = output("otool", "-l", str(binary))
    libraries = [line.strip().split(" (", 1)[0] for line in report["libraries"].splitlines()[1:]]
    if not libraries or any(not name.startswith(("/usr/lib/", "/System/Library/Frameworks/")) for name in libraries):
        raise RuntimeError(f"non-system Mach-O libraries: {libraries}")
    if "LC_RPATH" in report["loadCommands"]:
        raise RuntimeError("release contains a runtime library search path")
    if "arm64" not in report["file"]:
        raise RuntimeError("unexpected Mach-O architecture")
    minimum = re.search(r"\bminos\s+(\S+)", report["loadCommands"])
    if not minimum or minimum.group(1) != MINIMUM_OS:
        raise RuntimeError("unexpected macOS deployment target")
    report["help"] = output(str(binary), "--help")
    return report


def source_files():
    sources = [p for directory in ["src", "examples", "tests", "domain", "runtime", "journal", "events", "codex", "sessions", "http"]
               for p in (PROJECT / directory).rglob("*") if p.is_file() and (p.suffix == ".rs" or p.name == "Cargo.toml")]
    sources += [p for p in (PROJECT / "vendor/codex-app-server-client").rglob("*") if p.is_file()]
    sources += [PROJECT / name for name in ["Cargo.toml", "Cargo.lock", "scripts/build_release.py"]]
    sources += [PROJECT.parent / "Makefile"]
    return sorted(set(sources))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, default=PROJECT.parent / "dist")
    parser.add_argument("--verify-only", action="store_true")
    args = parser.parse_args()
    destination = args.dist.resolve() / TARGET
    binary = destination / "massd"
    if args.verify_only:
        report = inspect(binary)
        manifest = json.loads((destination / "manifest.json").read_text())
        if digest(binary) != manifest["sha256"]:
            raise RuntimeError("artifact SHA256 differs from manifest")
        current = {str(path.relative_to(PROJECT.parent)): digest(path) for path in source_files()}
        if current != manifest["sourceSha256"]:
            raise RuntimeError("release source fingerprint differs from current checkout")
        if manifest["target"] != TARGET or manifest["minimumOS"] != MINIMUM_OS:
            raise RuntimeError("release platform differs from delivery contract")
        subprocess.run(["python3", "scripts/check_official_client.py"], cwd=PROJECT, check=True)
        print(json.dumps({"sha256": digest(binary), "sourceInputs": len(current), "dependencies": report}, indent=2))
        return
    version = output("rustc", "-Vv")
    if not version.startswith(f"rustc {TOOLCHAIN} ") or f"host: {TARGET}\n" not in version:
        raise RuntimeError(f"use native Rust {TOOLCHAIN} for {TARGET}")
    # Reject hidden flags that could defeat the recorded release contract.
    for key in os.environ:
        if key.endswith("RUSTFLAGS") or key.startswith("CARGO_PROFILE_RELEASE_"):
            raise RuntimeError(f"unset {key} before the reproducible release build")
    env = dict(os.environ, CARGO_BUILD_JOBS=os.environ.get("CARGO_BUILD_JOBS", "2"))
    # Cargo configuration files can override manifest profiles too.
    env.update(CARGO_PROFILE_RELEASE_OPT_LEVEL="2", CARGO_PROFILE_RELEASE_DEBUG="0",
               CARGO_PROFILE_RELEASE_STRIP="debuginfo", CARGO_PROFILE_RELEASE_PANIC="unwind",
               CARGO_PROFILE_RELEASE_INCREMENTAL="false")
    flags = ""
    env["RUSTFLAGS"] = flags
    env["MACOSX_DEPLOYMENT_TARGET"] = MINIMUM_OS
    subprocess.run(["python3", "scripts/check_official_client.py"], cwd=PROJECT, check=True)
    command = ["cargo", "build", "--locked", "--release", "--target", TARGET, "--bin", "massd"]
    subprocess.run(command, cwd=PROJECT, env=env, check=True)
    # Cargo owns target-dir resolution, including relative env/config paths.
    target_dir = Path(json.loads(output("cargo", "metadata", "--locked", "--no-deps",
                                       "--format-version", "1"))["target_directory"])
    built = target_dir / TARGET / "release/massd"
    dependencies = inspect(built)
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(built, binary)
    sources = source_files()
    tools = {"cc": output("cc", "--version"), "python": output("python3", "--version")}
    tools["sdk"] = output("xcrun", "--show-sdk-version").strip()
    tree = output("cargo", "tree", "--locked", "--target", TARGET, "--edges", "normal,build",
                  "--prefix", "none", "--format", "{p} features={f}")
    native = {"aws-lc-sys", "libsqlite3-sys", "lzma-sys", "openssl-sys", "ring", "zstd-sys", "libdbus-sys"}
    manifest = {"workspaceRoot": str(PROJECT), "target": TARGET, "minimumOS": MINIMUM_OS, "rustc": version,
                "cargo": output("cargo", "-V").strip(), "command": command, "rustflags": flags,
                "buildTools": tools, "linkEnvironment": {key: env[key] for key in
                    ["MACOSX_DEPLOYMENT_TARGET"] if key in env},
                "panic": "unwind", "sha256": digest(binary), "size": binary.stat().st_size,
                "nativeDependencyFeatures": sorted({line for line in tree.splitlines() if line.split(" ", 1)[0] in native}),
                "codexRevision": "d27764b82f7118f674371e6d6e76271d9d606edb",
                "sourceSha256": {str(p.relative_to(PROJECT.parent)): digest(p) for p in sorted(sources)},
                "dependencies": dependencies}
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"artifact": str(binary), "sha256": manifest["sha256"], "size": manifest["size"]}))


if __name__ == "__main__":
    main()
