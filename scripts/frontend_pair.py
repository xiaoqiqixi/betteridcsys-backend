#!/usr/bin/env python3
"""Validate the pinned frontend checkout used by paired CI, not a release.

No network, build, extraction, installation or source fallback. The workflow
fetches official repository metadata and checks out the fixed source. Report
both independent source SHAs; never pretend the frontend shares the backend SHA.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
FRONTEND = 'xiaoqiqixi/betteridcsys-frontend'
FRONTEND_ID = 1435344023
BACKEND = 'xiaoqiqixi/betteridcsys-backend'
OWNER_ID = 123864154
SHA = r'[0-9a-f]{40}'
VERSION = r'(?:0|[1-9][0-9]{0,5})(?:\.(?:0|[1-9][0-9]{0,5})){2}'


class Rejected(ValueError):
    """Only fixed, nonsecret diagnostic codes authored in this policy."""


def require(condition, code):
    if not condition:
        raise Rejected(code)


def regular(path, limit=65536):
    require(path.resolve(strict=True) == path, 'linked_input_refused')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and 0 < info.st_size <= limit, 'bounded_regular_input_required')
        raw = stream.read(limit + 1)
    require(0 < len(raw) <= limit, 'input_size_limit')
    return raw


def unique(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, 'duplicate_json_field')
        value[key] = item
    return value


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError('nonfinite_json_value')))


def load_pin():
    pin = decode(regular(ROOT / 'FRONTEND_PAIR.json'))
    require(isinstance(pin, dict) and set(pin) == {'schema', 'repository', 'repository_id', 'source_sha',
            'frontend_version', 'core_version', 'compatibility_sha256'}, 'pin_schema_mismatch')
    require(type(pin['schema']) is int and pin['schema'] == 1 and type(pin['repository_id']) is int and
            pin['repository_id'] == FRONTEND_ID and pin['repository'] == FRONTEND, 'fixed_frontend_identity_required')
    for key, pattern in (('source_sha', SHA), ('frontend_version', VERSION), ('core_version', VERSION),
                         ('compatibility_sha256', r'[0-9a-f]{64}')):
        require(isinstance(pin[key], str) and re.fullmatch(pattern, pin[key]), 'invalid_pin_' + key)
    source = regular(ROOT / 'vps-billing/backend/pkg/pluginapi/protocol.go').decode('utf-8')
    versions = re.findall(r'^\s*CoreVersion\s*=\s*"([0-9.]+)"\s*$', source, re.M)
    require(versions == [pin['core_version']], 'backend_core_version_mismatch')
    return pin


def git(root, *args):
    env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
    env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null', GIT_TERMINAL_PROMPT='0')
    result = subprocess.run(['git', '--no-replace-objects', '-c', 'core.fsmonitor=false',
                             '-c', 'core.hooksPath=/dev/null', '-C', str(root), *args],
                            env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=20)
    require(result.returncode == 0 and len(result.stdout) <= 65536, 'git_source_check_failed')
    return result.stdout.decode('utf-8').strip()


def verify(pin, metadata_path, backend_source):
    require(isinstance(backend_source, str) and re.fullmatch(SHA, backend_source), 'backend_sha_required')
    metadata = decode(regular(metadata_path))
    require(isinstance(metadata, dict) and type(metadata.get('id')) is int and metadata['id'] == FRONTEND_ID and
            metadata.get('full_name') == FRONTEND and metadata.get('private') is False and
            metadata.get('default_branch') == 'main' and isinstance(metadata.get('owner'), dict) and
            type(metadata['owner'].get('id')) is int and metadata['owner']['id'] == OWNER_ID, 'official_repository_identity_mismatch')
    frontend = ROOT / 'vps-billing/frontend'
    require(frontend.resolve(strict=True) == frontend and frontend.is_dir(), 'canonical_frontend_checkout_required')
    for root, expected in ((ROOT, backend_source), (frontend, pin['source_sha'])):
        require(git(root, 'rev-parse', '--show-toplevel') == str(root), 'separate_git_checkout_required')
        require(git(root, 'rev-parse', '--verify', 'HEAD^{commit}') == expected, 'checkout_source_mismatch')
        require(not git(root, 'diff', '--name-only', 'HEAD', '--'), 'tracked_source_modified')
    require(not git(frontend, 'status', '--porcelain=v1', '--untracked-files=all'), 'frontend_checkout_not_clean')
    raw = regular(frontend / 'compatibility.json')
    require(hashlib.sha256(raw).hexdigest() == pin['compatibility_sha256'], 'compatibility_digest_mismatch')
    compatibility = decode(raw)
    require(isinstance(compatibility, dict) and type(compatibility.get('schema')) is int and compatibility['schema'] == 1 and
            compatibility.get('core_version') == pin['core_version'] and compatibility.get('api_contract') == '/api/v1' and
            compatibility.get('backend_repository') == BACKEND and
            compatibility.get('defaults') == {'locale': 'zh-CN', 'currency': 'CNY'}, 'compatibility_contract_mismatch')
    package = decode(regular(frontend / 'package.json'))
    require(isinstance(package, dict) and package.get('version') == pin['frontend_version'], 'frontend_version_mismatch')
    epoch = git(frontend, 'show', '-s', '--format=%ct', 'HEAD')
    require(re.fullmatch(r'[0-9]{1,10}', epoch), 'source_epoch_invalid')
    return dict(schema=1, backend_repository=BACKEND, backend_source_sha=backend_source,
                frontend_repository=FRONTEND, frontend_repository_id=FRONTEND_ID, frontend_source_sha=pin['source_sha'],
                frontend_version=pin['frontend_version'], core_version=pin['core_version'], frontend_source_epoch=int(epoch),
                compatibility_sha256=pin['compatibility_sha256'], defaults=compatibility['defaults'],
                verified_checkout=True, authenticated_release=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    commands.add_parser('pin', help='Print fixed, validated checkout outputs for GitHub Actions')
    check = commands.add_parser('verify', help='Verify both checkout identities and the compatibility contract')
    check.add_argument('--repository-metadata', type=Path, required=True)
    check.add_argument('--backend-source-sha', required=True)
    args = parser.parse_args()
    try:
        pin = load_pin()
        if args.action == 'pin':
            print('source_sha=' + pin['source_sha'])
        else:
            print(json.dumps(verify(pin, args.repository_metadata, args.backend_source_sha), sort_keys=True))
        return 0
    except Rejected as error:
        print('Frontend pair refused: ' + str(error), file=sys.stderr)
        return 1
    except (ValueError, OSError, UnicodeError, RecursionError, subprocess.SubprocessError):
        # Provider data, Git diagnostics and environment values are not errors
        # safe for public CI logs. Refusal is never treated as a skipped check.
        print('Frontend pair refused: source, identity or compatibility validation failed.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
