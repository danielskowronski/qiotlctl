"""Publication guard and source-isolation regressions. Never invokes a push."""
import contextlib
import io
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]


class ReleaseTest(unittest.TestCase):
    def test_source_allowlist(self):
        spec = importlib.util.spec_from_file_location('source_archive', REPO / 'scripts/source-archive.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in module.ROOTS:
                (root / name).write_text('public fixture')
            for name in ('live-access', '.local', 'history', 'dist'):
                (root / name).mkdir()
                (root / name / 'private.txt').write_text('PRIVATE-SENTINEL')
            previous = Path.cwd()
            try:
                os.chdir(root)
                checksum = io.StringIO()
                with contextlib.redirect_stdout(checksum):
                    module.archive(root / 'snapshot.tar.gz', 'test')
                (root / 'SOURCE.sha256').write_text(checksum.getvalue())
                subprocess.run(['shasum', '-a', '256', '-c', 'SOURCE.sha256'], check=True, capture_output=True)
                module.archive(root / 'snapshot-again.tar.gz', 'test')
                self.assertEqual((root / 'snapshot.tar.gz').read_bytes(), (root / 'snapshot-again.tar.gz').read_bytes())
                with tarfile.open(root / 'snapshot.tar.gz') as archive:
                    self.assertEqual(len(archive.getnames()), len(module.ROOTS))
                    for item in archive:
                        self.assertNotIn(b'PRIVATE-SENTINEL', archive.extractfile(item).read())
                (root / 'cmd').unlink()
                (root / 'cmd').symlink_to(root / '.local/private.txt')
                with self.assertRaises(SystemExit):
                    module.archive(root / 'bad.tar.gz', 'test')
            finally:
                os.chdir(previous)

    def test_publication_guard(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            (root / 'charts/qiotlctl').mkdir(parents=True)
            shutil.copyfile(REPO / 'scripts/release-check.sh', root / 'scripts/release-check.sh')
            (root / 'charts/qiotlctl/Chart.yaml').write_text('---\nversion: 0.2.0\nappVersion: "0.2.0"\n')
            for name in ('LICENSE', 'charts/qiotlctl/LICENSE'):
                (root / name).write_text('fixture license')
            def git(*args):
                subprocess.run(['git', '-c', 'commit.gpgsign=false', '-c', 'tag.gpgsign=false', '-c', 'core.hooksPath=/dev/null', *args], cwd=root, check=True, capture_output=True)
            def check(version='0.2.0'):
                env = dict(os.environ, VERSION=version, APP_VERSION=version, CHART_VERSION='0.2.0')
                return subprocess.run(['bash', 'scripts/release-check.sh'], cwd=root, env=env, capture_output=True).returncode
            git('init', '--quiet')
            git('add', '.')
            git('-c', 'user.name=Release test', '-c', 'user.email=test@example.invalid', 'commit', '--quiet', '-m', 'fixture')
            self.assertNotEqual(check(), 0)  # missing explicit local tag
            git('tag', 'v0.2.0')
            self.assertEqual(check(), 0)
            self.assertNotEqual(check('0.3.0'), 0)
            (root / 'uncommitted.go').write_text('package fixture')
            self.assertNotEqual(check(), 0)


if __name__ == '__main__':
    unittest.main()
