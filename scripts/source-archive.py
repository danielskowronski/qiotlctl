#!/usr/bin/env python3
"""Create a deterministic, explicitly allowlisted source snapshot for release packaging."""
import gzip
import hashlib
import io
from pathlib import Path
import sys
import tarfile

ROOTS = (
    'LICENSE', 'README.md', 'CHANGELOG.md', 'Dockerfile', 'Makefile', 'go.mod',
    'go.sum', '.gitignore', '.dockerignore', 'compose.dev.yaml', 'cmd', 'internal',
    'scripts', 'tools', 'docs', 'examples', 'charts', 'testdata', 'third_party',
)


def archive(destination, version):
    files = []
    for root in ROOTS:
        path = Path(root)
        if not path.exists():
            raise SystemExit(f'missing release source: {root}')
        for item in ([path] if path.is_file() else sorted(path.rglob('*'))):
            if item.is_symlink():
                raise SystemExit(f'symlink in release source: {item}')
            if item.is_file():
                if any(part.startswith('.') and part not in ('.gitignore', '.dockerignore', '.helmignore') for part in item.parts) or item.name.endswith(('.sqlite', '.sqlite3', '.pem', '.key', '.tgz', '.pyc')):
                    raise SystemExit(f'unexpected private/generated file in release source: {item}')
                files.append(item)
    with Path(destination).open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for path in sorted(files):
                data = path.read_bytes()
                info = tarfile.TarInfo(f'qiotlctl-{version}/{path.as_posix()}')
                info.size = len(data)
                info.mode = 0o755 if path.stat().st_mode & 0o111 else 0o644
                tar.addfile(info, io.BytesIO(data))
    print(hashlib.sha256(Path(destination).read_bytes()).hexdigest(), Path(destination).name, sep='  ')


if __name__ == '__main__':
    archive(*sys.argv[1:])
