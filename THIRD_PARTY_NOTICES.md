# Third-Party Notices

MCPlexer depends on third-party open source packages managed through Go modules
and npm.

The dependency manifests are the source of truth for the full dependency set:

- `go.mod` and `go.sum`
- `site/package.json` and `site/package-lock.json`
- `web/package.json` and `web/package-lock.json`
- `integrations/pi/package.json`

Known dependency license families include MIT, Apache-2.0, BSD, ISC, MPL-2.0,
and LGPL-licensed transitive components used by the site build toolchain.
Third-party packages remain under their own licenses and are not relicensed by
MCPlexer's AGPL-3.0-or-later license.

Release artifacts that bundle third-party code should include generated notices
for the exact dependency versions included in that artifact.
