#!/usr/bin/env python3
"""Independent release content and provenance policy. Never execute bundle members."""
import argparse
import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


R = load('release_runtime', ROOT / 'scripts/pair_runtime.py')
B, P = R.B, R.P
require = P.require
REPOSITORY, REPOSITORY_ID = B.REPOSITORY, B.REPOSITORY_ID
WORKFLOW, JOB = '.github/workflows/release.yml', 'release'
FORMAT = 'paired-bundles-v1'
FRONTEND_WORKFLOW, FRONTEND_JOB = '.github/workflows/ci.yml', 'frontend'


def gh(*args, binary=False):
    result = subprocess.run(['gh', *args], stdin=subprocess.DEVNULL, capture_output=True, timeout=120)
    require(result.returncode == 0, 'github_request_failed')
    require(len(result.stdout) <= 192 * 1024 * 1024, 'github_response_bound')
    return result.stdout if binary else P.decode(result.stdout)


def api(path):
    # Only call sites in this policy choose endpoints; no user-supplied URL/repo.
    return gh('api', path)


def repository(metadata, frontend=False):
    name, identifier = (P.FRONTEND, P.FRONTEND_ID) if frontend else (REPOSITORY, REPOSITORY_ID)
    require(isinstance(metadata, dict) and type(metadata.get('id')) is int and metadata['id'] == identifier and
            metadata.get('full_name') == name and metadata.get('private') is False and
            metadata.get('default_branch') == 'main', 'repository_identity_changed')


def run_identity(run, workflow, jobs, source, run_id, attempt, frontend=False, completed=True):
    name, identifier = (P.FRONTEND, P.FRONTEND_ID) if frontend else (REPOSITORY, REPOSITORY_ID)
    path, job = (FRONTEND_WORKFLOW, FRONTEND_JOB) if frontend else (WORKFLOW, JOB)
    require(run.get('id') == run_id and run.get('run_attempt') == attempt and run.get('head_sha') == source and
            run.get('path') == path and run.get('workflow_id') == workflow.get('id') and
            type(workflow.get('id')) is int and workflow['id'] > 0 and workflow.get('path') == path and
            workflow.get('state') == 'active', 'workflow_identity_mismatch')
    for key in ('repository', 'head_repository'):
        require(run.get(key, {}).get('id') == identifier and run[key].get('full_name') == name, 'run_repository_mismatch')
    if frontend:
        require(run.get('head_branch') == 'main' and run.get('event') in {'push', 'workflow_dispatch'}, 'frontend_event_mismatch')
    else:
        require(run.get('event') == 'push' and re.fullmatch('v' + P.VERSION, run.get('head_branch', '')), 'release_event_mismatch')
    if completed:
        require(run.get('status') == 'completed' and run.get('conclusion') == 'success', 'run_not_successful')
        require(jobs.get('total_count') == len(jobs.get('jobs', [])) and
                len(jobs['jobs']) <= 100, 'jobs_incomplete')
        selected = [item for item in jobs['jobs'] if item.get('name') == job]
        require(len(selected) == 1 and selected[0].get('conclusion') == 'success' and
                selected[0].get('status') == 'completed' and selected[0].get('head_sha') == source and
                selected[0].get('run_id') == run_id and selected[0].get('run_attempt') == attempt,
                'exact_job_not_successful')


def frontend_artifact(pin, artifact, payload):
    require(artifact.get('id') == pin['artifact_id'] and artifact.get('name') ==
            'finance-frontend-' + pin['source_sha'] + '-' + str(pin['run_attempt']) and artifact.get('expired') is False and
            artifact.get('digest') == 'sha256:' + pin['artifact_sha256'] and
            artifact.get('size_in_bytes') == len(payload) and B.digest(payload) == pin['artifact_sha256'], 'artifact_identity_mismatch')
    linked = artifact.get('workflow_run', {})
    require(linked.get('id') == pin['run_id'] and linked.get('head_sha') == pin['source_sha'] and
            linked.get('repository_id') == P.FRONTEND_ID and linked.get('head_repository_id') == P.FRONTEND_ID,
            'artifact_run_mismatch')
    name = 'aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz'
    with zipfile.ZipFile(io.BytesIO(payload)) as archive:
        entries = archive.infolist()
        require(len(entries) == 2 and {item.filename for item in entries} == {'FRONTEND.json', name} and
                all(not item.is_dir() and not item.flag_bits & 1 and item.file_size <= B.MAX_ARCHIVE and
                    (item.external_attr >> 16) & 0o170000 in {0, 0o100000} for item in entries), 'artifact_zip_invalid')
        files = {item.filename: archive.read(item) for item in entries}
    require(B.digest(files['FRONTEND.json']) == pin['manifest_sha256'] and B.digest(files[name]) == pin['archive_sha256'],
            'frontend_pin_digest_mismatch')
    return files


