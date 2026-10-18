#!/usr/bin/env python3
"""Verify the product crate dependency policy from Cargo's authoritative metadata."""
import argparse
import json
from pathlib import Path
import subprocess


def inspect():
    project = Path(__file__).resolve().parent.parent
    assert project.name == "mass-rs", "Rust workspace must remain under mass-rs"
    assert not (project.parent / "Cargo.toml").exists(), "repository root must not contain a second Cargo workspace"
    metadata = json.loads(subprocess.check_output(
        ["cargo", "metadata", "--no-deps", "--format-version", "1", "--locked"],
        cwd=project, text=True,
    ))
    assert metadata["workspace_root"] == str(project), metadata["workspace_root"]
    all_members = {p["name"]: p for p in metadata["packages"]
               if p["id"] in metadata["workspace_members"]}
    vendor=all_members.pop("codex-app-server-client")
    for package in all_members.values():
        assert all(d["name"] != "agent-client-protocol" for d in package["dependencies"]), (
            package["name"], "Rust product must use only the native Codex integration")
    assert vendor["manifest_path"]==str(project / "vendor/codex-app-server-client/Cargo.toml")
    assert vendor["version"]=="0.160.1"
    assert all(d["name"] not in all_members for d in vendor["dependencies"]), "official vendor must not depend on MASS"
    members=all_members
    expected = {"mass-daemon", *("mass-" + name for name in
                ("domain", "runtime", "journal", "events", "codex", "sessions", "http"))}
    assert members.keys() == expected, members.keys()
    graph = {name: sorted(d["name"] for d in package["dependencies"]
                         if d["name"] in members and d["kind"] is None)
             for name, package in members.items()}

    def descendants(name, trail=()):
        assert name not in trail, f"crate cycle: {trail + (name,)}"
        return set(graph[name]).union(*(descendants(child, trail + (name,))
                                        for child in graph[name]))

    allowed = {
        "mass-domain": set(),
        "mass-runtime": set(),
        "mass-journal": {"mass-domain"},
        "mass-events": {"mass-domain", "mass-runtime", "mass-journal"},
        "mass-codex": {"mass-runtime"},
        "mass-sessions": {"mass-runtime", "mass-events", "mass-journal", "mass-domain"},
        "mass-http": {"mass-sessions", "mass-runtime", "mass-events", "mass-journal", "mass-domain"},
    }
    for name in graph:
        reachable = descendants(name)
        if name in allowed:
            assert reachable <= allowed[name], (name, reachable - allowed[name])
    for name, package in members.items():
        assert package["manifest_path"] == str(
            project / ("Cargo.toml" if name == "mass-daemon" else
                       f"{name.removeprefix('mass-')}/Cargo.toml"))
        assert any("test" in target["kind"] for target in package["targets"]), (
            name, "each MASS crate must own public-interface integration tests")
        for target in package["targets"]:
            assert Path(target["src_path"]).is_relative_to(project), (name, target["src_path"])
    # Inspect the complete resolved production closure, including external crates.
    host = next(line.removeprefix("host: ") for line in subprocess.check_output(
        ["rustc", "-Vv"], text=True).splitlines() if line.startswith("host: "))
    resolved = json.loads(subprocess.check_output(
        ["cargo", "metadata", "--format-version", "1", "--locked", "--filter-platform", host],
        cwd=project, text=True))
    packages = {package["id"]: package for package in resolved["packages"]}
    nodes = {node["id"]: node for node in resolved["resolve"]["nodes"]}
    production_closures = {}
    for name in allowed.keys() - {"mass-codex"}:
        seen = set()
        pending = [members[name]["id"]]
        while pending:
            identity = pending.pop()
            if identity in seen:
                continue
            seen.add(identity)
            pending.extend(dependency["pkg"] for dependency in nodes[identity]["deps"]
                           if any(kind["kind"] in (None, "build") for kind in dependency["dep_kinds"]))
        closure = sorted({packages[identity]["name"] for identity in seen})
        assert not any(value.startswith("codex-") or value in ("mass-codex", "agent-client-protocol")
                       for value in closure), (name, closure)
        production_closures[name] = closure
    # Adapter-only vocabulary must not return to the generic commit/storage seam.
    for crate in ("domain", "runtime", "events", "journal", "sessions", "http"):
        for source in (project / crate / "src").rglob("*.rs"):
            content = source.read_text()
            for native_field in ('"nativeItem"', '"nativeResult"', '"threadId"', '"turnId"', '"hostGeneration"', '"codex"', '"codex-remote"', 'codex_app_server', 'mass_codex', '"sdkRevision"', '"connectorVersion"'):
                assert native_field not in content, (str(source), "native protocol knowledge outside adapter", native_field)
    for source in (project / "journal" / "src").rglob("*.rs"):
        content = source.read_text()
        for private_knowledge in ('["evidence"]["version"]', 'Uuid::parse_str', '"connectorVersion"', '"transport"'):
            assert private_knowledge not in content, (str(source), "adapter-owned recovery leaked into Journal", private_knowledge)
    from check_official_client import inspect as inspect_official
    official=inspect_official()
    return {"officialSource":official,"members": sorted(members), "officialVendor":[vendor["name"]],"dependencies": graph, "acyclic": True,"productionClosures": production_closures}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    result = inspect()
    report = json.dumps(result, indent=2) + "\n"
    if args.output:
        args.output.write_text(report)
    print(report, end="")
