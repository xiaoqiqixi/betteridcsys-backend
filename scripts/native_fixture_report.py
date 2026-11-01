#!/usr/bin/env python3
"""Record bounded cleanup facts after mysql-native.sh returns; never remove paths."""
import argparse
import json
import os
from pathlib import Path
import re
import stat


def private_directory(path):
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError('private directory required')


def record(root, output, source, exit_code):
    if not re.fullmatch(r'[0-9a-f]{40}', source) or not 0 <= exit_code <= 255:
        raise ValueError('invalid run result')
    private_directory(root)
    names = [item.name for item in root.iterdir()]
    value = dict(schema='aster.native-fixture-cleanup/1', source_sha=source,
                 command_exit_code=exit_code,
                 owned_mysql_fixture_removed=not any(name.startswith('aster-mysql.') for name in names),
                 owned_benchmark_scratch_removed=not any(name.startswith('aster-performance.') for name in names))
    value['passed'] = exit_code == 0 and value['owned_mysql_fixture_removed'] and value['owned_benchmark_scratch_removed']
    output.parent.mkdir(mode=0o700, exist_ok=True)
    private_directory(output.parent)
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'w') as stream:
        json.dump(value, stream, sort_keys=True)
        stream.write('\n')
    return value['passed']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--source', required=True)
    parser.add_argument('--exit-code', type=int, required=True)
    args = parser.parse_args()
    try:
        passed = record(args.root, args.output, args.source, args.exit_code)
    except (OSError, ValueError):
        raise SystemExit('FAIL: Native fixture cleanup evidence could not be verified or written.')
    if not passed:
        raise SystemExit('FAIL: Native command failed or owned fixture cleanup is incomplete.')


if __name__ == '__main__':
    main()
