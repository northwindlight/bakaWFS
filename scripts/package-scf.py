#!/usr/bin/env python3
"""Build explicit-file SCF ZIPs; never includes local config or manga files."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def write_zip(path, filename, data, mode):
    info = zipfile.ZipInfo(filename, date_time=(2026, 1, 1, 0, 0, 0))
    info.create_system = 3
    info.external_attr = mode << 16
    with zipfile.ZipFile(path, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=ROOT / 'dist' / 'scf')
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64')
    packages = []
    with tempfile.TemporaryDirectory(prefix='bakawfs-scf-') as temporary:
        binary = Path(temporary) / 'main'
        for name, target in (('bakawfs-scf', './cmd/scf'), ('go-probe', './cmd/scf-probe')):
            subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', str(binary), target], cwd=ROOT, env=env, check=True)
            path = output / (name + '.zip')
            write_zip(path, 'main', binary.read_bytes(), 0o100755)
            packages.append({'file': path.name, 'runtime': 'Go1', 'handler': 'main', 'architecture': 'x86_64'})
    path = output / 'node-probe.zip'
    write_zip(path, 'index.js', (ROOT / 'tools/scf/node-probe/index.js').read_bytes(), 0o100644)
    packages.append({'file': path.name, 'runtime': 'Nodejs20.19', 'handler': 'index.main_handler', 'architecture': 'x86_64'})
    for package in packages:
        data = (output / package['file']).read_bytes()
        package.update(bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
    manifest = {'git_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                'working_tree_dirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT)),
                'packages': packages}
    (output / 'manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
