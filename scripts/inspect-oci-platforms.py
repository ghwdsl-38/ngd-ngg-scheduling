#!/usr/bin/env python3
"""Validate that a Buildx OCI archive contains linux/amd64 and linux/arm64."""

import argparse
import hashlib
import json
import tarfile


def load_blob(archive, descriptor):
    digest = descriptor["digest"]
    algorithm, value = digest.split(":", 1)
    member = archive.extractfile(f"blobs/{algorithm}/{value}")
    if member is None:
        raise ValueError(f"missing OCI blob {digest}")
    payload = member.read()
    if hashlib.new(algorithm, payload).hexdigest() != value:
        raise ValueError(f"digest mismatch for {digest}")
    return json.loads(payload)


def platforms(path):
    with tarfile.open(path, "r:*") as archive:
        index_member = archive.extractfile("index.json")
        if index_member is None:
            raise ValueError("missing index.json")
        root = json.load(index_member)
        found = set()
        pending = list(root.get("manifests", []))
        while pending:
            descriptor = pending.pop()
            platform = descriptor.get("platform", {})
            if platform.get("os") and platform.get("architecture"):
                found.add((platform["os"], platform["architecture"]))
            media_type = descriptor.get("mediaType", "")
            if "index" in media_type or "manifest.list" in media_type:
                pending.extend(load_blob(archive, descriptor).get("manifests", []))
        return found


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("archives", nargs="+")
    args = parser.parse_args()
    required = {("linux", "amd64"), ("linux", "arm64")}
    for path in args.archives:
        actual = platforms(path)
        missing = required - actual
        if missing:
            raise SystemExit(f"{path}: missing platforms {sorted(missing)}; actual={sorted(actual)}")
        formatted = ",".join(f"{os_}/{arch}" for os_, arch in sorted(actual))
        print(f"{path}: platforms={formatted}")


if __name__ == "__main__":
    main()
