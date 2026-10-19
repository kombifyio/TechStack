# Bundled package licenses

This generated Open-Core tree includes the following source modules and
unmodified npm release tarballs (`app/third_party/npm/`). The export generates
this table from the projected modules and the vendored tarballs it actually
ships, so versions and rows match this release.

| Bundled package | Version | License in this distribution |
| --- | --- | --- |
| `internal/gocommon` | `v0.8.2` | Apache-2.0; see `internal/gocommon/LICENSE` |
| `internal/selfhostcontracts` | `v0.2.8` | Apache-2.0; see `internal/selfhostcontracts/LICENSE` |
| `@kombiverselabs/ai-sdk` | `0.13.7` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/cloud-notifications` | `0.3.120` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/design` | `0.11.1` | No license field; see the note below |
| `@kombiverselabs/embed` | `0.13.7` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/execution-contracts` | `0.13.7` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/onboarding-core` | `0.11.1` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/speechkit-voiceagent-client` | `0.77.7` | Apache-2.0; see `package/LICENSE` inside the tarball |
| `@kombiverselabs/ui` | `0.11.1` | MIT; see `BUNDLED-MIT-LICENSE.txt` |

The MIT packages declare `MIT` in their bundled `package.json` and ship no
license file; their terms are in `BUNDLED-MIT-LICENSE.txt`. A package whose
tarball carries its own license file keeps that file. The
`@kombiverselabs/design` package's bundled `package.json` omits a license
field; its upstream source repository carries the same dual-license text as
this tree's `LICENSE`, which applies to it. The absence of a tarball license
file does not change these terms. Check the lockfile and
`.techstack-public-export.json` for the exact tarball integrity and source
commit of this release.
