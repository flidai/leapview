#!/usr/bin/env python3
"""Resolve an exact public server release, or the latest publication for monitoring."""

import argparse
from datetime import datetime
import json
import os
import re
from urllib.parse import quote
from urllib.request import Request, urlopen

SERVER_TAG = re.compile(
    r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)'
    r'(?:-(?P<prerelease>[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?'
    r'(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?')


def valid_tag(tag):
    match = SERVER_TAG.fullmatch(tag)
    if not match:
        return False
    return all(not (part.isdigit() and len(part) > 1 and part.startswith('0'))
               for part in (match.group('prerelease') or '').split('.'))


def public_server(release):
    return (not release['draft'] and release.get('published_at') and
            valid_tag(release['tag_name']))


def resolve(requested, fetch):
    # None means scheduled monitoring. An empty explicit input is an error.
    if requested is not None:
        if not valid_tag(requested):
            raise ValueError(f'invalid server release tag: {requested!r}')
        release = fetch('/releases/tags/' + quote(requested, safe=''))
        if not public_server(release) or release['tag_name'] != requested:
            raise ValueError(f'requested release is not publicly available: {requested}')
        return requested
    candidates = []
    page = 1
    while True:
        releases = fetch(f'/releases?per_page=100&page={page}')
        candidates.extend(release for release in releases if public_server(release))
        if len(releases) < 100:
            break
        page += 1
    if not candidates:
        raise ValueError('scheduled-resolution: no published server releases')
    # GitHub lists releases by creation, which can differ from publication.
    latest = max(candidates, key=lambda r: (datetime.fromisoformat(
        r['published_at'].replace('Z', '+00:00')), r['id']))
    return latest['tag_name']


def fetch(path):
    headers = {'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'}
    if os.environ.get('GH_TOKEN'):
        headers['Authorization'] = 'Bearer ' + os.environ['GH_TOKEN']
    request = Request('https://api.github.com/repos/flidai/leapview' + path, headers=headers)
    with urlopen(request, timeout=30) as response:
        return json.load(response)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument('--tag')
    selection.add_argument('--scheduled', action='store_true')
    args = parser.parse_args()
    print(resolve(None if args.scheduled else args.tag, fetch))
