#!/usr/bin/env python3
"""Stage the two validated candidate archives for actual process/browser tests.

Not an installer or an authenticated release consumer. No root, host services,
network, package execution, legacy BUILD/receipt forgery or source fallback.
"""
import argparse
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import sys
import tarfile
import tempfile
import zlib

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
FRONTEND_POLICY_SHA256 = '8d4d8f9a050da51bef76084be835c320ac297222b33df9e9f709679d5e1981fa'
LIMIT = 192 * 1024 * 1024
MANIFEST_LIMIT = 2 * 1024 * 1024


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


B = load('paired_backend_content', ROOT / 'scripts/backend_bundle.py')
P = B.PAIR


def frontend_policy():
    path = ROOT / 'contracts/frontend-bundle/frontend_bundle.py'
    P.require(B.digest(P.regular(path, 65536)) == FRONTEND_POLICY_SHA256, 'frontend_content_policy_changed')
    return load('paired_frontend_content', path)


def frontend_target(name):
    if name.startswith('dist/'):
        return 'web/' + name[5:]
    if name.startswith('manifests/'):
        return 'manifests/frontend/' + name[10:]
    if name == 'DEPENDENCIES.json':
        return 'manifests/frontend/DEPENDENCIES.json'
    if name == 'LICENSE':
        return 'licenses/frontend/LICENSE'
    if name.startswith('licenses/'):
        return 'licenses/frontend/packages/' + name[9:]
    raise P.Rejected('unexpected_frontend_resource')


def checked_frontend(args, pin):
    policy = frontend_policy()
    # The approved parser reads owned private copies of the exact captured bytes.
    # Subsequent extraction uses those same in-memory bytes, not mutable inputs.
    manifest = P.regular(args.frontend_manifest, policy.MAX_MANIFEST)
    payload = P.regular(args.frontend_archive, policy.MAX_ARCHIVE)
    with tempfile.TemporaryDirectory(prefix='aster-frontend-content-') as tmp:
        root = Path(tmp).resolve()
        manifest_path, archive_path = root / 'FRONTEND.json', root / ('aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz')
        manifest_path.write_bytes(manifest)
        archive_path.write_bytes(payload)
        try:
            policy.inspect(argparse.Namespace(manifest=manifest_path, archive=archive_path,
                                              expected_source_sha=pin['source_sha'], expected_core_version=pin['core_version']))
        except (policy.Invalid, ValueError, TypeError, KeyError):
            raise P.Rejected('frontend_content_rejected') from None
    value = P.decode(manifest)
    P.require(value['version'] == pin['frontend_version'], 'frontend_version_mismatch')
    decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
    raw = decoder.decompress(payload, policy.MAX_RAW + 1)
    P.require(len(raw) <= policy.MAX_RAW and decoder.eof and not decoder.unconsumed_tail and not decoder.unused_data,
              'single_frontend_gzip_required')
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:') as archive:
        files = {entry.name: archive.extractfile(entry).read() for entry in archive}
    P.require(B.digest(files['manifests/compatibility.json']) == pin['compatibility_sha256'], 'frontend_compatibility_mismatch')
    return value, files, manifest


def shape_identity(back, front, pin, source):
    P.require(isinstance(source, str) and re.fullmatch(P.SHA, source), 'backend_source_required')
    P.require(isinstance(back, dict) and back.get('repository') == B.REPOSITORY and
              type(back.get('repository_id')) is int and back['repository_id'] == B.REPOSITORY_ID and
              back.get('format') == 'backend-tar-v1' and back.get('source_commit') == source and
              back.get('version') == pin['core_version'] and B.encode(back.get('frontend_pair')) == B.encode(pin),
              'backend_identity_mismatch')
    P.require(isinstance(front, dict) and front.get('repository') == P.FRONTEND and
              type(front.get('repository_id')) is int and front['repository_id'] == P.FRONTEND_ID and
              front.get('format') == 'static-site-tar-v1' and front.get('source_commit') == pin['source_sha'] and
              front.get('version') == pin['frontend_version'] and front.get('core_version') == pin['core_version'],
              'frontend_identity_mismatch')


