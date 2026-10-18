#!/usr/bin/env python3
"""Check the fixed official source and its reversible, reviewable local patch."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile


def inspect():
    root=Path(__file__).resolve().parent.parent / "vendor/codex-app-server-client"
    provenance=json.loads((root / "UPSTREAM.json").read_text())
    assert provenance["revision"]=="d27764b82f7118f674371e6d6e76271d9d606edb"
    with tempfile.TemporaryDirectory(prefix="mass-official-check-") as temporary:
        copy=Path(temporary)
        for relative in provenance["sha256"]:
            path=copy / relative
            path.parent.mkdir(parents=True,exist_ok=True)
            shutil.copy2(root / relative,path)
        shutil.copy2(root / "src/notification_methods.rs",copy / "src/notification_methods.rs")
        subprocess.run(["git","apply","--reverse",str(root / "MASS.patch")],cwd=copy,check=True)
        for relative,expected in provenance["sha256"].items():
            actual=hashlib.sha256((copy / relative).read_bytes()).hexdigest()
            assert actual==expected,(relative,actual,expected)
        assert not (copy / "src/notification_methods.rs").exists()
    return {"officialRevision":provenance["revision"],"pristineFiles":len(provenance["sha256"]),"patchSha256":hashlib.sha256((root / "MASS.patch").read_bytes()).hexdigest()}


if __name__=="__main__":
    print(json.dumps(inspect(),indent=2))
