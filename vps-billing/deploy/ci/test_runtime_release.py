#!/usr/bin/env python3
"""Binary-only core/plugin publication checks; all GitHub operations are mocked."""
import base64
import copy
from pathlib import Path
import unittest
from unittest import mock
from test_release_pipeline import (
    pipeline, flow_fixture, SHA, DEFAULT_SHA, PREFIX, RELEASE_ID, RUN_ID,
    json_bytes, tar_bytes, zip_bytes, refresh_checksums, git_blob_sha,
)


def elf_bytes():
    data = bytearray(120)
    data[:7] = b'\x7fELF\x02\x01\x01'
    data[16:18] = (2).to_bytes(2, 'little')
    data[18:20] = (62).to_bytes(2, 'little')
    data[20:24] = (1).to_bytes(4, 'little')
    data[24:32] = (0x400040).to_bytes(8, 'little')
    data[52:54] = (64).to_bytes(2, 'little')
    data[32:40] = (64).to_bytes(8, 'little')
    data[54:56] = (56).to_bytes(2, 'little')
    data[56:58] = (1).to_bytes(2, 'little')
    data[64:68] = (1).to_bytes(4, 'little')
    data[68:72] = (5).to_bytes(4, 'little')
    data[80:88] = (0x400000).to_bytes(8, 'little')
    data[96:104] = (len(data)).to_bytes(8, 'little')
    data[104:112] = (len(data)).to_bytes(8, 'little')
    data[112:120] = (4096).to_bytes(8, 'little')
    return bytes(data)