def expected_inventory(back, front, back_raw, front_raw):
    inventory = {}
    for manifest, convert, allowed in ((back, lambda name: name, B.resource_name),
                                       (front, frontend_target, lambda name: True)):
        entries = manifest.get('files')
        P.require(isinstance(entries, list) and 1 <= len(entries) <= 4096, 'component_inventory_bound')
        seen = set()
        for entry in entries:
            P.require(isinstance(entry, dict) and set(entry) == {'path', 'size', 'sha256'}, 'component_inventory_schema')
            name = entry['path']
            P.require(B.name_ok(name) and name not in seen and allowed(name) and
                      type(entry['size']) is int and 0 <= entry['size'] <= B.MAX_FILE and
                      isinstance(entry['sha256'], str) and re.fullmatch(r'[0-9a-f]{64}', entry['sha256']), 'component_inventory_invalid')
            seen.add(name)
            target = convert(name)
            P.require(target not in inventory, 'component_path_collision')
            inventory[target] = dict(entry, path=target)
    for name, raw in (('manifests/BACKEND.json', back_raw), ('manifests/FRONTEND.json', front_raw)):
        P.require(name not in inventory, 'component_proof_collision')
        inventory[name] = {'path': name, 'size': len(raw), 'sha256': B.digest(raw)}
    P.require(len(inventory) <= 6146 and sum(item['size'] for item in inventory.values()) <= LIMIT, 'paired_runtime_bound')
    return [inventory[name] for name in sorted(inventory)]


def report(source, pin, count):
    return {'backend_source': source, 'frontend_source': pin['source_sha'], 'version': pin['core_version'],
            'frontend_version': pin['frontend_version'], 'authenticated_release': False, 'installed': False, 'files': count}


def stage(args):
    P.require(os.geteuid() != 0, 'unprivileged_candidate_staging_required')
    pin = P.load_pin()
    back, backend_files, back_raw = B.verified_content(argparse.Namespace(
        manifest=args.backend_manifest, archive=args.backend_archive, expected_source_sha=args.backend_source,
        expected_frontend_sha=pin['source_sha']))
    front, frontend_files, front_raw = checked_frontend(args, pin)
    shape_identity(back, front, pin, args.backend_source)
    inventory = expected_inventory(back, front, back_raw, front_raw)
    files = dict(backend_files)
    for name, raw in frontend_files.items():
        target = frontend_target(name)
        P.require(target not in files, 'component_path_collision')
        files[target] = raw
    files.update({'manifests/BACKEND.json': back_raw, 'manifests/FRONTEND.json': front_raw})
    P.require(inventory == [{'path': name, 'size': len(raw), 'sha256': B.digest(raw)} for name, raw in sorted(files.items())],
              'staged_inventory_mismatch')
    output = args.output
    P.require(output.is_absolute() and output.parent.resolve(strict=True) == output.parent and
              not output.exists() and not output.is_symlink(), 'new_canonical_staging_required')
    info = output.parent.stat()
    P.require(info.st_uid == os.geteuid() and not info.st_mode & 0o022, 'owned_staging_parent_required')
    value = dict(report(args.backend_source, pin, len(files)), schema=1, format='paired-runtime-candidate-v1', inventory=inventory)
    encoded = B.encode(value)
    P.require(len(encoded) <= MANIFEST_LIMIT, 'paired_manifest_bound')
    output.mkdir(mode=0o700)
    for name, raw in sorted(files.items()):
        target = output / name
        directory = output
        for part in Path(name).parts[:-1]:
            directory = directory / part
            directory.mkdir(mode=0o700, exist_ok=True)
        with target.open('xb') as stream:
            stream.write(raw)
        target.chmod(0o555 if name in B.BINS else 0o444)
    # Receipt last; a partial directory is retained and can never be reused.
    with (output / 'PAIR.json').open('xb') as stream:
        stream.write(encoded)
    (output / 'PAIR.json').chmod(0o444)
    return report(args.backend_source, pin, len(files))


