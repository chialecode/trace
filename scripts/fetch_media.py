"""Fetch the pinned local development backend, verifying before extraction/execution."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent
DEST = ROOT / 'target' / 'media'


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    lock = json.loads((ROOT / 'media-backend.lock.json').read_text())
    DEST.mkdir(parents=True, exist_ok=True)
    archive = DEST / 'backend.zip'
    if not archive.exists() or digest(archive) != lock['sha256']:
        with urllib.request.urlopen(lock['url'], timeout=60) as response, archive.open('wb') as output:
            shutil.copyfileobj(response, output, 256 * 1024)
    if digest(archive) != lock['sha256']:
        raise ValueError('backend checksum mismatch')
    with zipfile.ZipFile(archive) as package:
        for item in package.infolist():
            target = DEST / item.filename
            if not target.resolve().is_relative_to(DEST.resolve()) or (item.external_attr >> 16) & 0o170000 == 0o120000:
                raise ValueError('unsafe backend archive member')
        package.extractall(DEST)
    binaries = list(DEST.glob('*/bin/ffmpeg.exe'))
    if len(binaries) != 1:
        raise ValueError('expected one backend directory')
    source = binaries[0].parent
    logs = {}
    for name in ('ffmpeg', 'ffprobe'):
        output = subprocess.run([str(source / (name + '.exe')), '-version'], capture_output=True, check=True, timeout=15).stdout.decode()
        conf = subprocess.run([str(source / (name + '.exe')), '-buildconf'], capture_output=True, check=True, timeout=15)
        license_output = subprocess.run([str(source / (name + '.exe')), '-L'], capture_output=True, check=True, timeout=15).stdout.decode()
        logs[name] = output + conf.stdout.decode() + conf.stderr.decode() + license_output
        if lock['license'] != 'LGPL-3.0-or-later' or 'either version 3' not in license_output:
            raise ValueError('unexpected LGPL version')
        if lock['version'] not in output.splitlines()[0] or any(flag in logs[name] for flag in ('--enable-gpl', '--enable-nonfree')):
            raise ValueError('unexpected backend version/license')
    for file in source.iterdir():
        if file.is_file():
            shutil.copy2(file, DEST / file.name)
    notices = source.parent / 'LICENSE.txt'
    if not notices.exists():
        notices = source.parent / 'LICENSE'
    if not notices.exists():
        raise ValueError('backend license missing')
    shutil.copy2(notices, DEST / 'LICENSE.txt')
    (DEST / 'SOURCE.txt').write_text(lock['source'] + '\n' + lock['url'] + '\n', encoding='utf-8')
    (DEST / 'verified.json').write_text(json.dumps({'lock': lock, 'logs': logs, 'files': {f.name: digest(f) for f in DEST.iterdir() if f.is_file() and f.suffix in ('.exe', '.dll')}}, indent=2), encoding='utf-8')
    print('PASS: pinned LGPL shared backend verified in target/media')


if __name__ == '__main__':
    main()
