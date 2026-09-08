#!/usr/bin/env python3
import hashlib, json, sys, zipfile
from pathlib import PurePosixPath

def main(path):
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)): raise SystemExit('duplicate ZIP path')
        if any(PurePosixPath(name).is_absolute() or '..' in PurePosixPath(name).parts or '\\' in name for name in names): raise SystemExit('unsafe ZIP path')
        manifest = json.loads(archive.read('manifest.json'))
        assert manifest['schema_version'] == 1
        assert manifest['requires']['plugin_protocol'] == 1 and manifest['requires']['transport_api'] == 1 and manifest['requires']['ui_bridge'] == 1
        files = manifest['files']
        for path, expected in files.items():
            actual = hashlib.sha256(archive.read(path)).hexdigest()
            assert actual == expected, f'hash mismatch: {path}'
        assert manifest['ui']['entrypoint'] in files
        for runtime in manifest['runtimes'].values(): assert runtime['path'] in files
        assert set(names) == set(files) | {'manifest.json'} | ({'signature.json'} if 'signature.json' in names else set())
        if 'signature.json' in names:
            signature = json.loads(archive.read('signature.json'))
            assert signature['algorithm'] == 'ed25519' and signature['key_id'] and signature['signature']
    print(f'valid {path}: {manifest["id"]} {manifest["version"]}, {len(files)} files')

if __name__ == '__main__':
    if len(sys.argv) != 2: raise SystemExit('usage: verify_package.py FILE')
    main(sys.argv[1])
