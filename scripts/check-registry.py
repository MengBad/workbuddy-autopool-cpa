#!/usr/bin/env python3
"""Verify registry.json is internally consistent with VERSION.

The plugin store reads `version` from registry.json as the display fallback
whenever the GitHub API query fails or is rate-limited, so a stale value here
is a user-visible bug (0.9.2 kept showing for four releases because of it).
"""
import json
import pathlib
import sys

root = pathlib.Path(__file__).resolve().parent.parent
data = json.loads((root / "registry.json").read_text(encoding="utf-8"))

if data.get("schema_version") != 2:
    sys.exit(f"schema_version must be 2, got {data.get('schema_version')!r}")

plugins = data.get("plugins") or []
if not plugins:
    sys.exit("registry.json has no plugins")

version = (root / "VERSION").read_text(encoding="utf-8").strip()

for p in plugins:
    for key in ("id", "name", "version", "release_tag", "repository", "license"):
        if not p.get(key):
            sys.exit(f"plugin {p.get('id')!r}: missing {key}")
    if p["version"] != version:
        sys.exit(f"plugin {p['id']!r}: version {p['version']} != VERSION {version}")
    if p["release_tag"] != f"v{version}":
        sys.exit(f"plugin {p['id']!r}: release_tag {p['release_tag']} != v{version}")
    if p.get("install", {}).get("type") != "github-release":
        sys.exit(f"plugin {p['id']!r}: install.type must be github-release")

print(f"registry.json ok: {plugins[0]['id']} v{version}")