def runtime_fixture(kind='core', binary_override=None, build_override=None, remove_members=(), raw=False, protocol_source=None, version=None, migration_names=None):
    version = version or (('0.5.2' if kind == 'core' else '1.1.0') if raw else ('0.4.0' if kind == 'core' else '1.0.0'))
    core_version = version if kind == 'core' else ('0.5.2' if raw else '0.4.0')
    tag = ('v' if kind == 'core' else 'smtp-v') + version
    root = ('aster' if kind == 'core' else 'aster-smtp') + '-' + version + '-linux-amd64'
    if raw: root = pipeline.resources_asset(version, kind)[:-7]
    license_bytes = (Path(__file__).resolve().parents[3] / 'LICENSE').read_bytes()
    inputs = {path: ('input fixture ' + path + '\n').encode() for path in pipeline.CURRENT_INPUT_PATHS}
    inputs.update({'LICENSE': license_bytes, 'README.md': '# updated repository README\n'.encode(),
                   'vps-billing/backend/pkg/pluginapi/protocol.go': b'package pluginapi\nconst CoreVersion = "0.4.0"\n',
                   'vps-billing/backend/plugins/smtp/VERSION': b'1.0.0\n'})
    if raw:
        inputs['vps-billing/backend/pkg/pluginapi/protocol.go'] = f'package pluginapi\nconst CoreVersion = "{core_version}"\n'.encode()
        inputs['vps-billing/backend/plugins/smtp/VERSION'] = b'1.1.0\n'
    if protocol_source is not None:
        inputs['vps-billing/backend/pkg/pluginapi/protocol.go'] = protocol_source
    copies = {'LICENSE': 'LICENSE'}
    binaries = {'bin/aster-api': elf_bytes(), 'bin/aster-plugin-host': elf_bytes()} if kind == 'core' else {'plugin': elf_bytes()}
    bundle = {'LICENSE': license_bytes, **binaries}
    if kind == 'core':
        copies.update({'REPOSITORY-README.md': 'README.md', 'PROJECT-README.md': 'vps-billing/README.md'})
        for path in pipeline.core_deploy_files(version):
            copies[path] = 'vps-billing/' + path
            inputs.setdefault('vps-billing/' + path, ('deployed input ' + path + '\n').encode())
        for source in pipeline.INPUT_PATHS[2:]:
            copies['manifests/' + source.rsplit('/', 1)[-1]] = source
        inputs['vps-billing/README.md'] = b'# project fixture\n'
        if raw:
            for name in migration_names or ('001_core.sql', '007_currency_registry.sql'):
                copies['migrations/' + name] = 'vps-billing/backend/migrations/' + name
                inputs[copies['migrations/' + name]] = ('-- Reviewed fixture migration ' + name + '\n').encode()
        bundle.update({path: inputs[source] for path, source in copies.items()})
        bundle.update({'web/index.html': b'<!doctype html><title>core fixture</title>',
                       'manifests/go-dependencies.json': b'[]\n',
                       'manifests/npm-installed-dependencies.json': b'[]\n',
                       'manifests/aster-api-build.txt': b'aster-api: fixture\n',
                       'manifests/aster-plugin-host-build.txt': b'aster-plugin-host: fixture\n'})
    else:
        copies["README.md"] = "vps-billing/backend/plugins/smtp/README.md"
        inputs[copies["README.md"]] = b"# independent SMTP fixture\n"
        notice = b'Original Go runtime license fixture\n'
        bundle.update({'manifest.json': json_bytes({'schema': 1, 'id': 'email.smtp', 'version': version,
                        'protocol': 'aster.smtp/1', 'core_min': '0.4.0', 'core_max_exclusive': '0.6.0' if raw else '0.5.0',
                        'capabilities': ['smtp.send'], 'executable': 'plugin', 'sha256': pipeline.sha256(bundle['plugin'])}),
                       'README.md': b'# independent SMTP fixture\n',
                       'THIRD_PARTY_NOTICES.json': json_bytes({'schema': 1, 'notices': [{'path': 'go-runtime/LICENSE',
                       'sha256': pipeline.sha256(notice), 'text': notice.decode()}]})})
        bundle['SHA256SUMS'] = ''.join(pipeline.sha256(data) + '  ' + name + '\n' for name, data in sorted(bundle.items())).encode()
    build = {'schema': 2, 'release_kind': kind, 'release_label': version, 'source_commit': SHA,
             'source_date_epoch': 1791936000, 'target': 'linux/amd64', 'cgo_enabled': False,
             'component_versions': {'core': '0.4.0', 'smtp': '1.0.0'}, 'plugin_protocol': 'aster.smtp/1',
             'smtp_core_compatibility': {'min': '0.4.0', 'max_exclusive': '0.5.0'},
             'input_sha256': {path: pipeline.sha256(inputs[path]) for path in pipeline.CURRENT_INPUT_PATHS},
             'source_files': copies, 'binary_sha256': {path: pipeline.sha256(data) for path, data in binaries.items()}}
    if raw:
        build.update(schema=3, distribution_format="raw-binaries-v1", component_versions={"core": core_version, "smtp": "1.1.0"}, smtp_core_compatibility={"min": "0.4.0", "max_exclusive": "0.6.0"}, binary_assets=pipeline.binary_assets(version, kind), resources_asset=pipeline.resources_asset(version, kind))
    build.update(build_override or {})
    build_raw = json_bytes(build)
    if kind == 'core':
        bundle['manifests/BUILD.json'] = build_raw
    bundle.update(binary_override or {})
    for name in remove_members:
        bundle.pop(name)
    # Helper defaults chmod only aster-api; set explicit modes for each runtime binary/script.
    import io, tarfile
    raw_assets = {asset: bundle.pop(path) for path, asset in pipeline.binary_assets(version, kind).items()} if raw else {}
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w:gz', format=tarfile.GNU_FORMAT) as archive:
        for name, data in sorted(bundle.items()):
            item = tarfile.TarInfo(root + '/' + name)
            item.mode = 0o755 if name in set(binaries) | {'deploy/scripts/plugin-manager.py', 'deploy/scripts/backup-native.sh', 'deploy/scripts/update-manager.py', 'deploy/scripts/core-manager.py', 'deploy/scripts/core-operation.py'} else 0o644
            item.size = len(data)
            archive.addfile(item, io.BytesIO(data))
    files = refresh_checksums({'BUILD.json': build_raw, root + '.tar.gz': output.getvalue(), **raw_assets})
    return tag, version, files, inputs, bundle


