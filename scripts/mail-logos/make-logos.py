#!/usr/bin/env python3
"""Regenerate assets/mail/images from the Vocdoni logo in vocdoni-app.

Reads the ten SVG paths of src/components/Layout/Logo.tsx at a git ref of a vocdoni-app checkout,
writes the wordmark and the mark in black, white and black with a white halo (legible when a mail
client forces a dark background without inverting images), and rasterizes each at 2x and 3x of
the size the emails show (wordmark 120px wide, mark 40px wide) with rsvg-convert.

Not run at build time. If the artwork changes, give the PNGs new file names: mail clients and
proxies cache /mail/assets/{file} for a day.

    scripts/mail-logos/make-logos.py --app ~/Repos/vocdoni-app --ref feat/fil-voter-flow \
        --out assets/mail/images
"""
import argparse
import pathlib
import re
import subprocess
import tempfile

WIDTHS = {'wordmark': 120, 'mark': 40}
VIEWBOXES = {'wordmark': (0, 0, 1000.4, 215.5), 'mark': (0, 0, 251.8, 215.5)}
TONES = [('black', '#000000', None), ('white', '#ffffff', None), ('black-halo', '#000000', '#ffffff')]


def svg(paths, viewbox, fill, stroke=None):
    attrs = f' stroke="{stroke}" stroke-width="22" stroke-linejoin="round" paint-order="stroke"' if stroke else ''
    pad = 12 if stroke else 0
    x, y, w, h = viewbox
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{x - pad} {y - pad} {w + 2 * pad} {h + 2 * pad}">'
            + ''.join(f'<path d="{p}" fill="{fill}"{attrs}/>' for p in paths) + '</svg>')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--app', required=True, help='path to a vocdoni-app checkout')
    parser.add_argument('--ref', default='feat/fil-voter-flow', help='git ref to read Logo.tsx from')
    parser.add_argument('--out', required=True, help='output directory, normally assets/mail/images')
    args = parser.parse_args()

    source = subprocess.run(['git', '-C', args.app, 'show', f'{args.ref}:src/components/Layout/Logo.tsx'],
                            capture_output=True, text=True, check=True).stdout
    paths = re.findall(r"\sd='([^']+)'", source)
    if len(paths) != 10:
        raise SystemExit(f'expected 10 paths in Logo.tsx, found {len(paths)}')
    shapes = {'wordmark': paths, 'mark': paths[:2]}
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    (pathlib.Path.home() / '.cache' / 'saas-backend').mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=pathlib.Path.home() / '.cache' / 'saas-backend') as tmp:
        for name, shape in shapes.items():
            for tone, fill, stroke in TONES:
                source_svg = pathlib.Path(tmp) / f'vocdoni-{name}-{tone}.svg'
                source_svg.write_text(svg(shape, VIEWBOXES[name], fill, stroke))
                for scale in (2, 3):
                    png = out / f'vocdoni-{name}-{tone}@{scale}x.png'
                    subprocess.run(['rsvg-convert', '-w', str(WIDTHS[name] * scale), '-o', str(png), str(source_svg)],
                                   check=True)
                    print(f'wrote {png}')


if __name__ == '__main__':
    main()
