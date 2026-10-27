#!/usr/bin/env python3
"""Read-only, fixed Ubuntu platform/manifest policy for the native CI fixture.

No caller-supplied distribution, URL, digest or extracted tree is accepted by
the CLI. Importable validators let offline tests exercise rejected inputs
without impersonating a host or downloading/executing a database.
"""
import inspect
import json
from pathlib import Path
import re
import stat
import sys
import tarfile

PLATFORMS = {
    "22.04": ("jammy", "mysql-distribution-ubuntu22.json"),
    "24.04": ("noble", "mysql-distribution.json"),
}
PACKAGES = frozenset({"mysql-community-server-core", "mysql-community-client-core",
                      "mysql-community-client-plugins", "mysql-community-client"})


def ubuntu_version(document):
    fields = {}
    for line in document.splitlines():
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key in fields:
            raise ValueError("duplicate OS identity field")
        fields[key] = value.strip('"')
    version = fields.get("VERSION_ID")
    if fields.get("ID") != "ubuntu" or version not in PLATFORMS:
        raise ValueError("native MySQL CI supports only Ubuntu 22.04/24.04")
    return version


def require_data_filter():
    # Security filters are backported to supported distribution Python builds.
    # A version check alone either rejects patched Jammy or trusts an unpatched
    # interpreter. Never fall back to fully_trusted/unfiltered extraction.
    if not callable(getattr(tarfile, "data_filter", None)) or \
            "filter" not in inspect.signature(tarfile.TarFile.extractall).parameters:
        raise ValueError("Python tarfile data_filter and extractall(filter=) are required before any download")


def validate_manifest(value, version):
    if version not in PLATFORMS:
        raise ValueError("unsupported Ubuntu version")
    if not isinstance(value, dict) or type(value.get("schema")) is not int or value["schema"] != 2:
        raise ValueError("unsupported distribution manifest schema")
    if value.get("status") != "pinned_official_metadata":
        raise ValueError("native MySQL distribution requires reviewed official package pins")
    mysql_version = value.get("version")
    if not isinstance(mysql_version, str) or re.fullmatch(r"8\.4\.[0-9]+", mysql_version) is None:
        raise ValueError("only a reviewed MySQL 8.4 patch release is accepted")
    package_version = f"{mysql_version}-1ubuntu{version}"
    if value.get("package_version") != package_version or value.get("architecture") != "amd64" or \
            value.get("distribution") != "ubuntu-" + version:
        raise ValueError("only the matching pinned Ubuntu amd64 package version is accepted")
    codename = PLATFORMS[version][0]
    if value.get("metadata_url") != f"https://repo.mysql.com/apt/ubuntu/dists/{codename}/mysql-8.4-lts/binary-amd64/Packages":
        raise ValueError("package metadata must identify the matching official Oracle Ubuntu 8.4 repository")
    packages = value.get("packages")
    if not isinstance(packages, list) or len(packages) != len(PACKAGES):
        raise ValueError("all four pinned MySQL packages are required")
    names = [item.get("name") for item in packages if isinstance(item, dict)]
    if len(names) != len(PACKAGES) or not all(isinstance(name, str) for name in names) or set(names) != PACKAGES:
        raise ValueError("package names must be the exact, nonduplicated MySQL fixture set")
    output = [mysql_version]
    for item in packages:
        digest, size = item.get("sha256"), item.get("size")
        if not isinstance(digest, str) or re.fullmatch(r"[0-9a-f]{64}", digest) is None or digest == "0" * 64:
            raise ValueError("a reviewed, nonempty SHA-256 is required for every package")
        if type(size) is not int or not 0 < size <= 1024 * 1024 * 1024:
            raise ValueError("a positive bounded package size is required")
        output.extend((f"{item['name']}_{package_version}_amd64.deb", digest, str(size)))
    return output


def strict_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate manifest field")
        result[key] = value
    return result


def main():
    if len(sys.argv) != 1:
        raise ValueError("the fixed native distribution policy takes no arguments")
    require_data_filter()
    version = ubuntu_version(Path("/etc/os-release").read_text(encoding="utf-8"))
    manifest = Path(__file__).with_name(PLATFORMS[version][1])
    info = manifest.lstat()
    if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 16384:
        raise ValueError("reviewed manifest must be a bounded regular file, never a symlink")
    value = json.loads(manifest.read_text(encoding="utf-8"), object_pairs_hook=strict_pairs)
    output = validate_manifest(value, version)
    print(manifest)
    print("\n".join(output))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, UnicodeError) as error:
        print("FAIL: " + str(error), file=sys.stderr)
        raise SystemExit(1)