def fetch_frontend(output):
    pin = P.load_pin()
    prefix = 'repos/' + P.FRONTEND
    repository(api(prefix), frontend=True)
    run_identity(api(prefix + '/actions/runs/' + str(pin['run_id']) + '/attempts/' + str(pin['run_attempt'])),
                 api(prefix + '/actions/workflows/ci.yml'),
                 api(prefix + '/actions/runs/' + str(pin['run_id']) + '/attempts/' + str(pin['run_attempt']) + '/jobs?per_page=100'),
                 pin['source_sha'], pin['run_id'], pin['run_attempt'], frontend=True)
    endpoint = prefix + '/actions/artifacts/' + str(pin['artifact_id'])
    metadata = api(endpoint)
    payload = gh('api', endpoint + '/zip', binary=True)
    files = frontend_artifact(pin, metadata, payload)
    output.mkdir(mode=0o700)
    for name, raw in files.items():
        (output / name).write_bytes(raw)
    R.checked_frontend(argparse.Namespace(frontend_manifest=output / 'FRONTEND.json',
                                         frontend_archive=output / ('aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz')), pin)


def names(version, pin):
    P.validate_pin(pin)
    require(version == pin['core_version'], 'release_version_mismatch')
    return {'BUILD.json', 'BACKEND.json', 'FRONTEND.json', 'PROVENANCE.json', 'SHA256SUMS',
            'aster-finance-backend-' + version + '-linux-amd64.tar.gz',
            'aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz'}


def content(files, source, pin):
    """Inspect captured bytes with the reviewed parsers, then assemble a runtime."""
    P.validate_pin(pin)
    with tempfile.TemporaryDirectory(prefix='aster-release-content-') as directory:
        root = Path(directory).resolve()
        for name in ('BACKEND.json', 'FRONTEND.json', 'aster-finance-backend-' + pin['core_version'] + '-linux-amd64.tar.gz',
                     'aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz'):
            require(name in files, 'component_asset_missing')
            (root / name).write_bytes(files[name])
        back, backend_files, back_raw = B.verified_content(argparse.Namespace(
            manifest=root / 'BACKEND.json', archive=root / ('aster-finance-backend-' + pin['core_version'] + '-linux-amd64.tar.gz'),
            expected_source_sha=source, expected_frontend_sha=pin['source_sha']), pin=pin)
        front, frontend_files, front_raw = R.checked_frontend(argparse.Namespace(
            frontend_manifest=root / 'FRONTEND.json', frontend_archive=root / ('aster-finance-frontend-' + pin['frontend_version'] + '.tar.gz')), pin)
    R.shape_identity(back, front, pin, source)
    expected = R.expected_inventory(back, front, back_raw, front_raw)
    bundle = dict(backend_files)
    for name, raw in frontend_files.items():
        target = R.frontend_target(name)
        require(target not in bundle, 'runtime_path_collision')
        bundle[target] = raw
    bundle.update({'manifests/BACKEND.json': back_raw, 'manifests/FRONTEND.json': front_raw})
    require(expected == [{'path': name, 'size': len(raw), 'sha256': B.digest(raw)} for name, raw in sorted(bundle.items())],
            'runtime_inventory_mismatch')
    return bundle


