#!/usr/bin/env python3
"""Check the deployed custom dashboard before reloading the production edge."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/home/user1/dingjx/newapi'))
    parser.add_argument('--config', type=Path, help='Override config for offline validation')
    parser.add_argument('--live', action='store_true', help='Also check the local HTTPS ingress')
    args = parser.parse_args()
    edge = args.root / 'edge'
    manifest = json.loads((edge / 'frontend-release.json').read_text())
    config = args.config or edge / 'nginx.conf'
    if not re.search(r'^\s*include\s+/etc/nginx/edge/frontend\.conf\s*;', config.read_text(), re.M):
        raise RuntimeError('Dashboard include is missing; refusing to reload an old configuration')
    release = (edge / 'frontend-current').resolve(strict=True)
    if release != (edge / manifest['release']).resolve(strict=True):
        raise RuntimeError('Active dashboard differs from the approved release manifest')
    for relative, expected in manifest['sha256'].items():
        actual = hashlib.sha256((release / relative).read_bytes()).hexdigest()
        if actual != expected:
            raise RuntimeError('Release file changed: ' + relative)
    javascript = b'\n'.join((release / p).read_bytes() for p in manifest['sha256'] if p.endswith('.js'))
    for marker in manifest['required_markers']:
        if marker.encode() not in javascript:
            raise RuntimeError('Custom channel category functionality missing: ' + marker)
    if args.live:
        for route in ['/', '/channels']:
            html = subprocess.check_output([
                'curl', '--fail', '--silent', '--show-error', '--noproxy', '*',
                '--resolve', 'nicolessss.com:443:127.0.0.1', '--max-time', '20',
                'https://nicolessss.com' + route,
            ])
            if manifest['entry'].encode() not in html:
                raise RuntimeError('Live page is serving another release: ' + route)
        for asset in manifest['verify_assets']:
            data = subprocess.check_output([
                'curl', '--fail', '--silent', '--show-error', '--noproxy', '*',
                '--resolve', 'nicolessss.com:443:127.0.0.1', '--max-time', '20',
                'https://nicolessss.com/' + asset,
            ])
            if hashlib.sha256(data).hexdigest() != manifest['sha256'][asset]:
                raise RuntimeError('Live asset differs from the approved release: ' + asset)
    print('PASS: custom dashboard ' + manifest['release'] + (' (live verified)' if args.live else ''))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print('FAIL: ' + str(error), file=sys.stderr)
        sys.exit(1)
