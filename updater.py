#!/usr/bin/env python3
"""Fetch and atomically activate the latest ip2region xdb release."""

import argparse
import fcntl
import hashlib
import json
import logging
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Dict


API_URL = "https://api.github.com/repos/fa1seut0pia/ip2region-xdb/releases/latest"
ASSETS = ("ip2region_v4.xdb", "ip2region_v6.xdb")
USER_AGENT = "ip2region-automation/1.0"
CHUNK_SIZE = 1024 * 1024
VERSION_PATTERN = re.compile(r"^[A-Za-z0-9._-]+$")


class UpdateError(RuntimeError):
    pass


def request(url: str, timeout: int):
    req = urllib.request.Request(
        url,
        headers={"Accept": "application/vnd.github+json", "User-Agent": USER_AGENT},
    )
    return urllib.request.urlopen(req, timeout=timeout)


def read_release(timeout: int) -> Dict[str, Any]:
    try:
        with request(API_URL, timeout) as response:
            release = json.load(response)
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise UpdateError("failed to fetch release metadata: {}".format(exc)) from exc

    if not isinstance(release, dict) or not isinstance(release.get("assets"), list):
        raise UpdateError("GitHub returned an invalid release response")
    version = release.get("tag_name")
    if not isinstance(version, str) or not VERSION_PATTERN.fullmatch(version):
        raise UpdateError("invalid release tag: {!r}".format(version))

    by_name = {asset.get("name"): asset for asset in release["assets"] if isinstance(asset, dict)}
    selected = {}
    for name in ASSETS:
        asset = by_name.get(name)
        if not asset:
            raise UpdateError("release {} is missing asset {}".format(version, name))
        url = asset.get("browser_download_url")
        digest = asset.get("digest")
        size = asset.get("size")
        if not isinstance(url, str) or not url.startswith("https://"):
            raise UpdateError("asset {} has an invalid download URL".format(name))
        if not isinstance(size, int) or size <= 0:
            raise UpdateError("asset {} has an invalid size".format(name))
        if not isinstance(digest, str) or not re.fullmatch(r"sha256:[0-9a-fA-F]{64}", digest):
            raise UpdateError("asset {} has no valid SHA-256 digest".format(name))
        selected[name] = {"url": url, "size": size, "sha256": digest[7:].lower()}
    return {"version": version, "assets": selected}


def download_asset(asset: Dict[str, Any], destination: Path, timeout: int) -> str:
    digest = hashlib.sha256()
    received = 0
    try:
        with request(asset["url"], timeout) as response, destination.open("xb") as output:
            while True:
                chunk = response.read(CHUNK_SIZE)
                if not chunk:
                    break
                received += len(chunk)
                if received > asset["size"]:
                    raise UpdateError("download exceeded expected size for {}".format(destination.name))
                digest.update(chunk)
                output.write(chunk)
            output.flush()
            os.fsync(output.fileno())
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        safe_unlink(destination)
        if isinstance(exc, UpdateError):
            raise
        raise UpdateError("failed to download {}: {}".format(destination.name, exc)) from exc

    actual = digest.hexdigest()
    if received != asset["size"]:
        safe_unlink(destination)
        raise UpdateError("size mismatch for {}: expected {}, got {}".format(
            destination.name, asset["size"], received))
    if actual != asset["sha256"]:
        safe_unlink(destination)
        raise UpdateError("SHA-256 mismatch for {}".format(destination.name))
    return actual


def safe_unlink(path: Path) -> None:
    try:
        path.unlink()
    except FileNotFoundError:
        pass


def atomic_current(data_dir: Path, target: str) -> None:
    temporary = data_dir / ".current-{}".format(os.getpid())
    safe_unlink(temporary)
    temporary.symlink_to(target)
    os.replace(temporary, data_dir / "current")


def run_reload(command: str, data_dir: Path, version: str, timeout: int) -> None:
    if not command:
        return
    env = os.environ.copy()
    env.update({"IP2REGION_CURRENT": str(data_dir / "current"), "IP2REGION_VERSION": version})
    try:
        subprocess.run(shlex.split(command), check=True, timeout=timeout, env=env)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError) as exc:
        raise UpdateError("reload command failed: {}".format(exc)) from exc


def update(data_dir: Path, timeout: int, reload_command: str, reload_timeout: int) -> bool:
    data_dir = data_dir.expanduser().resolve()
    releases_dir = data_dir / "releases"
    staging_dir = data_dir / "staging"
    data_dir.mkdir(parents=True, exist_ok=True)
    releases_dir.mkdir(exist_ok=True)
    staging_dir.mkdir(exist_ok=True)

    lock_path = data_dir / ".updater.lock"
    with lock_path.open("a") as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            logging.info("another update process is running; exiting")
            return False

        release = read_release(timeout)
        version = release["version"]
        current = data_dir / "current"
        old_target = os.readlink(current) if current.is_symlink() else None
        if old_target == "releases/{}".format(version):
            logging.info("already at latest version %s", version)
            return False

        final_dir = releases_dir / version
        if final_dir.exists():
            raise UpdateError("release directory already exists but is not active: {}".format(final_dir))

        stage = Path(tempfile.mkdtemp(prefix="{}-".format(version), dir=staging_dir))
        try:
            hashes = {}
            for name in ASSETS:
                logging.info("downloading %s", name)
                hashes[name] = download_asset(release["assets"][name], stage / name, timeout)

            manifest = {
                "version": version,
                "source": "fa1seut0pia/ip2region-xdb",
                "assets": {
                    name: {"size": release["assets"][name]["size"], "sha256": hashes[name]}
                    for name in ASSETS
                },
            }
            manifest_path = stage / "manifest.json"
            with manifest_path.open("x", encoding="utf-8") as output:
                json.dump(manifest, output, indent=2)
                output.write("\n")
                output.flush()
                os.fsync(output.fileno())

            os.replace(stage, final_dir)
            previous_target = old_target
            atomic_current(data_dir, "releases/{}".format(version))
            try:
                run_reload(reload_command, data_dir, version, reload_timeout)
            except UpdateError:
                if previous_target:
                    atomic_current(data_dir, previous_target)
                else:
                    safe_unlink(data_dir / "current")
                shutil.rmtree(final_dir, ignore_errors=True)
                raise

            logging.info("activated ip2region release %s", version)
            return True
        finally:
            if stage.exists():
                shutil.rmtree(stage, ignore_errors=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data-dir", default=os.getenv("IP2REGION_DATA_DIR", "/var/lib/ip2region"))
    parser.add_argument("--timeout", type=int, default=int(os.getenv("IP2REGION_HTTP_TIMEOUT", "60")))
    parser.add_argument("--reload-command", default=os.getenv("IP2REGION_RELOAD_COMMAND", ""))
    parser.add_argument("--reload-timeout", type=int, default=int(os.getenv("IP2REGION_RELOAD_TIMEOUT", "30")))
    args = parser.parse_args()
    if args.timeout <= 0 or args.reload_timeout <= 0:
        parser.error("timeouts must be positive")

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    try:
        update(Path(args.data_dir), args.timeout, args.reload_command, args.reload_timeout)
    except UpdateError as exc:
        logging.error("update failed: %s", exc)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
