#!/usr/bin/env python3
"""Build/inspect the independent backend half of a paired finance distribution.

Unprivileged build tooling only. This new content format is NOT an authenticated
release or input to the legacy single-source installer. No host apply, signing,
publishing, frontend execution or substitution of the frontend source occurs.
"""
import argparse
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tarfile
import tempfile
import zlib

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location('backend_bundle_source_pair', ROOT / 'scripts/frontend_pair.py')
PAIR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PAIR)
REPOSITORY = 'xiaoqiqixi/betteridcsys-backend'
REPOSITORY_ID = 1435344087
MAX_FILES, MAX_FILE, MAX_RAW, MAX_ARCHIVE = 4096, 32 * 1024 * 1024, 128 * 1024 * 1024, 64 * 1024 * 1024
MAX_MANIFEST = 1024 * 1024
BINS = {'bin/aster-api': './cmd/server', 'bin/aster-plugin-host': './cmd/plugin-host'}
SCRIPTS = ('backup-native.sh', 'core-manager.py', 'core-operation.py', 'plugin-manager.py', 'update-manager.py')
SYSTEMD = {'aster-api.service', 'aster-payment.service', 'aster-resource.service', 'aster-smtp.service',
           'api.env.example', 'journald-aster.conf.example'}


def require(ok, code):
    PAIR.require(ok, code)