def normal_flow(kind='core', raw=False, protocol_source=None, version=None, migration_names=None):
    api, env, event, policy, _, _ = flow_fixture()
    tag, version, files, inputs, _ = runtime_fixture(kind, raw=raw, protocol_source=protocol_source, version=version, migration_names=migration_names)
    run = api.responses[('GET', PREFIX + f'/actions/runs/{RUN_ID}')]
    old_tag = run['head_branch']
    run['head_branch'] = tag
    event['workflow_run']['head_branch'] = tag
    api.responses[('GET', PREFIX + '/git/ref/tags/' + tag)] = {'ref': 'refs/tags/' + tag, 'object': {'type': 'commit', 'sha': SHA}}
    api.responses[('GET', PREFIX + '/releases/tags/' + tag)] = pipeline.APIError(404)
    for commit in (SHA, DEFAULT_SHA):
        tree = api.responses[('GET', PREFIX + f'/git/trees/{commit}?recursive=1')]
        tree['tree'] = [item for item in tree['tree'] if item['path'] not in inputs]
        tree['tree'].extend({'path': path, 'sha': git_blob_sha(data), 'type': 'blob', 'mode': '100644', 'size': len(data)} for path, data in inputs.items())
    for path in pipeline.CURRENT_INPUT_PATHS:
        data = inputs[path]; digest = git_blob_sha(data)
        api.responses[('GET', PREFIX + '/git/blobs/' + digest)] = {'sha': digest, 'size': len(data), 'encoding': 'base64', 'content': base64.b64encode(data).decode()}
    policy.update(tag=tag, version=version, trusted_blobs=pipeline.trusted_blobs(api, SHA))
    files['PROVENANCE.json'] = json_bytes(dict(policy, assets={name: meta for name, meta in pipeline.validate_package(files, SHA, version, kind).items() if name != 'SHA256SUMS'}))
    files['SHA256SUMS'] = ''.join(pipeline.sha256(data) + '  ' + name + '\n' for name, data in sorted(files.items()) if name != 'SHA256SUMS').encode()
    archive = zip_bytes(files)
    artifact = api.responses[('GET', PREFIX + f'/actions/runs/{RUN_ID}/artifacts?per_page=100&page=1')]['artifacts'][0]
    artifact.update(size_in_bytes=len(archive), digest='sha256:' + pipeline.sha256(archive))
    draft = api.responses[('GET', PREFIX + f'/releases/{RELEASE_ID}')]
    draft.update(tag_name=tag, prerelease=False)
    api.responses[('POST', PREFIX + '/releases')].update(tag_name=tag, prerelease=False)
    api.responses[('GET', PREFIX + '/releases/latest')] = {'id': 12345} if kind == 'smtp' else {'id': RELEASE_ID}
    def publication(data):
        draft.update(draft=data['draft'], prerelease=data['prerelease'])
        return draft
    api.responses[('PATCH', PREFIX + f'/releases/{RELEASE_ID}')] = publication
    return api, env, event, files, archive, kind, version


