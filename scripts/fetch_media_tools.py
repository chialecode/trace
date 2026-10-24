"""Fetch the pinned YouTube media tools (yt-dlp, Deno) into target/tools, verifying before use.

Every download is checked against the SHA-256 in media-tools.lock.json before anything is
extracted or executed; the receipt written last (verified.json) is what Core checks again at run
time. Nothing here updates the tools: a new version means a new lock entry.
"""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent
DEST = ROOT / 'target' / 'tools'
LOCK = ROOT / 'media-tools.lock.json'
LIMIT = 256 * 1024 * 1024


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def fetch(url, path, sha256, size=None):
    """Downloads `url` to `path` unless a file with the pinned digest is already there."""
    if path.exists() and digest(path) == sha256:
        return
    if not url.startswith('https://'):
        raise ValueError('tool downloads must use https')
    partial = path.with_name(path.name + '.part')
    with urllib.request.urlopen(url, timeout=60) as response, partial.open('wb') as output:
        copied = 0
        while chunk := response.read(256 * 1024):
            copied += len(chunk)
            if copied > LIMIT or (size is not None and copied > size):
                raise ValueError('tool download is larger than the pinned size')
            output.write(chunk)
    if digest(partial) != sha256:
        partial.unlink()
        raise ValueError(f'checksum mismatch for {path.name}')
    partial.replace(path)


def run(command, **options):
    return subprocess.run(command, capture_output=True, check=True, timeout=30, **options)


def main():
    lock = json.loads(LOCK.read_text())
    DEST.mkdir(parents=True, exist_ok=True)
    downloads = DEST / 'downloads'
    downloads.mkdir(exist_ok=True)

    yt = lock['yt_dlp']
    fetch(yt['url'], DEST / 'yt-dlp.exe', yt['sha256'], yt['size'])

    deno = lock['deno']
    archive = downloads / 'deno.zip'
    fetch(deno['url'], archive, deno['sha256'], deno['size'])
    with zipfile.ZipFile(archive) as package:
        names = package.namelist()
        if names != [deno['member']]:
            raise ValueError('unexpected Deno archive members')
        info = package.getinfo(deno['member'])
        if (info.external_attr >> 16) & 0o170000 == 0o120000 or info.file_size > LIMIT:
            raise ValueError('unsafe Deno archive member')
        with package.open(info) as source, (DEST / deno['member']).open('wb') as target:
            shutil.copyfileobj(source, target, 256 * 1024)
    if digest(DEST / deno['member']) != deno['member_sha256']:
        raise ValueError('Deno executable differs from the pinned file')

    for tool in (yt, deno):
        for notice in tool['notices']:
            fetch(notice['url'], DEST / notice['name'], notice['sha256'])

    version_out = run([str(DEST / 'yt-dlp.exe'), '--version']).stdout.decode().strip()
    if version_out != yt['version']:
        raise ValueError(f'yt-dlp reports {version_out}, expected {yt["version"]}')
    deno_out = run([str(DEST / 'deno.exe'), '--version'], env={'DENO_NO_UPDATE_CHECK': '1'}).stdout.decode()
    if f"deno {deno['version']}" not in deno_out.splitlines()[0]:
        raise ValueError('Deno reports an unexpected version')
    (DEST / 'SOURCE.txt').write_text(
        '\n'.join([yt['source'], yt['url'], deno['source'], deno['url']]) + '\n', encoding='utf-8')
    files = {f.name: digest(f) for f in DEST.iterdir() if f.is_file() and f.suffix in ('.exe', '.txt', '.md')}
    (DEST / 'verified.json').write_text(
        json.dumps({'lock': lock, 'files': files}, indent=2, sort_keys=True), encoding='utf-8')
    print('PASS: pinned yt-dlp and Deno verified in target/tools')


if __name__ == '__main__':
    main()