def encode(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def name_ok(name):
    return (isinstance(name, str) and len(name) <= 240 and re.fullmatch(r'[A-Za-z0-9_@+./-]+', name) and
            all(part not in ('', '.', '..') and not part.startswith('.') for part in name.split('/')))


def run(argv, cwd, env=None, limit=MAX_MANIFEST, timeout=600):
    # Keep command diagnostics and temporary paths out of public CI errors.
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
        result = subprocess.run(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                stdout=out, stderr=err, timeout=timeout)
        require(result.returncode == 0, 'build_command_failed')
        out.seek(0)
        raw = out.read(limit + 1)
        require(len(raw) <= limit, 'command_output_bound')
        return raw


def git_source(source):
    require(isinstance(source, str) and re.fullmatch(PAIR.SHA, source), 'source_sha_required')
    require(PAIR.git(ROOT, 'rev-parse', '--show-toplevel') == str(ROOT), 'independent_git_root_required')
    require(PAIR.git(ROOT, 'rev-parse', '--verify', 'HEAD^{commit}') == source, 'source_sha_mismatch')
    require(not PAIR.git(ROOT, 'status', '--porcelain=v1', '--untracked-files=all'), 'source_tree_not_clean')
    require(PAIR.git(ROOT, 'ls-files', '--', 'scripts/backend_bundle.py', 'scripts/frontend_pair.py').splitlines() ==
            ['scripts/backend_bundle.py', 'scripts/frontend_pair.py'], 'reviewed_build_recipes_required')
    epoch = int(PAIR.git(ROOT, 'show', '-s', '--format=%ct', source))
    require(946684800 <= epoch <= 4294967295, 'source_epoch_invalid')
    return epoch


def snapshot(source, destination):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    env.update(GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0')
    raw = run(['git', '--no-replace-objects', '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
               'archive', '--format=tar', source, '--', 'LICENSE', 'FRONTEND_PAIR.json', 'vps-billing/backend',
               'vps-billing/docs', 'vps-billing/deploy/systemd', 'vps-billing/deploy/nginx.native.conf',
               *('vps-billing/deploy/scripts/' + name for name in SCRIPTS)], ROOT, env, limit=MAX_RAW)
    seen, total = set(), 0
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:') as archive:
        for member in archive:
            if member.isdir():
                continue
            total += member.size
            require(member.isfile() and member.name not in seen and name_ok(member.name) and
                    0 <= member.size <= MAX_FILE and len(seen) < MAX_FILES and total <= MAX_RAW,
                    'unsafe_source_archive')
            seen.add(member.name)
            target = destination / member.name
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open('xb') as out:
                out.write(archive.extractfile(member).read())
    require({'LICENSE', 'FRONTEND_PAIR.json', 'vps-billing/backend/go.mod'} <= seen, 'source_inventory_incomplete')


def put(files, name, raw):
    require(name_ok(name) and name not in files and isinstance(raw, bytes) and len(raw) <= MAX_FILE,
            'invalid_resource')
    files[name] = raw
    require(len(files) <= MAX_FILES and sum(map(len, files.values())) <= MAX_RAW, 'resource_budget_exceeded')


def resource_name(name):
    return (name in {'LICENSE', 'FRONTEND_PAIR.json', 'manifests/go.mod', 'manifests/go.sum',
                     'DEPENDENCIES.json', 'deploy/nginx.native.conf', *BINS} or
            name in {'deploy/scripts/' + script for script in SCRIPTS} or
            name in {'deploy/systemd/' + unit for unit in SYSTEMD} or
            re.fullmatch(r'migrations/[0-9]{3}_[A-Za-z0-9_-]+\.sql', name) is not None or
            (name.startswith('docs/') and name.endswith('.md')) or name.startswith('licenses/'))


def complete_inventory(files, pin):
    require(all(resource_name(name) for name in files), 'unexpected_backend_resource')
    required = {'LICENSE', 'FRONTEND_PAIR.json', 'manifests/go.mod', 'manifests/go.sum', 'DEPENDENCIES.json',
                'licenses/go-runtime/LICENSE', 'docs/OPERATIONS.md', 'deploy/nginx.native.conf',
                'deploy/systemd/aster-api.service', *BINS, *('deploy/scripts/' + name for name in SCRIPTS)}
    require(required <= set(files) and any(name.startswith('migrations/') for name in files),
            'required_resources_missing')
    require(all(raw or name == 'manifests/go.sum' for name, raw in files.items()), 'empty_resource_refused')
    require(encode(PAIR.decode(files['FRONTEND_PAIR.json'])) == encode(pin) and all(elf(files[name]) for name in BINS),
            'bundle_contract_mismatch')


def read(path):
    # go.sum may legitimately be empty for a module without dependencies.
    require(path.resolve(strict=True) == path, 'linked_resource')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        before = os.fstat(stream.fileno())
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and 0 <= before.st_size <= MAX_FILE,
                'bounded_resource_required')
        raw = stream.read(MAX_FILE + 1)
        after = os.fstat(stream.fileno())
        require(len(raw) == before.st_size and before.st_size == after.st_size and
                before.st_mtime_ns == after.st_mtime_ns, 'resource_changed')
        return raw


def elf(raw):
    return len(raw) >= 64 and raw[:7] == b'\x7fELF\x02\x01\x01' and raw[18:20] == b'\x3e\x00'


def notices(files, directory, prefix):
    found = []
    for path in sorted(directory.iterdir()):
        if not re.match(r'^(licen[sc]e|copying|notice|copyright|patents)([._-]|$)', path.name, re.I):
            continue
        name = prefix + '/' + path.name
        put(files, name, read(path))
        found.append(name)
    require(found, 'dependency_notice_missing')
    return found


def compile_backend(source, files, scratch):
    backend = source / 'vps-billing/backend'
    env = {k: v for k, v in os.environ.items() if not k.startswith(('GIT_', 'GO'))}
    env.update(GOENV='off', GOFLAGS='', GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='0',
               GOOS='linux', GOARCH='amd64', LC_ALL='C.UTF-8')
    run(['go', 'mod', 'download'], backend, env)
    run(['go', 'mod', 'verify'], backend, env)
    modules = run(['go', 'list', '-mod=readonly', '-m', '-json', 'all'], backend, env).decode()
    build_info = {}
    for name, package in BINS.items():
        target = scratch / Path(name).name
        run(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w',
             '-o', str(target), package], backend, env)
        raw = read(target)
        require(elf(raw), 'linux_amd64_binary_required')
        put(files, name, raw)
        lines = run(['go', 'version', '-m', str(target)], backend, env).decode().splitlines()
        require(lines and ': ' in lines[0], 'go_build_info_required')
        build_info[name] = '\n'.join([name + ': ' + lines[0].split(': ', 1)[1], *lines[1:]]) + '\n'
    linked = {fields[1] for text in build_info.values() for line in text.splitlines()
              if len(fields := line.split()) >= 2 and fields[0] == 'dep'}
    decoder, dependencies = json.JSONDecoder(), []
    while modules.strip():
        module, end = decoder.raw_decode(modules.lstrip())
        modules = modules.lstrip()[end:]
        require(isinstance(module, dict) and not module.get('Replace'), 'module_replace_refused')
        if module.get('Main'):
            continue
        location, version = module.get('Path'), module.get('Version')
        require(isinstance(location, str) and isinstance(version, str), 'module_identity_invalid')
        item = {'path': location, 'version': version, 'linked_in_binary': location in linked, 'notice_files': []}
        if item['linked_in_binary']:
            require(isinstance(module.get('Dir'), str), 'linked_module_source_missing')
            item['notice_files'] = notices(files, Path(module['Dir']), 'licenses/go/' + location + '@' + version)
            item['sum'] = module.get('Sum')
            require(isinstance(item['sum'], str) and item['sum'].startswith('h1:'), 'module_sum_missing')
        dependencies.append(item)
    goroot = Path(run(['go', 'env', 'GOROOT'], backend, env).decode().strip()).resolve()
    notices(files, goroot, 'licenses/go-runtime')
    put(files, 'DEPENDENCIES.json', encode({'scope': 'go_module_graph_with_linked_binary_notices',
                                         'modules': sorted(dependencies, key=lambda item: item['path']),
                                         'build_info': build_info}))