def verify(args):
    P.require(os.geteuid() != 0, 'unprivileged_candidate_verification_required')
    root = args.directory
    info = root.lstat()
    P.require(root.is_absolute() and root.resolve(strict=True) == root and stat.S_ISDIR(info.st_mode) and
              info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o700, 'owned_private_candidate_required')
    pin = P.load_pin()
    value = P.decode(P.regular(root / 'PAIR.json', MANIFEST_LIMIT))
    keys = {'schema', 'format', 'backend_source', 'frontend_source', 'version', 'frontend_version',
            'authenticated_release', 'installed', 'files', 'inventory'}
    P.require(isinstance(value, dict) and set(value) == keys and type(value['schema']) is int and value['schema'] == 1 and
              value['format'] == 'paired-runtime-candidate-v1' and value['authenticated_release'] is False and value['installed'] is False,
              'candidate_receipt_schema')
    back_raw = P.regular(root / 'manifests/BACKEND.json', B.MAX_MANIFEST)
    front_raw = P.regular(root / 'manifests/FRONTEND.json', 512 * 1024)
    back, front = P.decode(back_raw), P.decode(front_raw)
    shape_identity(back, front, pin, args.backend_source)
    inventory = expected_inventory(back, front, back_raw, front_raw)
    expected = dict(report(args.backend_source, pin, len(inventory)), schema=1, format='paired-runtime-candidate-v1', inventory=inventory)
    P.require(B.encode(value) == B.encode(expected), 'candidate_receipt_identity_mismatch')
    paths = {item['path']: item for item in inventory}
    actual_files, actual_dirs = set(), set()
    for base, dirs, names in os.walk(root, followlinks=False):
        for name in dirs:
            path = Path(base) / name
            details = path.lstat()
            P.require(not path.is_symlink() and stat.S_ISDIR(details.st_mode) and details.st_uid == os.geteuid() and
                      stat.S_IMODE(details.st_mode) == 0o700, 'candidate_directory_changed')
            actual_dirs.add(path.relative_to(root).as_posix())
        for name in names:
            path = Path(base) / name
            relative = path.relative_to(root).as_posix()
            P.require(relative in paths or relative == 'PAIR.json', 'candidate_unknown_file')
            details = path.lstat()
            P.require(details.st_uid == os.geteuid() and stat.S_IMODE(details.st_mode) == (0o555 if relative in B.BINS else 0o444),
                      'candidate_file_permissions_changed')
            if relative == 'PAIR.json':
                continue
            raw = B.read(path)
            P.require(len(raw) == paths[relative]['size'] and B.digest(raw) == paths[relative]['sha256'], 'candidate_file_changed')
            actual_files.add(relative)
    expected_dirs = {parent.as_posix() for name in paths for parent in Path(name).parents if parent != Path('.')}
    P.require(actual_files == set(paths) and actual_dirs == expected_dirs, 'candidate_tree_incomplete')
    P.require(B.digest(B.read(root / 'manifests/frontend/compatibility.json')) == pin['compatibility_sha256'],
              'candidate_frontend_compatibility_changed')
    return report(args.backend_source, pin, len(inventory))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest='action', required=True)
    create = actions.add_parser('stage')
    for name in ('backend-manifest', 'backend-archive', 'frontend-manifest', 'frontend-archive', 'output'):
        create.add_argument('--' + name, type=Path, required=True)
    create.add_argument('--backend-source', required=True)
    check = actions.add_parser('verify')
    check.add_argument('--directory', type=Path, required=True)
    check.add_argument('--backend-source', required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(stage(args) if args.action == 'stage' else verify(args), sort_keys=True))
        return 0
    except P.Rejected as error:
        print('Paired runtime refused: ' + str(error), file=sys.stderr)
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, RecursionError, tarfile.TarError, zlib.error, EOFError):
        print('Paired runtime refused: component identity or bounded content validation failed.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
