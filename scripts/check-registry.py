#!/usr/bin/env python3
"""Verify registry.json is internally consistent with VERSION.

The plugin store reads `version` from registry.json as the display fallback
whenever the GitHub API query fails or is rate-limited, so a stale value here
is a user-visible bug (0.9.2 kept showing for four releases because of it).
"""
import json
import pathlib
import re
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

# main.go's version const is what `make build` stamps into a local binary (CI
# overrides it with -ldflags -X main.version=<tag>). Letting it drift means a
# hand-built plugin reports a version that is not the one in VERSION.
# The version constant can live in any source file (it moved from main.go to
# registration.go in the v0.15 split), so scan them all rather than pin a path
# that will rot again on the next move.
found = {}
for src in sorted(root.glob("*.go")):
    if src.name.endswith("_test.go"):
        continue
    m = re.search(r'^\s*version\s*=\s*"([^"]+)"', src.read_text(encoding="utf-8"), re.M)
    if m:
        found[src.name] = m.group(1)
if not found:
    sys.exit("no version constant found in any *.go")
if len(found) > 1:
    sys.exit(f"version constant declared in more than one file: {found}")
src_name, ver = next(iter(found.items()))
if ver != version:
    sys.exit(f"{src_name} version {ver} != VERSION {version}")

print(f"registry.json ok: {plugins[0]['id']} v{version} ({src_name} agrees)")
