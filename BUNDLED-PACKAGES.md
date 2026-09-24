# Bundled package licenses

This generated Open-Core tree includes the following source and release
tarballs. The npm tarballs in `app/third_party/npm/` are unmodified and do not
contain license files. Their licenses are documented here alongside them.

| Bundled package | Version | License in this distribution |
| --- | --- | --- |
| `internal/gocommon` | `v0.7.0` | Apache-2.0; see `internal/gocommon/LICENSE` |
| `internal/selfhostcontracts` | `v0.2.0` | Apache-2.0; see `internal/selfhostcontracts/LICENSE` |
| `@kombiverselabs/ai-sdk` | `0.10.2419` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/design` | `0.10.302` | Dual Apache-2.0 / GPL-3.0-or-later; see `LICENSE` |
| `@kombiverselabs/embed` | `0.10.2419` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/notifications` | `0.3.68` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/onboarding-core` | `0.10.302` | MIT; see `BUNDLED-MIT-LICENSE.txt` |
| `@kombiverselabs/ui` | `0.10.302` | MIT; see `BUNDLED-MIT-LICENSE.txt` |

The five MIT packages declare `MIT` in their bundled `package.json`. The
design package's bundled `package.json` omits a license field; its upstream
source repository carries the same dual-license text as this tree's `LICENSE`.
The absence of a tarball license file does not change these terms. Check the
lockfile and `.techstack-public-export.json` for the exact tarball integrity
and source commit of this release.
