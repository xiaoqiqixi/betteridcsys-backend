#!/usr/bin/env python3
"""Build/inspect a bounded static-site bundle. Content checks are NOT source authentication.

No network, extraction, package execution, installation or service operations.
The release verifier must independently authenticate GitHub/workflow/asset identity.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import sys
import tarfile
import zlib

REPOSITORY = 'xiaoqiqixi/betteridcsys-frontend'
REPOSITORY_ID = 1435344023
BACKEND = 'xiaoqiqixi/betteridcsys-backend'
MAX_FILE = 8 * 1024 * 1024
MAX_ARCHIVE = 32 * 1024 * 1024
MAX_RAW = 64 * 1024 * 1024
MAX_MANIFEST = 512 * 1024
MAX_FILES = 2048
MAX_PACKAGE_RECORDS = 4096
VERSION = r'(?:0|[1-9][0-9]{0,5})(?:\.(?:0|[1-9][0-9]{0,5})){2}'
SHA = r'[0-9a-f]{40}'
DIGEST = r'[0-9a-f]{64}'
STATIC_SUFFIXES = {'.html', '.js', '.css', '.svg', '.ico', '.png', '.jpg', '.jpeg', '.webp', '.avif', '.woff', '.woff2', '.ttf', '.txt', '.webmanifest'}


class Invalid(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise Invalid(message)


def matches(pattern, value):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def encode(obj):
    return (json.dumps(obj, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode()


def unique_pairs(pairs):
    obj = {}
    for key, value in pairs:
        require(key not in obj, 'duplicate JSON field')
        obj[key] = value
    return obj


def decode(raw):
    try:
        return json.loads(raw.decode('utf-8'), object_pairs_hook=unique_pairs,
                          parse_constant=lambda _: (_ for _ in ()).throw(Invalid('nonfinite JSON value')))
    except (UnicodeError, json.JSONDecodeError, RecursionError) as exc:
        raise Invalid('invalid JSON') from exc


def regular(path, maximum=MAX_FILE):
    path = Path(path)
    for parent in (path, *path.parents):
        require(not parent.is_symlink(), 'symlink input is forbidden')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and 0 < info.st_size <= maximum,
                'input must be a bounded nonempty regular file with one link')
        raw = stream.read(maximum + 1)
        require(len(raw) == info.st_size and len(raw) <= maximum, 'input changed or exceeds size limit')
        return raw


def valid_path(name):
    return (isinstance(name, str) and len(name) <= 240 and
            matches(r'[A-Za-z0-9_@+./-]+', name) and
            all(part not in {'', '.', '..'} and not part.startswith('.') for part in name.split('/')))


def metadata(files):
    package = decode(files['manifests/package.json'])
    lock = decode(files['manifests/package-lock.json'])
    compatibility = decode(files['manifests/compatibility.json'])
    require(isinstance(package, dict) and isinstance(lock, dict) and isinstance(compatibility, dict), 'invalid metadata objects')
    version = package.get('version')
    require(matches(VERSION, version), 'expected a stable frontend version')
    require(lock.get('version') == version and lock.get('lockfileVersion') == 3 and
            type(lock.get('lockfileVersion')) is int and isinstance(lock.get('packages'), dict), 'lockfile version mismatch')
    require(len(lock['packages']) <= MAX_PACKAGE_RECORDS, 'dependency record limit exceeded')
    main = lock['packages'].get('')
    require(isinstance(main, dict) and main.get('version') == version and main.get('name') == package.get('name') == lock.get('name'), 'lockfile root mismatch')
    require(set(compatibility) == {'schema', 'core_version', 'api_contract', 'backend_repository', 'source_commit', 'defaults'} and
            type(compatibility['schema']) is int and compatibility['schema'] == 1 and
            matches(VERSION, compatibility['core_version']) and compatibility['api_contract'] == '/api/v1' and
            compatibility['backend_repository'] == BACKEND and matches(SHA, compatibility['source_commit']) and
            compatibility['defaults'] == {'locale': 'zh-CN', 'currency': 'CNY'}, 'invalid compatibility contract')
    return version, compatibility['core_version'], lock


def tar_bytes(files, epoch):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for name, data in sorted(files.items()):
            require(valid_path(name), 'unsafe archive path')
            entry = tarfile.TarInfo(name)
            entry.size, entry.mtime, entry.mode = len(data), epoch, 0o444
            entry.uid = entry.gid = 0
            archive.addfile(entry, io.BytesIO(data))
    result = raw.getvalue()
    require(len(result) <= MAX_RAW, 'expanded archive exceeds limit')
    return result


def collect(root):
    files = {'LICENSE': regular(root / 'LICENSE')}
    for name in ('package.json', 'package-lock.json', 'compatibility.json'):
        files['manifests/' + name] = regular(root / name)
    dist = root / 'dist'
    require(dist.is_dir() and not dist.is_symlink(), 'expected a built static site')
    count = 0
    for base, directories, names in os.walk(dist, followlinks=False):
        for name in directories:
            require(not (Path(base) / name).is_symlink(), 'symlink directory is forbidden')
        count += len(directories) + len(names)
        require(count <= MAX_FILES, 'too many static entries')
        for name in names:
            path = Path(base) / name
            relative = path.relative_to(root).as_posix()
            require(valid_path(relative) and path.suffix.lower() in STATIC_SUFFIXES, 'unexpected static file')
            files[relative] = regular(path)
            require(sum(map(len, files.values())) <= MAX_RAW, 'static site exceeds size limit')
    require('dist/index.html' in files, 'missing static entrypoint')
    version, core, lock = metadata(files)
    dependencies = []
    for location, spec in sorted(lock['packages'].items()):
        require(isinstance(spec, dict), 'invalid lock entry')
        if not location or spec.get('dev') is True:
            continue
        require(valid_path(location) and location.startswith('node_modules/') and not spec.get('link'), 'unexpected dependency location')
        package = decode(regular(root / location / 'package.json'))
        require(isinstance(package, dict) and package.get('version') == spec.get('version') and
                isinstance(package.get('name'), str), 'installed dependency differs from lockfile')
        notices = []
        for path in sorted((root / location).iterdir()):
            if path.name.upper().split('.')[0] not in {'LICENSE', 'LICENCE', 'COPYING', 'COPYRIGHT', 'NOTICE'}:
                continue
            target = 'licenses/' + location.removeprefix('node_modules/') + '/' + path.name
            require(valid_path(target), 'unsafe notice path')
            files[target] = regular(path)
            require(len(files) <= MAX_FILES and sum(map(len, files.values())) <= MAX_RAW,
                    'dependency notices exceed bundle limits')
            notices.append(target)
        dependencies.append({'name': package['name'], 'version': package['version'],
                             'license': package.get('license', 'not-declared'),
                             'lock_location': location, 'integrity': spec.get('integrity', ''),
                             'notice_files': notices})
    files['DEPENDENCIES.json'] = encode({'scope': 'production_lock_closure_not_tree_shaking_attribution', 'packages': dependencies})
    require(len(files) <= MAX_FILES and all(0 < len(raw) <= MAX_FILE for raw in files.values()), 'bundle file limits exceeded')
    return version, core, files


def build(args):
    require(matches(SHA, args.source_sha), 'invalid source commit')
    require(946684800 <= args.source_date_epoch <= 4294967295, 'invalid source timestamp')
    version, core, files = collect(Path(args.source_root))
    raw = tar_bytes(files, args.source_date_epoch)
    compressed = io.BytesIO()
    with gzip.GzipFile(filename='', mode='wb', fileobj=compressed, compresslevel=9, mtime=args.source_date_epoch) as stream:
        stream.write(raw)
    payload = compressed.getvalue()
    require(len(payload) <= MAX_ARCHIVE, 'compressed archive exceeds limit')
    asset_name = 'aster-finance-frontend-' + version + '.tar.gz'
    manifest = {'schema': 1, 'kind': 'finance-frontend', 'repository': REPOSITORY, 'repository_id': REPOSITORY_ID,
                'version': version, 'core_version': core, 'source_commit': args.source_sha,
                'source_date_epoch': args.source_date_epoch, 'format': 'static-site-tar-v1',
                'asset': {'name': asset_name, 'size': len(payload), 'sha256': digest(payload)},
                'files': [{'path': name, 'size': len(data), 'sha256': digest(data)} for name, data in sorted(files.items())]}
    encoded = encode(manifest)
    require(len(encoded) <= MAX_MANIFEST, 'content manifest exceeds limit')
    output = Path(args.output)
    for parent in output.parents:
        require(not parent.is_symlink(), 'symlink output parent is forbidden')
    # An existing directory, including a previous partial attempt, is never reused.
    output.mkdir(mode=0o700)
    for name, data in ((asset_name, payload), ('FRONTEND.json', encoded)):
        with (output / name).open('xb') as stream:
            stream.write(data)
    return {'status': 'built', 'authenticated_source': False, 'asset': asset_name, 'files': len(files)}


def inspect(args):
    manifest = decode(regular(args.manifest, MAX_MANIFEST))
    keys = {'schema', 'kind', 'repository', 'repository_id', 'version', 'core_version', 'source_commit', 'source_date_epoch', 'format', 'asset', 'files'}
    require(isinstance(manifest, dict) and set(manifest) == keys, 'invalid manifest fields')
    require(type(manifest['schema']) is int and manifest['schema'] == 1 and manifest['kind'] == 'finance-frontend' and
            manifest['repository'] == REPOSITORY and type(manifest['repository_id']) is int and manifest['repository_id'] == REPOSITORY_ID and
            manifest['format'] == 'static-site-tar-v1', 'invalid bundle identity')
    require(matches(VERSION, manifest['version']) and matches(VERSION, args.expected_core_version) and
            manifest['core_version'] == args.expected_core_version and matches(SHA, args.expected_source_sha) and
            manifest['source_commit'] == args.expected_source_sha, 'unexpected version or source')
    epoch = manifest['source_date_epoch']
    require(type(epoch) is int and 946684800 <= epoch <= 4294967295, 'invalid source timestamp')
    asset = manifest['asset']
    require(isinstance(asset, dict) and set(asset) == {'name', 'size', 'sha256'} and
            asset['name'] == 'aster-finance-frontend-' + manifest['version'] + '.tar.gz' and
            type(asset['size']) is int and 0 < asset['size'] <= MAX_ARCHIVE and matches(DIGEST, asset['sha256']), 'invalid asset metadata')
    payload = regular(args.archive, MAX_ARCHIVE)
    require(Path(args.archive).name == asset['name'] and len(payload) == asset['size'] and digest(payload) == asset['sha256'], 'archive digest or size mismatch')
    entries = manifest['files']
    require(isinstance(entries, list) and 5 <= len(entries) <= MAX_FILES, 'invalid file inventory')
    inventory = {}
    for entry in entries:
        require(isinstance(entry, dict) and set(entry) == {'path', 'size', 'sha256'} and valid_path(entry['path']) and
                type(entry['size']) is int and 0 < entry['size'] <= MAX_FILE and matches(DIGEST, entry['sha256']), 'invalid inventory entry')
        name = entry['path']
        require(name not in inventory, 'duplicate inventory path')
        require(name in {'LICENSE', 'DEPENDENCIES.json', 'manifests/package.json', 'manifests/package-lock.json', 'manifests/compatibility.json'} or
                (name.startswith('dist/') and Path(name).suffix.lower() in STATIC_SUFFIXES) or name.startswith('licenses/'), 'unexpected bundle file')
        inventory[name] = entry
    require(list(inventory) == sorted(inventory) and sum(e['size'] for e in entries) <= MAX_RAW, 'unordered or oversized inventory')
    with gzip.GzipFile(fileobj=io.BytesIO(payload), mode='rb') as stream:
        raw = stream.read(MAX_RAW + 1)
    require(len(raw) <= MAX_RAW, 'expanded archive exceeds limit')
    files = {}
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:') as archive:
        for entry in archive:
            require(entry.name in inventory and entry.name not in files and entry.isreg() and not entry.pax_headers and
                    entry.mode == 0o444 and entry.uid == entry.gid == 0 and not entry.uname and not entry.gname and
                    entry.mtime == epoch and entry.size == inventory[entry.name]['size'], 'unexpected archive member')
            stream = archive.extractfile(entry)
            require(stream is not None, 'unreadable member')
            data = stream.read(MAX_FILE + 1)
            require(len(data) == entry.size and digest(data) == inventory[entry.name]['sha256'], 'member digest mismatch')
            files[entry.name] = data
    require(set(files) == set(inventory) and raw == tar_bytes(files, epoch), 'noncanonical or incomplete archive')
    mandatory = {'LICENSE', 'DEPENDENCIES.json', 'dist/index.html', 'manifests/package.json', 'manifests/package-lock.json', 'manifests/compatibility.json'}
    require(mandatory <= set(files), 'missing required bundle files')
    version, core, _ = metadata(files)
    require(version == manifest['version'] and core == manifest['core_version'], 'packaged version mismatch')
    require(isinstance(decode(files['DEPENDENCIES.json']), dict), 'invalid dependency inventory')
    return {'status': 'content_verified', 'authenticated_source': False, 'version': version, 'core_version': core, 'files': sorted(files)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    builder = sub.add_parser('build')
    builder.add_argument('--source-root', required=True)
    builder.add_argument('--output', required=True)
    builder.add_argument('--source-sha', required=True)
    builder.add_argument('--source-date-epoch', required=True, type=int)
    checker = sub.add_parser('inspect')
    checker.add_argument('--manifest', required=True)
    checker.add_argument('--archive', required=True)
    checker.add_argument('--expected-source-sha', required=True)
    checker.add_argument('--expected-core-version', required=True)
    args = parser.parse_args()
    try:
        result = build(args) if args.command == 'build' else inspect(args)
    except (Invalid, OSError, ValueError, KeyError, TypeError, tarfile.TarError, zlib.error, EOFError, RecursionError) as exc:
        print('bundle rejected: ' + str(exc), file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