def archive_bytes(files, epoch):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for name, raw in sorted(files.items()):
            entry = tarfile.TarInfo(name)
            entry.size, entry.mtime = len(raw), epoch
            entry.mode = 0o555 if name in BINS else 0o444
            entry.uid = entry.gid = 0
            archive.addfile(entry, io.BytesIO(raw))
    require(len(stream.getvalue()) <= MAX_RAW, 'expanded_archive_bound')
    return stream.getvalue()


def compress(raw, epoch):
    stream = io.BytesIO()
    with gzip.GzipFile(filename='', fileobj=stream, mode='wb', compresslevel=9, mtime=epoch) as out:
        out.write(raw)
    result = stream.getvalue()
    require(0 < len(result) <= MAX_ARCHIVE, 'compressed_archive_bound')
    return result


def build(args):
    require(os.geteuid() != 0, 'unprivileged_builder_required')
    epoch = git_source(args.source_sha)
    pin = PAIR.load_pin()
    output = args.output
    require(output.is_absolute() and output.parent.resolve(strict=True) == output.parent and
            not output.exists() and not output.is_symlink(), 'new_canonical_output_required')
    files = {}
    with tempfile.TemporaryDirectory(prefix='aster-backend-build-') as tmp:
        scratch = Path(tmp).resolve()
        source = scratch / 'source'
        source.mkdir()
        snapshot(args.source_sha, source)
        project = source / 'vps-billing'
        for name in ('LICENSE', 'FRONTEND_PAIR.json'):
            put(files, name, read(source / name))
        for name in ('go.mod', 'go.sum'):
            put(files, 'manifests/' + name, read(project / 'backend' / name))
        for origin, prefix in ((project / 'backend/migrations', 'migrations'),
                               (project / 'docs', 'docs'), (project / 'deploy/systemd', 'deploy/systemd')):
            paths = sorted(path for path in origin.rglob('*') if path.is_file())
            require(paths, 'resource_directory_empty')
            for path in paths:
                if prefix == 'migrations' and path.suffix != '.sql':
                    continue
                put(files, prefix + '/' + path.relative_to(origin).as_posix(), read(path))
        put(files, 'deploy/nginx.native.conf', read(project / 'deploy/nginx.native.conf'))
        for name in SCRIPTS:
            put(files, 'deploy/scripts/' + name, read(project / 'deploy/scripts' / name))
        compile_backend(source, files, scratch)
        require(all(read(project / 'backend' / name) == files['manifests/' + name] for name in ('go.mod', 'go.sum')),
                'module_inputs_changed_during_build')
    # Do not allow a concurrent worktree edit to change the source claim.
    require(git_source(args.source_sha) == epoch and PAIR.load_pin() == pin, 'source_changed_during_build')
    complete_inventory(files, pin)
    version = pin['core_version']
    payload = compress(archive_bytes(files, epoch), epoch)
    name = 'aster-finance-backend-' + version + '-linux-amd64.tar.gz'
    manifest = {'schema': 1, 'kind': 'finance-backend', 'repository': REPOSITORY, 'repository_id': REPOSITORY_ID,
                'version': version, 'source_commit': args.source_sha, 'source_date_epoch': epoch,
                'format': 'backend-tar-v1', 'frontend_pair': pin,
                'asset': {'name': name, 'size': len(payload), 'sha256': digest(payload)},
                'files': [{'path': path, 'size': len(raw), 'sha256': digest(raw)} for path, raw in sorted(files.items())]}
    encoded = encode(manifest)
    require(len(encoded) <= MAX_MANIFEST, 'manifest_bound')
    output.mkdir(mode=0o700)
    for filename, raw in (('BACKEND.json', encoded), (name, payload)):
        with (output / filename).open('xb') as out:
            out.write(raw)
    return {'status': 'built', 'authenticated_source': False, 'installable_release': False,
            'files': len(files), 'asset': name}