class RuntimeReleaseTests(unittest.TestCase):
    def test_0510_candidate_publication_binds_versions_schema_and_history_boundary(self):
        migrations = tuple(path.name for path in sorted(
            (Path(__file__).resolve().parents[2] / "backend/migrations").glob("*.sql")))
        self.assertEqual(len(migrations), 13)
        api, env, event, files, artifact, _, _ = normal_flow(
            "core", raw=True, version="0.5.10", migration_names=migrations)
        build = pipeline.strict_json(files["BUILD.json"])
        self.assertEqual(build["component_versions"], {"core": "0.5.10", "smtp": "1.1.0"})
        self.assertEqual(build["smtp_core_compatibility"], {"min": "0.4.0", "max_exclusive": "0.6.0"})
        self.assertEqual({p for p in build["source_files"] if p.startswith("migrations/")},
                         {"migrations/" + name for name in migrations})
        policy = pipeline.prepare_publish(api, env, event)
        pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)
        self.assertEqual(pipeline.publish(api, policy, artifact), RELEASE_ID)
        fields = api.mutations[0][2]
        self.assertEqual(fields["name"], "Aster Core 0.5.10")
        for text in ("Core 0.5.10", "当前 main 祖先", "旧 v0.5.6–v0.5.9", "PREPARED.json",
                     "没有新增数据库迁移", "金额、支付幂等和历史快照不变", "aster.resource/1",
                     "Core >=0.5.10,<0.6.0", "SMTP 1.1.0", "Ubuntu 22.04", "Ubuntu 24.04",
                     "Ubuntu 26.04", "发行要求同一源码的两个平台完整原生检查成功", "原会话", "RELEASE-v0.5.10.md"):
            self.assertIn(text, fields["body"])
        self.assertNotIn("完整原生验收未完成", fields["body"])
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual({name: data for _, name, data in api.uploads}, files)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])

    def test_core_and_smtp_are_independent_binary_only_packages(self):
        for kind in ('core', 'smtp'):
            _, version, files, _, _ = runtime_fixture(kind)
            with self.subTest(kind=kind):
                self.assertEqual(len(files), 3)
                self.assertFalse(any('source.tar.gz' in name for name in files))
                self.assertEqual(set(pipeline.validate_package(files, SHA, version, kind)), set(files))

    def test_059_raw_metadata_binds_active_core_smtp_and_all_thirteen_migrations(self):
        # Explicit active-release fixture; historical archive/raw defaults remain
        # 0.4.0/0.5.2 and retain their original compatibility/inventory semantics.
        migrations = tuple(path.name for path in sorted(
            (Path(__file__).resolve().parents[2] / "backend/migrations").glob("*.sql")))
        self.assertEqual(len(migrations), 13)
        self.assertEqual(migrations[-1], "013_product_configurations.sql")
        api, env, event, files, _, kind, version = normal_flow(
            "core", raw=True, version="0.5.9", migration_names=migrations)
        self.assertEqual((kind, version), ("core", "0.5.9"))
        self.assertEqual(set(files), {
            "BUILD.json", "PROVENANCE.json", "SHA256SUMS",
            "aster-api-0.5.9-linux-amd64", "aster-plugin-host-0.5.9-linux-amd64",
            "aster-core-0.5.9-linux-amd64-resources.tar.gz"})
        build = pipeline.strict_json(files["BUILD.json"])
        self.assertEqual(build["component_versions"], {"core": "0.5.9", "smtp": "1.1.0"})
        self.assertEqual(build["release_label"], "0.5.9")
        self.assertEqual(build["distribution_format"], "raw-binaries-v1")
        self.assertEqual(build["smtp_core_compatibility"], {"min": "0.4.0", "max_exclusive": "0.6.0"})
        self.assertEqual({path for path in build["source_files"] if path.startswith("migrations/")},
                         {"migrations/" + name for name in migrations})
        policy = pipeline.prepare_publish(api, env, event)
        provenance = pipeline.strict_json(files["PROVENANCE.json"])
        self.assertEqual((provenance["tag"], provenance["version"]), ("v0.5.9", "0.5.9"))
        pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)
        self.assertFalse(api.uploads)
        _, _, wrong, _, _ = runtime_fixture(
            "core", raw=True, version="0.5.9", migration_names=migrations,
            build_override={"component_versions": {"core": "0.5.8", "smtp": "1.1.0"}})
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(wrong, SHA, "0.5.9", "core")

    def test_new_main_readme_is_bound_to_selected_git_not_old_baseline(self):
        api, env, event, files, _, _, _ = normal_flow()
        policy = pipeline.prepare_publish(api, env, event)
        pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)

    def test_source_input_hashes_and_declared_versions_must_match_git(self):
        for change in ('blob', 'version', 'readme'):
            api, env, event, files, _, _, _ = normal_flow()
            policy = pipeline.prepare_publish(api, env, event)
            if change == 'blob':
                key = next(key for key in api.responses if key[1].startswith(PREFIX + '/git/blobs/'))
                api.responses[key]['content'] = base64.b64encode(b'changed').decode()
            elif change == 'version':
                build = pipeline.strict_json(files['BUILD.json']); build['component_versions']['core'] = '0.4.1'
                files['BUILD.json'] = json_bytes(build)
            else:
                tree = api.responses[('GET', PREFIX + f'/git/trees/{SHA}?recursive=1')]
                next(item for item in tree['tree'] if item['path'] == 'README.md')['sha'] = 'f' * 40
            with self.subTest(change=change), self.assertRaises(pipeline.GuardError):
                pipeline.verify_provenance(files, policy, api)
            self.assertFalse(api.mutations)

    def test_git_source_version_uses_exact_core_declaration_not_payment_minimum_or_comment(self):
        accepted = (b'package pluginapi\nconst (\n PaymentMinCoreVersion = "9.9.9"\n CoreVersion = "0.4.0"\n)\n',
                    b'package pluginapi\n// CoreVersion = "9.9.9"\nconst CoreVersion = "0.4.0"\n')
        rejected = (b'package pluginapi\nconst PaymentMinCoreVersion = "0.4.0"\n',
                    b'package pluginapi\n// CoreVersion = "0.4.0"\n',
                    b'package pluginapi\nconst CoreVersion = "0.4.0"\nconst CoreVersion = "0.4.0"\n')
        for source in accepted + rejected:
            with self.subTest(source=source):
                api, env, event, files, _, _, _ = normal_flow(protocol_source=source)
                policy = pipeline.prepare_publish(api, env, event)
                if source in accepted:
                    pipeline.verify_provenance(files, policy, api)
                else:
                    with self.assertRaisesRegex(pipeline.GuardError, "component version"):
                        pipeline.verify_provenance(files, policy, api)
                self.assertFalse(api.mutations)

    def test_runtime_manifest_core_hash_and_missing_host_are_rejected(self):
        for override in ({'schema': True}, {'release_kind': 'smtp'}, {'component_versions': {'core': '0.5.0', 'smtp': '1.0.0'}}, {'binary_sha256': {'bin/aster-api': '0' * 64}}, {'input_sha256': {}}):
            _, version, files, _, _ = runtime_fixture(build_override=override)
            with self.subTest(override=override), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(files, SHA, version)
        _, version, files, _, _ = runtime_fixture(binary_override={'bin/aster-plugin-host': b'not an executable'})
        with self.assertRaises(pipeline.GuardError): pipeline.validate_package(files, SHA, version)

    def test_update_cli_is_required_and_bound_to_exact_git_source(self):
        api, env, event, files, _, _, version = normal_flow()
        policy = pipeline.prepare_publish(api, env, event)
        build = pipeline.strict_json(files["BUILD.json"])
        self.assertEqual(build["source_files"]["deploy/scripts/update-manager.py"], "vps-billing/deploy/scripts/update-manager.py")
        self.assertIn("vps-billing/deploy/scripts/update-manager.py", pipeline.TRUSTED_PATHS)
        self.assertIn("vps-billing/deploy/scripts/test-update-manager.py", pipeline.TRUSTED_PATHS)
        tree = api.responses[("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")]
        entry = next(item for item in tree["tree"] if item["path"] == "vps-billing/deploy/scripts/update-manager.py")
        entry["sha"] = "f" * 40
        with self.assertRaises(pipeline.GuardError):
            pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)

    def test_update_cli_trust_inventory_matches_the_publisher(self):
        import importlib.util, sys
        name = "release_update_policy_fixture"
        spec = importlib.util.spec_from_file_location(name, Path(__file__).resolve().parents[1] / "scripts/update-manager.py")
        manager = importlib.util.module_from_spec(spec)
        sys.modules[name] = manager
        try:
            spec.loader.exec_module(manager)
            self.assertEqual(manager.TRUSTED_PATHS, set(pipeline.TRUSTED_PATHS))
            new_gate = {
                ".github/workflows/ci.yml",
                "vps-billing/deploy/ci/mysql-distribution.py",
                "vps-billing/deploy/ci/mysql-distribution-ubuntu22.json",
                "vps-billing/deploy/ci/browser-mysql.sh",
                "vps-billing/deploy/ci/browser-mysql.py",
                "vps-billing/deploy/ci/browser-mysql-fixture.gotxt",
                "vps-billing/frontend/tests/browser-mysql.mjs",
                "vps-billing/frontend/tests/ui-copy-boundaries.mjs",
            }
            self.assertEqual(manager.trusted_paths_for_core("0.5.10"), set(pipeline.TRUSTED_PATHS))
            for version in ("0.5.3", "0.5.4", "0.5.9"):
                self.assertEqual(manager.trusted_paths_for_core(version), manager.PAYMENT_TRUSTED_PATHS)
                self.assertFalse(manager.trusted_paths_for_core(version) & new_gate)
            self.assertEqual(manager.TRUSTED_PATHS - manager.PAYMENT_TRUSTED_PATHS, new_gate)
        finally:
            sys.modules.pop(name, None)

    def test_core_without_shipped_update_cli_is_rejected(self):
        _, version, files, _, _ = runtime_fixture(remove_members=("deploy/scripts/update-manager.py",))
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(files, SHA, version)

    def test_raw_core_prepare_tool_is_required_and_source_bound(self):
        path = "deploy/scripts/core-manager.py"
        _, version, files, _, _ = runtime_fixture(raw=True, remove_members=(path,))
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(files, SHA, version)
        api, env, event, files, _, _, version = normal_flow(raw=True)
        policy = pipeline.prepare_publish(api, env, event)
        build = pipeline.strict_json(files["BUILD.json"])
        self.assertEqual(build["source_files"][path], "vps-billing/" + path)
        self.assertIn("vps-billing/deploy/scripts/test-core-manager.py", pipeline.TRUSTED_PATHS)
        tree = api.responses[("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")]
        next(item for item in tree["tree"] if item["path"] == "vps-billing/" + path)["sha"] = "f" * 40
        with self.assertRaises(pipeline.GuardError):
            pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)

    def test_executor_tool_is_required_and_bound_to_selected_source(self):
        path = "deploy/scripts/core-operation.py"
        _, version, files, _, _ = runtime_fixture(raw=True, remove_members=(path,))
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(files, SHA, version)
        api, env, event, files, _, _, version = normal_flow(raw=True)
        policy = pipeline.prepare_publish(api, env, event)
        build = pipeline.strict_json(files["BUILD.json"])
        self.assertEqual(build["source_files"][path], "vps-billing/" + path)
        self.assertIn("vps-billing/deploy/scripts/test-core-operation.py", pipeline.TRUSTED_PATHS)
        tree = api.responses[("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")]
        next(item for item in tree["tree"] if item["path"] == "vps-billing/" + path)["sha"] = "f" * 40
        with self.assertRaises(pipeline.GuardError):
            pipeline.verify_provenance(files, policy, api)
        self.assertFalse(api.mutations)

    def test_smtp_fixed_inventory_manifest_inner_hash_and_notice_text_are_enforced(self):
        for path, data in (('unknown.go', b'package fixture'), ('plugin', b'not ELF'), ('manifest.json', b'{}'), ('SHA256SUMS', b''), ('THIRD_PARTY_NOTICES.json', b'{"schema":1,"notices":[]}')):
            _, version, files, _, _ = runtime_fixture('smtp', binary_override={path: data})
            with self.subTest(path=path), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(files, SHA, version, 'smtp')

    def test_each_normal_release_uploads_four_verified_assets_then_publishes_once(self):
        for kind in ('core', 'smtp'):
            api, env, event, files, archive, _, _ = normal_flow(kind)
            policy = pipeline.prepare_publish(api, env, event)
            self.assertEqual(pipeline.publish(api, policy, archive), RELEASE_ID)
            self.assertEqual(len(api.uploads), 4)
            self.assertEqual([call[0] for call in api.mutations], ['POST', 'PATCH'])
            self.assertIs(api.mutations[0][2]['draft'], True)
            self.assertIs(api.mutations[0][2]['prerelease'], False)
            self.assertEqual(api.mutations[-1][2], {'draft': False, 'prerelease': False, 'make_latest': 'true' if kind == 'core' else 'false'})
            self.assertEqual({name: data for _, name, data in api.uploads}, files)

    def test_sealing_checksums_cover_provenance_without_a_hash_cycle(self):
        import tempfile
        _, version, files, _, _ = runtime_fixture()
        api, _, _, policy, _, _ = flow_fixture()
        policy.update(tag="v" + version, version=version)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name, data in files.items(): (directory / name).write_bytes(data)
            provenance = pipeline.seal_build(policy, directory)
            self.assertNotIn("SHA256SUMS", provenance["assets"])
            final = {path.name: path.read_bytes() for path in directory.iterdir()}
            covered = pipeline.checksum_inventory(final["SHA256SUMS"], {"BUILD.json", "PROVENANCE.json", "aster-0.4.0-linux-amd64.tar.gz"})
            self.assertTrue(all(pipeline.sha256(final[name]) == digest for name, digest in covered.items()))
            pipeline.validate_package({k: v for k, v in final.items() if k != "PROVENANCE.json"}, SHA, version, "core", final["PROVENANCE.json"])

    def test_raw_core_smtp_asset_count_no_elf_resources_and_publication(self):
        for kind, count in (("core", 6), ("smtp", 5)):
            api, env, event, files, artifact, _, version = normal_flow(kind, raw=True)
            policy = pipeline.prepare_publish(api, env, event)
            pipeline.publish(api, policy, artifact)
            self.assertEqual(len(api.uploads), count)
            resources = pipeline.tar_files(files[pipeline.resources_asset(version, kind)], pipeline.resources_asset(version, kind)[:-7])
            self.assertFalse(any(data.startswith(b"\x7fELF") for data in resources.values()))
            self.assertTrue(all(name in files for name in pipeline.binary_assets(version, kind).values()))

    def test_current_core_publication_describes_payment_boundaries_and_six_assets(self):
        for version in ('0.5.3', '0.5.4', '0.5.5'):
            with self.subTest(version=version):
                api, env, event, files, artifact, _, _ = normal_flow(raw=True, version=version)
                policy = pipeline.prepare_publish(api, env, event)
                pipeline.publish(api, policy, artifact)
                fields = api.mutations[0][2]
                body = fields['body']
                for text in ('Aster Core ' + version, 'aster.payment/1', 'manifest 与程序的 SHA-256', '商户身份',
                             '独立的非特权进程', '无需重新编译 Core', '支付默认关闭', '第三方渠道由开发者',
                             '合成示例', '尚未提供渠道查询、撤销、退款或渠道特定 ACK', '幂等账务状态转换',
                             '渠道交易 ID 唯一性', '取消订单与在途支付互斥', '客户端成功提示和跳转不能改变账务状态',
                             '页头高度、路由切换滚动、币种标签、按钮样式和财务副标题', '不互相替代',
                             '裸 API 程序、裸插件宿主、配套资源包', 'BUILD.json', 'PROVENANCE.json', 'SHA256SUMS',
                             '共六项', '不能用旧程序降低 schema', '没有自动向下迁移或自动导入数据库',
                             f'https://github.com/{pipeline.REPOSITORY}/commit/{SHA}',
                             f'https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}'):
                    self.assertIn(text, body)
                self.assertEqual(fields['name'], 'Aster Core ' + version)
                self.assertEqual(len(api.uploads), 6)
                self.assertEqual({name: data for _, name, data in api.uploads}, files)
                self.assertEqual([call[0] for call in api.mutations], ['POST', 'PATCH'])
                self.assertEqual(api.mutations[-1][2], {'draft': False, 'prerelease': False, 'make_latest': 'true'})

    def test_historical_core_and_smtp_release_bodies_keep_existing_scope(self):
        for kind, raw in (('core', False), ('core', True), ('smtp', False), ('smtp', True)):
            with self.subTest(kind=kind, raw=raw):
                api, env, event, _, artifact, _, version = normal_flow(kind, raw=raw)
                pipeline.publish(api, pipeline.prepare_publish(api, env, event), artifact)
                body = api.mutations[0][2]['body']
                self.assertNotIn('aster.payment/1', body)
                self.assertIn('支付渠道由运营者与开发者按支付适配器接口接入', body)
                self.assertIn('aster.smtp/1', body)
                if kind == 'core' and raw:
                    self.assertEqual(version, '0.5.2')
                    self.assertIn('附件为两个裸程序、资源和三份元数据，共六项', body)
                    self.assertIn('旧程序仅在数据库仍完全兼容时回退', body)
                if kind == 'smtp' and raw:
                    self.assertEqual(version, '1.1.0')
                    self.assertIn('共五项', body)
                    self.assertIn('>=0.4.0,<0.6.0', body)
                    self.assertEqual(api.mutations[-1][2]['make_latest'], 'false')

    def test_raw_sealing_covers_every_asset_without_resource_hash_cycle(self):
        import tempfile
        for kind, count in (("core", 6), ("smtp", 5)):
            tag, version, files, _, _ = runtime_fixture(kind, raw=True)
            _, _, _, policy, _, _ = flow_fixture()
            policy.update(tag=tag, version=version)
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                for name, data in files.items():
                    (directory / name).write_bytes(data)
                provenance = pipeline.seal_build(policy, directory)
                final = {path.name: path.read_bytes() for path in directory.iterdir()}
                self.assertEqual(len(final), count)
                self.assertEqual(set(provenance["assets"]), set(final) - {"PROVENANCE.json", "SHA256SUMS"})
                covered = pipeline.checksum_inventory(final["SHA256SUMS"], set(final) - {"SHA256SUMS"})
                self.assertTrue(all(pipeline.sha256(final[name]) == digest for name, digest in covered.items()))
                self.assertFalse(set(pipeline.strict_json(final["BUILD.json"])) & {"resources_sha256", "resources_size"})
                pipeline.validate_package({k: v for k, v in final.items() if k != "PROVENANCE.json"}, SHA, version, kind, final["PROVENANCE.json"])

    def test_raw_missing_extra_mixed_layout_type_and_binary_tampering_rejected(self):
        for kind in ("core", "smtp"):
            _, version, files, _, _ = runtime_fixture(kind, raw=True)
            for change in ("missing", "extra", "tamper", "legacy", "mapping", "format"):
                modified = dict(files)
                if change == "missing": modified.pop(next(iter(pipeline.binary_assets(version, kind).values())))
                elif change == "extra": modified["unexpected"] = b"unexpected"
                elif change == "tamper": modified[next(iter(pipeline.binary_assets(version, kind).values()))] = b"bad executable"
                elif change == "legacy": modified["aster-" + version + "-linux-amd64.tar.gz"] = b"unexpected archive"
                else:
                    build = pipeline.strict_json(modified["BUILD.json"])
                    build["binary_assets" if change == "mapping" else "distribution_format"] = []
                    modified["BUILD.json"] = json_bytes(build)
                refresh_checksums(modified)
                with self.subTest(kind=kind, change=change), self.assertRaises(pipeline.GuardError):
                    pipeline.validate_package(modified, SHA, version, kind)

    def test_raw_resource_executable_and_self_hash_metadata_rejected(self):
        for kind in ("core", "smtp"):
            _, version, files, _, _ = runtime_fixture(kind, raw=True, binary_override={"extra-elf": elf_bytes()})
            with self.assertRaises(pipeline.GuardError): pipeline.validate_package(files, SHA, version, kind)
            _, version, files, _, _ = runtime_fixture(kind, raw=True, build_override={"resources_sha256": "a" * 64})
            with self.assertRaises(pipeline.GuardError): pipeline.validate_package(files, SHA, version, kind)

    def test_raw_core_omitted_tagged_migration_rejected_before_publication(self):
        api, env, event, original, _, _, version = normal_flow('core', raw=True)
        policy = pipeline.prepare_publish(api, env, event)
        copies = pipeline.strict_json(original['BUILD.json'])['source_files']
        copies.pop('migrations/001_core.sql')
        _, _, files, _, _ = runtime_fixture(raw=True, build_override={'source_files': copies}, remove_members=('migrations/001_core.sql',))
        # Even replacement metadata/checksums matching the incomplete resources
        # cannot omit a migration retained in the immutable selected source.
        assets = pipeline.validate_package(files, SHA, version, 'core')
        provenance = pipeline.strict_json(original['PROVENANCE.json'])
        provenance['assets'] = {name: meta for name, meta in assets.items() if name != 'SHA256SUMS'}
        files['PROVENANCE.json'] = json_bytes(provenance)
        files['SHA256SUMS'] = ''.join(pipeline.sha256(data) + '  ' + name + '\n' for name, data in sorted(files.items()) if name != 'SHA256SUMS').encode()
        artifact = zip_bytes(files)
        policy['artifact'].update(size_in_bytes=len(artifact), digest='sha256:' + pipeline.sha256(artifact))
        with self.assertRaisesRegex(pipeline.GuardError, 'complete tagged migration inventory'):
            pipeline.publish(api, policy, artifact)
        self.assertFalse(api.mutations)
        self.assertFalse(api.uploads)

    def test_partial_upload_never_reaches_public_patch(self):
        api, env, event, _, archive, _, _ = normal_flow()
        policy = pipeline.prepare_publish(api, env, event); api.fail_upload_number = 2
        with self.assertRaises(pipeline.GuardError): pipeline.publish(api, policy, archive)
        self.assertEqual([call[0] for call in api.mutations], ['POST'])
        self.assertIs(api.responses[('GET', PREFIX + f'/releases/{RELEASE_ID}')]['draft'], True)

    def test_uncertain_publication_not_retried_or_claimed_as_success(self):
        api, env, event, _, archive, _, _ = normal_flow()
        policy = pipeline.prepare_publish(api, env, event)
        api.responses[('PATCH', PREFIX + f'/releases/{RELEASE_ID}')] = pipeline.GuardError('synthetic uncertain outcome')
        with self.assertRaisesRegex(pipeline.GuardError, 'uncertain'):
            pipeline.publish(api, policy, archive)
        self.assertEqual([call[0] for call in api.mutations], ['POST', 'PATCH'])

    def test_plugin_cannot_take_over_latest_core(self):
        api, env, event, _, archive, _, _ = normal_flow('smtp')
        policy = pipeline.prepare_publish(api, env, event)
        original = api.responses[('PATCH', PREFIX + f'/releases/{RELEASE_ID}')]
        def wrong_latest(data):
            result = original(data); api.responses[('GET', PREFIX + '/releases/latest')] = {'id': RELEASE_ID}; return result
        api.responses[('PATCH', PREFIX + f'/releases/{RELEASE_ID}')] = wrong_latest
        with self.assertRaisesRegex(pipeline.GuardError, 'latest-release'):
            pipeline.publish(api, policy, archive)

    def test_transport_patch_only_changes_newly_created_release_public_state(self):
        api = pipeline.GitHub('fixture-token')
        api.created_releases.add(RELEASE_ID)
        body = {'draft': False, 'prerelease': False, 'make_latest': 'true'}
        for path, fields in ((PREFIX + '/releases/123', body), (PREFIX + f'/releases/{RELEASE_ID}', dict(body, body='edit history')), (PREFIX + f'/releases/{RELEASE_ID}', dict(body, draft=True)), (PREFIX + f'/releases/{RELEASE_ID}', dict(body, make_latest='legacy'))):
            with self.subTest(path=path, fields=fields), mock.patch.object(api, '_open') as opened, self.assertRaises(pipeline.GuardError):
                api.request('PATCH', path, data=fields)
            opened.assert_not_called()
        with mock.patch.object(api, '_open', return_value=(200, {}, b'{"id":900}')) as opened:
            self.assertEqual(api.request('PATCH', PREFIX + f'/releases/{RELEASE_ID}', data=body), {'id': RELEASE_ID})
            self.assertEqual(opened.call_count, 1)


if __name__ == '__main__': unittest.main()
