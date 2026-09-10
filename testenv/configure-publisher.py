#!/usr/bin/env python3
"""Add the trusted plugin publisher to the test instance config."""
import pathlib
import sys

try:
    import yaml
except ImportError:
    raise SystemExit("PyYAML is required: python3 -m pip install pyyaml")

if len(sys.argv) != 2 or not sys.argv[1].strip():
    raise SystemExit("usage: configure-publisher.py BASE64_ED25519_PUBLIC_KEY")

path = pathlib.Path(__file__).with_name("data") / "config.yaml"
if not path.exists():
    raise SystemExit(f"config not found: {path}; start the test environment first")
config = yaml.safe_load(path.read_text()) or {}
plugins = config.setdefault("plugins", {})
publishers = plugins.setdefault("trusted_publishers", {})
publishers["milesians"] = sys.argv[1].strip()
path.write_text(yaml.safe_dump(config, allow_unicode=True, sort_keys=False))
print(f"configured milesians in {path}")