def inspect(args):
    raw_manifest = PAIR.regular(args.manifest, MAX_MANIFEST)
    value = PAIR.decode(raw_manifest)
    require(isinstance(value, dict) and set(value) == {'schema', 'kind', 'repository', 'repository_id', 'version',
            'source_commit', 'source_date_epoch', 'format', 'frontend_pair', 'asset', 'files'}, 'manifest_schema_mismatch')
    require(type(value['schema']) is int and value['schema'] == 1 and value['kind'] == 'finance-backend' and
            value['repository'] == REPOSITORY and type(value['repository_id']) is int and value['repository_id'] == REPOSITORY_ID and
            value['format'] == 'backend-tar-v1' and isinstance(args.expected_source_sha, str) and
            re.fullmatch(PAIR.SHA, args.expected_source_sha) and value['source_commit'] == args.expected_source_sha,
            'backend_identity_mismatch')
    pin = PAIR.load_pin()
    require(encode(pin) == encode(value['frontend_pair']) and pin['source_sha'] == args.expected_frontend_sha and
            value['version'] == pin['core_version'], 'paired_frontend_mismatch')
    epoch = value['source_date_epoch']
    require(type(epoch) is int and 946684800 <= epoch <= 4294967295, 'invalid_epoch')
    asset = value['asset']
    expected_name = 'aster-finance-backend-' + pin['core_version'] + '-linux-amd64.tar.gz'
    require(isinstance(asset, dict) and set(asset) == {'name', 'size', 'sha256'} and asset['name'] == expected_name and
            type(asset['size']) is int and 0 < asset['size'] <= MAX_ARCHIVE, 'asset_schema_mismatch')
    payload = PAIR.regular(args.archive, MAX_ARCHIVE)
    require(len(payload) == asset['size'] and digest(payload) == asset['sha256'], 'asset_digest_mismatch')
    decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
    raw = decoder.decompress(payload, MAX_RAW + 1)
    require(len(raw) <= MAX_RAW and decoder.eof and not decoder.unused_data and not decoder.unconsumed_tail,
            'bounded_single_gzip_required')
    files = {}
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:') as archive:
        for entry in archive:
            require(entry.isfile() and not entry.pax_headers and 0 <= entry.size <= MAX_FILE,
                    'regular_archive_entry_required')
            put(files, entry.name, archive.extractfile(entry).read())
    require(raw == archive_bytes(files, epoch), 'canonical_archive_required')
    inventory = [{'path': name, 'size': len(data), 'sha256': digest(data)} for name, data in sorted(files.items())]
    require(encode(value['files']) == encode(inventory), 'exact_inventory_required')
    complete_inventory(files, pin)
    return {'status': 'content_verified', 'authenticated_source': False, 'installable_release': False,
            'source_commit': value['source_commit'], 'frontend_source_commit': pin['source_sha'], 'files': len(files)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest='action', required=True)
    builder = actions.add_parser('build')
    builder.add_argument('--source-sha', required=True)
    builder.add_argument('--output', type=Path, required=True)
    checker = actions.add_parser('inspect')
    checker.add_argument('--manifest', type=Path, required=True)
    checker.add_argument('--archive', type=Path, required=True)
    checker.add_argument('--expected-source-sha', required=True)
    checker.add_argument('--expected-frontend-sha', required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(build(args) if args.action == 'build' else inspect(args), sort_keys=True))
        return 0
    except PAIR.Rejected as error:
        print('Backend bundle refused: ' + str(error), file=sys.stderr)
    except (OSError, ValueError, UnicodeError, RecursionError, tarfile.TarError, zlib.error, subprocess.SubprocessError):
        print('Backend bundle refused: source, build or archive validation failed.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