def build_manifest(files, source, pin):
    back = P.decode(files['BACKEND.json'])
    return dict(schema=4, distribution_format=FORMAT, release_kind='core', release_label=pin['core_version'],
                source_commit=source, target='linux/amd64', cgo_enabled=False, frontend_pair=pin,
                component_versions={'core': pin['core_version']},
                binary_sha256={item['path']: item['sha256'] for item in back['files'] if item['path'] in B.BINS})


def provenance(files, source, pin, env):
    require(env.get('GITHUB_REPOSITORY') == REPOSITORY and env.get('GITHUB_REPOSITORY_ID') == str(REPOSITORY_ID) and
            env.get('GITHUB_SHA') == source and env.get('GITHUB_WORKFLOW_SHA') == source and
            env.get('GITHUB_WORKFLOW_REF') == REPOSITORY + '/' + WORKFLOW + '@' + env.get('GITHUB_REF', '') and
            env.get('GITHUB_JOB') == JOB and env.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'release_environment_mismatch')
    dry = env.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
    require(dry or env.get('GITHUB_EVENT_NAME') == 'push' and env.get('GITHUB_REF') == 'refs/tags/v' + pin['core_version'],
            'release_event_ref_mismatch')
    for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT'):
        require(re.fullmatch(r'[1-9][0-9]{0,15}', env.get(key, '')), 'run_identity_required')
    return dict(schema=2, repository=REPOSITORY, repository_id=REPOSITORY_ID, source_sha=source,
                workflow_path=WORKFLOW, workflow_sha=source, job=JOB, event=env['GITHUB_EVENT_NAME'],
                tag='v' + pin['core_version'], version=pin['core_version'],
                run_id=int(env['GITHUB_RUN_ID']), run_attempt=int(env['GITHUB_RUN_ATTEMPT']), dry_run=dry,
                frontend_pair=pin, assets={name: {'size': len(raw), 'sha256': B.digest(raw)} for name, raw in sorted(files.items())})


def release_gate(source, dry_run):
    require(re.fullmatch(P.SHA, source), 'release_source_required')
    prefix = 'repos/' + REPOSITORY
    repository(api(prefix))
    require(P.git(ROOT, 'rev-parse', 'HEAD') == source, 'checkout_source_mismatch')
    if dry_run:
        return
    ancestry = api(prefix + '/compare/' + source + '...main')
    require(ancestry.get('status') in {'ahead', 'identical'} and ancestry.get('merge_base_commit', {}).get('sha') == source,
            'release_source_not_on_main')
    result = api(prefix + '/actions/workflows/ci.yml/runs?head_sha=' + source + '&branch=main&status=success&per_page=100')
    required = {'Go and real MySQL (ubuntu-22.04)', 'Go and real MySQL (ubuntu-24.04)',
                'vulnerability', 'Real MySQL financial browser acceptance'}
    for run in result.get('workflow_runs', []):
        if run.get('head_sha') != source or run.get('head_branch') != 'main' or run.get('event') not in {'push', 'workflow_dispatch'} or \
                run.get('path') != '.github/workflows/ci.yml' or run.get('conclusion') != 'success' or \
                any(run.get(key, {}).get('id') != REPOSITORY_ID for key in ('repository', 'head_repository')):
            continue
        jobs = api(prefix + '/actions/runs/' + str(run['id']) + '/attempts/' + str(run['run_attempt']) + '/jobs?per_page=100')
        if jobs.get('total_count') == len(jobs.get('jobs', [])) and required <= {job.get('name') for job in jobs['jobs']
                if job.get('conclusion') == 'success' and job.get('status') == 'completed' and job.get('head_sha') == source}:
            return
    raise P.Rejected('exact_source_backend_ci_required')


