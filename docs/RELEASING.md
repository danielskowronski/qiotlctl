# Release preparation

Release builds and validation run locally. The examples below use **0.2.0**.
Publishing requires an explicit target or upload command; the repository has no
automatic release workflow.

## Local artifacts

From the module root, with Go 1.27.2, Python 3, Make, Docker/Buildx and Helm:

```sh
make check
make race
make release-local VERSION=0.2.0
make image VERSION=0.2.0
make image-archive VERSION=0.2.0
make chart-package VERSION=0.2.0
```

- `dist/0.2.0/` contains macOS/Linux AMD64/ARM64 tarballs with both binaries,
  licenses, dependency notices, build information and checksums. It also contains
  a deterministic source snapshot from an explicit allowlist. Native tarballs
  retain build timestamps; checksum equality of separate builds is not promised.
- `dist/qiotlctl-0.2.0.oci.tar` contains both Linux image platforms and Buildx
  SBOM/provenance. Its adjacent `.sha256` file verifies the archive.
- `dist/qiotlctl-0.2.0.tgz` and `.sha256` contain the OCI-compatible Helm chart.
- `dist/image-0.2.0-*.json` records image build metadata. Runtime image labels
  identify version, repository, project license and source revision.

`-dirty` on the revision means the working tree includes uncommitted source.
These are review candidates. Checksums identify the files built; they are not
signatures, and a local candidate is not a published release. Build info records
both the base Git revision and source archive SHA256. Rebuilding replaces artifacts
of the same version, so preserve reviewed outputs if comparing builds.

Private config, live broker access, history, `.local`, databases and generated
output are outside the source/build allowlists. Review staged files before
source publication; `.gitignore` cannot remove secrets already tracked.

## Maintainer checklist before publishing

1. Review source, docs and `git diff --cached` after staging the intended files.
   Keep private configuration/history untracked. Commit the reviewed source and
   create the local `v0.2.0` tag at that commit when ready.
2. Confirm application/chart versions in `charts/qiotlctl/Chart.yaml`, changelog,
   license notices and pinned image/compiler digests. Regenerate notices after
   dependency updates using [the notice instructions](../third_party/README.md).
3. Rerun tests, race detector, vet, module verification and `govulncheck`; repeat
   diagnostics/kind smoke tests after relevant changes. The pinned scanner is `golang.org/x/vuln/cmd/govulncheck@v1.9.0`.
4. Rebuild local release artifacts from the clean tagged checkout, verify SHA256
   files, inspect versions/labels and inspect chart rendering with deployment
   values. Keep credentials out of rendered files submitted for review.
5. Run `make release-check VERSION=0.2.0`. This read-only gate checks clean Git
   state, tag, versions and the chart's project license. It does not certify that
   tests passed and does not create the commit/tag.
6. Only after deciding to publish, authenticate and run the explicit commands in
   [Deployment](DEPLOYMENT.md#publishing). Those targets rebuild/package from
   the checked source; do not edit source between verification and publication.
   Record registry digests and upload the reviewed native bundles/checksums through
   the chosen GitHub release process. Source pushing and GitHub release creation
   remain explicit maintainer actions.

The push guard is also used by `image-push` and `chart-push`. Direct Docker/Helm
commands bypass it. Signing, automated CI and a GitHub release uploader are not
configured; they can be added later without changing the runtime.

## Deployment checks

Verify the configured MQTT permissions and enabled output endpoints before
deploying. For an existing installation, follow the
[upgrade guidance](DEPLOYMENT.md#upgrading-an-existing-installation). HA discovery
readback requires subscribe permission as well as publish permission. Enabled HA/VM outputs affect readiness unless configured as advisory;
liveness remains independent of those services. Back up persistent storage before
upgrades and retain it for rollback. See [diagnostics](DIAGNOSTICS.md),
[ACLs](DEPLOYMENT.md#mqtt-permissions-and-emqx-acls) and
[validation limitations](VALIDATION.md).
