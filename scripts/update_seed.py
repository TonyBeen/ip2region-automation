#!/usr/bin/env python3
"""
Helper script to fetch the latest fa1seut0pia/ip2region-xdb release
and update the local seed/ directory tracked by the repository.
"""
from pathlib import Path
import json
import sys
import time

repo_root = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(repo_root))

from updater import read_release, download_asset, ASSETS, UpdateError


def update_seed(seed_dir: Path, timeout: int = 300) -> None:
    seed_dir = seed_dir.resolve()
    seed_dir.mkdir(parents=True, exist_ok=True)

    print("Checking latest fa1seut0pia/ip2region-xdb release...")
    rel = read_release(timeout=60)
    version = rel["version"]
    print("Latest upstream release: {}".format(version))

    manifest_path = seed_dir / "manifest.json"
    if manifest_path.exists():
        try:
            with manifest_path.open("r", encoding="utf-8") as f:
                old_manifest = json.load(f)
            if old_manifest.get("version") == version:
                print("seed/ is already up to date with release {}.".format(version))
                return
        except Exception:
            pass

    manifest = {
        "version": version,
        "source": "fa1seut0pia/ip2region-xdb",
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "assets": {}
    }

    for name in ASSETS:
        asset = rel["assets"][name]
        dest = seed_dir / name
        size_mb = asset["size"] / 1024 / 1024
        print("Downloading {} ({:.1f} MB)...".format(name, size_mb))
        t0 = time.time()
        sha = download_asset(asset, dest, timeout=timeout)
        print("Downloaded {} in {:.1f}s (SHA-256: {}...)".format(name, time.time() - t0, sha[:16]))
        manifest["assets"][name] = {"size": asset["size"], "sha256": sha}

    with manifest_path.open("w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)
        f.write("\n")

    print("Successfully updated seed/ to release {}!".format(version))


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else repo_root / "seed"
    try:
        update_seed(target)
    except UpdateError as exc:
        print("Error: {}".format(exc), file=sys.stderr)
        sys.exit(1)
