#!/usr/bin/env python3
"""Create a Sub2API v1 .s2plugin ZIP with complete file hashes."""
import argparse, base64, hashlib, json, os, subprocess, tempfile, zipfile
from pathlib import Path

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--source', type=Path)
    parser.add_argument('--version', default=os.environ.get('PLUGIN_VERSION', '0.2.0'))
    parser.add_argument('--signing-key', type=Path)
    parser.add_argument('--key-id', default=os.environ.get('PLUGIN_SIGNING_KEY_ID', ''))
    args = parser.parse_args()
    root, out = args.root.resolve(), args.out.resolve()
    binaries = {
        'linux-amd64': 'runtimes/linux-amd64/sub2api-plugin-zstd',
        'linux-arm64': 'runtimes/linux-arm64/sub2api-plugin-zstd',
        'windows-amd64': 'runtimes/windows-amd64/sub2api-plugin-zstd.exe',
    }
    files = {path: hashlib.sha256((root / path).read_bytes()).hexdigest() for path in [*binaries.values(), 'ui/index.html']}
    source_path = args.source or (root / 'manifest.source.json')
    manifest = json.loads(source_path.read_text(encoding='utf-8'))
    manifest['version'] = args.version
    manifest['runtimes'] = {key: {'path': path} for key, path in binaries.items()}
    manifest['files'] = files
    manifest_bytes = (json.dumps(manifest, separators=(',', ':'), ensure_ascii=False) + '\n').encode()
    signature = None
    env_key = Path(os.environ['PLUGIN_SIGNING_KEY_FILE']) if os.environ.get('PLUGIN_SIGNING_KEY_FILE') else None
    signing_key = args.signing_key or (env_key if env_key and env_key.exists() else None)
    if signing_key:
        if not args.key_id:
            raise SystemExit('--key-id or PLUGIN_SIGNING_KEY_ID is required when signing')
        with tempfile.NamedTemporaryFile() as manifest_file, tempfile.NamedTemporaryFile() as signed:
            manifest_file.write(manifest_bytes)
            manifest_file.flush()
            subprocess.run(['openssl', 'pkeyutl', '-sign', '-rawin', '-inkey', str(signing_key), '-in', manifest_file.name, '-out', signed.name], check=True)
            signature = (json.dumps({'algorithm': 'ed25519', 'key_id': args.key_id, 'signature': base64.b64encode(Path(signed.name).read_bytes()).decode()}, separators=(',', ':')) + '\n').encode()
            (root / 'signature.json').write_bytes(signature)
    out.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(out, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr('manifest.json', manifest_bytes)
        if signature: archive.writestr('signature.json', signature)
        for path in sorted(files):
            info = zipfile.ZipInfo(path)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = ((0o755 if path.startswith('runtimes/') else 0o644) & 0xffff) << 16
            archive.writestr(info, (root / path).read_bytes())
    print(out)

if __name__ == '__main__': main()