def seal(directory, source):
    pin = P.load_pin()
    release_gate(source, os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch')
    expected = names(pin['core_version'], pin) - {'BUILD.json', 'PROVENANCE.json', 'SHA256SUMS'}
    require({p.name for p in directory.iterdir()} == expected, 'candidate_inventory_mismatch')
    files = {name: P.regular(directory / name, B.MAX_ARCHIVE) for name in expected}
    content(files, source, pin)
    files['BUILD.json'] = B.encode(build_manifest(files, source, pin))
    proof = provenance(files, source, pin, os.environ)
    files['PROVENANCE.json'] = B.encode(proof)
    files['SHA256SUMS'] = ''.join(B.digest(raw) + '  ' + name + '\n' for name, raw in sorted(files.items())).encode()
    for name in ('BUILD.json', 'PROVENANCE.json', 'SHA256SUMS'):
        with (directory / name).open('xb') as stream:
            stream.write(files[name])


def attestation_identity(results, proof, name, raw):
    """Only called with successful gh cryptographic verification output."""
    require(isinstance(results, list) and 1 <= len(results) <= 30, 'attestation_result_missing')
    ref = 'refs/tags/' + proof['tag']
    signer = 'https://github.com/' + REPOSITORY + '/' + WORKFLOW + '@' + ref
    invocation = 'https://github.com/' + REPOSITORY + '/actions/runs/' + str(proof['run_id']) + '/attempts/' + str(proof['run_attempt'])
    for result in results:
        verified = result.get('verificationResult', {})
        cert = verified.get('signature', {}).get('certificate', {})
        statement = verified.get('statement', {})
        expected = dict(sourceRepositoryIdentifier=str(REPOSITORY_ID), sourceRepositoryOwnerIdentifier=str(P.OWNER_ID),
                        sourceRepositoryURI='https://github.com/' + REPOSITORY, sourceRepositoryDigest=proof['source_sha'],
                        sourceRepositoryRef=ref, buildSignerURI=signer, buildSignerDigest=proof['source_sha'],
                        buildConfigURI=signer, buildConfigDigest=proof['source_sha'], runInvocationURI=invocation,
                        runnerEnvironment='github-hosted', buildTrigger='push')
        if all(cert.get(key) == value for key, value in expected.items()) and verified.get('verifiedTimestamps') and \
                statement.get('predicateType') == 'https://slsa.dev/provenance/v1' and \
                any(item.get('name') == name and item.get('digest') == {'sha256': B.digest(raw)} for item in statement.get('subject', [])):
            return
    raise P.Rejected('attestation_certificate_identity_mismatch')


def verify_attestation(name, raw, proof, token=''):
    # /usr/bin/gh is an operator-installed verifier, never a downloaded bundle program.
    # gh verifies the Sigstore signature, certificate chain and transparency timestamp.
    with tempfile.TemporaryDirectory(prefix='aster-signature-') as directory:
        path = Path(directory) / name
        path.write_bytes(raw)
        args = ['/usr/bin/gh', 'attestation', 'verify', str(path), '--repo', REPOSITORY,
                '--signer-workflow', REPOSITORY + '/' + WORKFLOW,
                '--source-digest', proof['source_sha'], '--signer-digest', proof['source_sha'],
                '--source-ref', 'refs/tags/' + proof['tag'], '--deny-self-hosted-runners',
                '--cert-oidc-issuer', 'https://token.actions.githubusercontent.com',
                '--predicate-type', 'https://slsa.dev/provenance/v1', '--format', 'json']
        env = {'PATH': '/usr/bin:/bin', 'HOME': directory, 'GH_CONFIG_DIR': directory,
               'GH_PROMPT_DISABLED': '1', 'GH_HOST': 'github.com'}
        if token:
            env['GH_TOKEN'] = token
        with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
            result = subprocess.run(args, env=env, stdin=subprocess.DEVNULL, stdout=output, stderr=errors, timeout=120)
            require(result.returncode == 0, 'artifact_signature_invalid')
            output.seek(0)
            value = output.read(4 * 1024 * 1024 + 1)
        require(len(value) <= 4 * 1024 * 1024, 'attestation_result_bound')
        attestation_identity(P.decode(value), proof, name, raw)


def verify_release(api, intent, files):
    """Network identity + signatures + content; return exact bytes for the host."""
    proof = P.decode(files['PROVENANCE.json'])
    pin = P.validate_pin(proof['frontend_pair'])
    expected = names(intent['version'], pin)
    require(set(files) == expected, 'release_assets_mismatch')
    source = intent['source_sha']
    require(re.fullmatch(P.SHA, source) and proof == dict(schema=2, repository=REPOSITORY, repository_id=REPOSITORY_ID,
            source_sha=source, workflow_path=WORKFLOW, workflow_sha=source, job=JOB, event='push', dry_run=False,
            tag=intent['tag'], version=intent['version'], run_id=intent['run_id'], run_attempt=intent['run_attempt'],
            frontend_pair=pin, assets={name: {'size': len(raw), 'sha256': B.digest(raw)} for name, raw in files.items()
                                      if name not in {'PROVENANCE.json', 'SHA256SUMS'}}), 'release_provenance_mismatch')
    require(intent['tag'] == 'v' + pin['core_version'], 'release_tag_mismatch')
    run_prefix = '/actions/runs/' + str(proof['run_id']) + '/attempts/' + str(proof['run_attempt'])
    run_identity(api.json(run_prefix), api.json('/actions/workflows/release.yml'),
                 api.json(run_prefix + '/jobs?per_page=100'), source, proof['run_id'], proof['run_attempt'])
    require(api.json(run_prefix).get('head_branch') == intent['tag'], 'run_tag_mismatch')
    repository(api.frontend_repository(), frontend=True)
    source_pin = api.json('/contents/FRONTEND_PAIR.json?ref=' + source)
    require(source_pin.get('type') == 'file' and source_pin.get('encoding') == 'base64' and
            type(source_pin.get('size')) is int and 0 < source_pin['size'] <= 65536, 'source_pin_missing')
    pin_raw = base64.b64decode(source_pin['content'].replace('\n', ''), validate=True)
    require(len(pin_raw) == source_pin['size'] and P.decode(pin_raw) == pin and
            hashlib.sha1(b'blob ' + str(len(pin_raw)).encode() + b'\0' + pin_raw).hexdigest() == source_pin.get('sha'),
            'source_pin_mismatch')
    for name, raw in sorted(files.items()):
        api.attestation(name, raw, proof)
    require(P.decode(files['BUILD.json']) == build_manifest(files, source, pin), 'build_manifest_mismatch')
    bundle = content(files, source, pin)
    bundle['manifests/BUILD.json'] = files['BUILD.json']
    return {name: (raw, 0o755 if name in B.BINS or name.startswith('deploy/scripts/') else 0o644)
            for name, raw in bundle.items()}


def assemble_candidate(directory, output, source):
    require(os.geteuid() != 0, 'unprivileged_candidate_required')
    pin = P.load_pin()
    files = {name: P.regular(directory / name, B.MAX_ARCHIVE) for name in names(pin['core_version'], pin)
             if name not in {'BUILD.json', 'PROVENANCE.json', 'SHA256SUMS'}}
    bundle = content(files, source, pin)
    bundle['manifests/BUILD.json'] = B.encode(build_manifest(files, source, pin))
    require(output.is_absolute() and output.parent.resolve(strict=True) == output.parent and not output.is_symlink(),
            'canonical_candidate_output_required')
    output.mkdir(mode=0o700)
    for name, raw in bundle.items():
        path = output / name
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open('xb') as stream:
            stream.write(raw)
        path.chmod(0o755 if name in B.BINS or name.startswith('deploy/scripts/') else 0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    fetch = sub.add_parser('fetch-frontend')
    fetch.add_argument('--output', type=Path, required=True)
    sealing = sub.add_parser('seal')
    sealing.add_argument('--directory', type=Path, required=True)
    sealing.add_argument('--source', required=True)
    assembling = sub.add_parser('assemble-candidate')
    assembling.add_argument('--directory', type=Path, required=True)
    assembling.add_argument('--output', type=Path, required=True)
    assembling.add_argument('--source', required=True)
    args = parser.parse_args()
    try:
        if args.action == 'fetch-frontend':
            fetch_frontend(args.output)
        elif args.action == 'seal':
            seal(args.directory, args.source)
        else:
            assemble_candidate(args.directory, args.output, args.source)
        print('Release content verified; no publication performed.')
    except (P.Rejected, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, zipfile.BadZipFile):
        print('Release rejected: immutable source, workflow, digest or content check failed.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
