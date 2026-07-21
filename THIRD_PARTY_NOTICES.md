# Third-Party Notices

MCPlexer depends on third-party open source packages managed through Go modules
and npm.

The dependency manifests are the source of truth for the dependency set:

- `go.mod` and `go.sum`
- `site/package.json` and `site/package-lock.json`
- `web/package.json` and `web/package-lock.json`
- `integrations/pi/package.json`

Third-party packages remain under their own licenses and are not relicensed by
MCPlexer's AGPL-3.0-or-later license.

Official binary archives include a generated `THIRD_PARTY_LICENSES` directory.
Its index records the exact linked Go dependency graph and production npm tree
used for the release, and it carries the license, notice, copyright, and
package metadata files distributed by those dependencies. Official releases
also attach a machine-readable SPDX SBOM.
