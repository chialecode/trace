"""Crop the user-provided 1536x1024 design into local, untracked preview assets.

Requires Pillow. Never writes inside the repository or alters the source image.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--reference-dir', type=Path, required=True)
    args = parser.parse_args()
    from PIL import Image
    root = Path(__file__).resolve().parents[1]
    target = (args.reference_dir / 'design').resolve()
    if target.is_relative_to(root):
        parser.error('Reference assets must stay outside the repository')
    source = args.source.resolve()
    with Image.open(source) as img:
        if img.size != (1536, 1024):
            parser.error('Expected the original 1536x1024 reference image')
        target.mkdir(parents=True, exist_ok=True)
        original = target / 'trace-white.png'
        digest = hashlib.sha256(source.read_bytes()).hexdigest()
        if original.exists() and hashlib.sha256(original.read_bytes()).hexdigest() != digest:
            parser.error('A different reference already exists; refusing to overwrite')
        if source != original:
            shutil.copyfile(source, original)
        # Artwork only; the interface is implemented with Vue and CSS.
        crops = {
            'portrait': (299, 87, 401, 193),
            'banner': (722, 61, 1129, 164),
            'feature': (1168, 119, 1511, 273),
            'city': (870, 616, 1097, 714),
            'character': (864, 796, 1090, 890),
            'sea': (863, 916, 1090, 991),
        }
        for name, box in crops.items():
            img.crop(box).convert('RGB').save(target / f'{name}.webp', quality=94)
        (target / 'manifest.json').write_text(json.dumps({
            'source_sha256': digest, 'size': list(img.size), 'crops': crops,
            'usage': 'local UI preview only; no redistribution',
        }, indent=2) + '\n', encoding='utf-8')
    print('Prepared 6 local preview crops; source preserved; repository unchanged.')


if __name__ == '__main__':
    main()
