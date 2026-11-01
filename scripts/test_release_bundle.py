#!/usr/bin/env python3
"""Independent release trust boundaries and real bundle assembly; no publication."""
import argparse
import base64
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


policy = load('release_policy_tests', ROOT / 'scripts/release_bundle.py')
fixtures = load('release_pair_fixtures', ROOT / 'scripts/test_pair_runtime.py')
updater = load('release_update_tests', ROOT / 'vps-billing/deploy/scripts/update-manager.py')


class ReleasePolicy(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.PairedRuntimeCLI()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.pin = self.fixture.backend.pin
        self.source = self.fixture.backend.sha
        self.files = {p.name: p.read_bytes() for directory in (self.fixture.backend.output, self.fixture.frontend) for p in directory.iterdir()}
        self.files['BUILD.json'] = policy.B.encode(policy.build_manifest(self.files, self.source, self.pin))
        self.env = dict(GITHUB_REPOSITORY=policy.REPOSITORY, GITHUB_REPOSITORY_ID=str(policy.REPOSITORY_ID),
                        GITHUB_SHA=self.source, GITHUB_WORKFLOW_SHA=self.source,
                        GITHUB_WORKFLOW_REF=policy.REPOSITORY + '/' + policy.WORKFLOW + '@refs/tags/v0.5.10',
                        GITHUB_REF='refs/tags/v0.5.10', GITHUB_JOB='release', RUNNER_ENVIRONMENT='github-hosted',
                        GITHUB_EVENT_NAME='push', GITHUB_RUN_ID='900', GITHUB_RUN_ATTEMPT='2')
        self.proof = policy.provenance(self.files, self.source, self.pin, self.env)
        self.files['PROVENANCE.json'] = policy.B.encode(self.proof)
        self.files['SHA256SUMS'] = ''.join(policy.B.digest(raw) + '  ' + name + '\n' for name, raw in sorted(self.files.items())).encode()
        self.intent = dict(component='core', version='0.5.10', tag='v0.5.10', source_sha=self.source, run_id=900,
                           run_attempt=2, distribution_format=policy.FORMAT, assets=[dict(name=name, id=i+1, size=len(raw),
                           sha256=policy.B.digest(raw)) for i, (name, raw) in enumerate(sorted(self.files.items()))])
        repo = dict(id=policy.REPOSITORY_ID, full_name=policy.REPOSITORY)
        self.run = dict(id=900, run_attempt=2, head_sha=self.source, path=policy.WORKFLOW, workflow_id=4,
                        repository=repo, head_repository=repo, head_branch='v0.5.10', event='push', status='completed', conclusion='success')
        self.jobs = dict(total_count=1, jobs=[dict(name='release', status='completed', conclusion='success',
                                                head_sha=self.source, run_id=900, run_attempt=2)])
        self.workflow = dict(id=4, path=policy.WORKFLOW, state='active')
        raw = policy.B.encode(self.pin)
        self.source_pin = dict(type='file', encoding='base64', size=len(raw), content=base64.b64encode(raw).decode(),
                               sha=hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest())
        self.api = mock.Mock()
        def request(path):
            if path == '/contents/FRONTEND_PAIR.json?ref=' + self.source:
                return self.source_pin
            if path == '/actions/workflows/release.yml':
                return self.workflow
            if path == '/actions/runs/900/attempts/2/jobs?per_page=100':
                return self.jobs
            if path == '/actions/runs/900/attempts/2':
                return self.run
            raise AssertionError('Unexpected API path: ' + path)
        self.api.json.side_effect = request
        self.api.frontend_repository.return_value = dict(id=policy.P.FRONTEND_ID, full_name=policy.P.FRONTEND,
                                                        private=False, default_branch='main')

    def test_signed_pair_assembles_runtime_without_executing_members(self):
        bundle = policy.verify_release(self.api, self.intent, self.files)
        self.assertIn('web/index.html', bundle)
        self.assertNotIn('dist/index.html', bundle)
        self.assertEqual(bundle['manifests/FRONTEND.json'][0], self.files['FRONTEND.json'])
        self.assertEqual(bundle['bin/aster-api'][1], 0o755)
        self.assertEqual(bundle['web/index.html'][1], 0o644)
        self.assertEqual(self.api.attestation.call_count, 7)
        self.assertTrue(updater.asset_inventory(self.intent['assets'], 'core', '0.5.10', policy.FORMAT))

    def test_wrong_repo_workflow_job_attempt_event_and_missing_signature_fail_closed(self):
        cases = [(self.run, 'run_attempt', 1), (self.run, 'head_sha', 'f'*40), (self.run, 'path', '.github/workflows/ci.yml'),
                 (self.run, 'event', 'workflow_dispatch'), (self.run, 'head_branch', 'v0.5.11'),
                 (self.jobs['jobs'][0], 'name', 'release-other'), (self.jobs['jobs'][0], 'conclusion', 'skipped'),
                 (self.run['repository'], 'id', 1418618420), (self.workflow, 'id', 99),
                 (self.api.frontend_repository.return_value, 'id', 1418618420)]
        for target, key, value in cases:
            old = target[key]
            with self.subTest(key=key, value=value):
                target[key] = value
                with self.assertRaises(policy.P.Rejected):
                    policy.verify_release(self.api, self.intent, self.files)
            target[key] = old
        self.api.attestation.side_effect = policy.P.Rejected('signature_rejected')
        with self.assertRaises(policy.P.Rejected):
            policy.verify_release(self.api, self.intent, self.files)

    def test_dry_runs_are_never_accepted_as_releases(self):
        proof = dict(self.proof, event='workflow_dispatch', dry_run=True)
        self.files['PROVENANCE.json'] = policy.B.encode(proof)
        with self.assertRaises(policy.P.Rejected):
            policy.verify_release(self.api, self.intent, self.files)
        for key, value in [('GITHUB_REPOSITORY_ID', '1418618420'), ('GITHUB_JOB', 'other'),
                           ('GITHUB_EVENT_NAME', 'pull_request'), ('GITHUB_REF', 'refs/tags/v0.5.11'),
                           ('GITHUB_WORKFLOW_SHA', 'f'*40), ('RUNNER_ENVIRONMENT', 'self-hosted')]:
            with self.subTest(key=key), self.assertRaises(policy.P.Rejected):
                policy.provenance({}, self.source, self.pin, dict(self.env, **{key:value}))

    def test_pair_is_bound_to_tagged_source_and_digest(self):
        self.source_pin['content'] = base64.b64encode(policy.B.encode(dict(self.pin, archive_sha256='f'*64))).decode()
        with self.assertRaises(policy.P.Rejected):
            policy.verify_release(self.api, self.intent, self.files)
        name = 'aster-finance-frontend-0.5.10.tar.gz'
        self.files[name] += b'tampered'
        with self.assertRaises(policy.P.Rejected):
            policy.content(self.files, self.source, self.pin)

    def test_artifact_download_requires_exact_id_run_digest_and_bounded_flat_zip(self):
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w') as archive:
            for name in ('FRONTEND.json', 'aster-finance-frontend-0.5.10.tar.gz'):
                archive.writestr(name, self.files[name])
        payload = output.getvalue()
        pin = dict(self.pin, artifact_sha256=policy.B.digest(payload))
        artifact = dict(id=pin['artifact_id'], name='finance-frontend-' + pin['source_sha'] + '-1', expired=False,
                        size_in_bytes=len(payload), digest='sha256:' + pin['artifact_sha256'],
                        workflow_run=dict(id=pin['run_id'], head_sha=pin['source_sha'], repository_id=1435344023, head_repository_id=1435344023))
        self.assertEqual(set(policy.frontend_artifact(pin, artifact, payload)), {'FRONTEND.json','aster-finance-frontend-0.5.10.tar.gz'})
        for key, value in [('id', 7), ('expired', True), ('name', 'latest'), ('digest', 'sha256:' + '0'*64)]:
            with self.subTest(key=key), self.assertRaises(policy.P.Rejected):
                policy.frontend_artifact(pin, dict(artifact, **{key:value}), payload)
        artifact['workflow_run']['repository_id'] = 1418618420
        with self.assertRaises(policy.P.Rejected):
            policy.frontend_artifact(pin, artifact, payload)

    def test_verifier_uses_exact_flags_and_refuses_failed_crypto_before_json(self):
        with mock.patch.object(policy.subprocess, 'run', return_value=argparse.Namespace(returncode=1)) as run:
            with self.assertRaises(policy.P.Rejected):
                policy.verify_attestation('BUILD.json', self.files['BUILD.json'], self.proof)
            args = run.call_args.args[0]
            for value in ('/usr/bin/gh', '--signer-workflow', '--source-digest', '--signer-digest', '--source-ref',
                          '--deny-self-hosted-runners', self.source, 'refs/tags/v0.5.10'):
                self.assertIn(value, args)
            self.assertNotIn('--custom-trusted-root', args)

    def test_attestation_certificate_id_and_run_cannot_be_supplied_by_predicate(self):
        ref = 'refs/tags/v0.5.10'
        signer = 'https://github.com/' + policy.REPOSITORY + '/' + policy.WORKFLOW + '@' + ref
        cert = dict(sourceRepositoryIdentifier='1435344087', sourceRepositoryOwnerIdentifier='123864154',
                    sourceRepositoryURI='https://github.com/' + policy.REPOSITORY, sourceRepositoryDigest=self.source,
                    sourceRepositoryRef=ref, buildSignerURI=signer, buildSignerDigest=self.source,
                    buildConfigURI=signer, buildConfigDigest=self.source,
                    runInvocationURI='https://github.com/' + policy.REPOSITORY + '/actions/runs/900/attempts/2',
                    runnerEnvironment='github-hosted', buildTrigger='push')
        result = [dict(verificationResult=dict(signature=dict(certificate=cert), verifiedTimestamps=[{}],
                  statement=dict(predicateType='https://slsa.dev/provenance/v1', subject=[dict(name='BUILD.json',
                                 digest=dict(sha256=policy.B.digest(self.files['BUILD.json'])))])))]
        policy.attestation_identity(result, self.proof, 'BUILD.json', self.files['BUILD.json'])
        for key in cert:
            bad = copy.deepcopy(result)
            bad[0]['verificationResult']['signature']['certificate'][key] = 'wrong'
            with self.subTest(key=key), self.assertRaises(policy.P.Rejected):
                policy.attestation_identity(bad, self.proof, 'BUILD.json', self.files['BUILD.json'])

    def test_workflows_pin_actions_and_keep_pr_and_dispatch_unpublished(self):
        release = (ROOT / '.github/workflows/release.yml').read_text()
        dry = (ROOT / '.github/workflows/release-tooling.yml').read_text()
        self.assertNotIn('pull_request:', release)
        self.assertIn("tags: ['v*']", release)
        self.assertEqual(release.count("if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')"), 2)
        self.assertNotIn('release create', dry)
        self.assertNotIn('id-token: write', dry)
        import re
        for uses in re.findall(r'uses: (.+)', release + dry):
            self.assertRegex(uses, r'^[\w/-]+@[a-f0-9]{40}$')


if __name__ == '__main__':
    unittest.main()
