# Contributing to kombify Techstack Open Core

Thanks for helping. This page says how a contribution reaches a release and
what to expect from us.

## How this repository works

`kombifyio/TechStack` is the public release mirror of kombify Techstack Open Core. Its source of
truth is a private repository, and each release replaces the tree here with a
curated export. Your pull request is still the way in:

1. We review it here, without running your code on our infrastructure first.
2. If we take it, we import the change into the source with your authorship
   kept. Changes to build configuration or to security-sensitive parts we
   re-implement ourselves and credit to you.
3. When the release that carries it is published, we close your pull request
   with the commit and the version, and the release notes credit you by handle.

If we do not take it, we say why on the pull request.

## Before you start

- **Fixes:** open an issue first, or link one.
- **New features:** start with an idea in [Discussions](https://github.com/kombifyio/TechStack/discussions) so
  we can say whether it fits before you spend time.
- **Small changes** (typos, docs): open a pull request directly.

## Sign off your commits (DCO)

Every commit carries a sign-off line that certifies the
[Developer Certificate of Origin](https://developercertificate.org/), version
1.1: you wrote the change or have the right to submit it under the licence of
this repository ([LICENSE](LICENSE)). Add the line with
`git commit -s`:

```text
Signed-off-by: Your Name <you@example.com>
```

Use your real name. If you forgot, `git rebase --signoff <base>` adds the line to
every commit of your branch, and you force-push the result to your own branch.
We ask for a missing sign-off on the pull request before we review further.

## Rules

- Follow the [Code of Conduct](CODE_OF_CONDUCT.md).
- Sign off every commit, as above.
- Say which AI tool helped, if any. A person who can explain the change is
  accountable for it.
- No secrets, credentials, personal data or private hostnames in a diff.
- Draft pull requests are welcome for early feedback.

## What to expect

| Step | Target |
| --- | --- |
| First response | One business day |
| Verdict (take, change, or decline with reason) | Three business days |
| Release | The next release after import; see the [changelog](https://docs.kombify.io/changelog/overview) |

These are targets, not contractual promises.

## Local development

Techstack Open Core is a Go control plane with a SvelteKit operator UI and a
Windows local client. You need Go (version in `go.mod`), Node.js 24, and pnpm
11. No account, private registry, or hosted kombify service is required.

```sh
go build ./...
cd app
pnpm install --frozen-lockfile
PUBLIC_KOMBIFY_EDITION=selfhost-oss pnpm build
```

The container image builds from the repository root with
`docker build -t techstack:selfhost .`; `deploy/selfhosted/` holds the Compose
setup and its `.env.example`.

### Bundled third-party source

- `internal/gocommon/` is the unmodified source of the shared kombify Go
  library packages this tree imports, under their own Apache-2.0 `LICENSE`.
- `app/third_party/npm/` holds the unmodified release tarballs of the kombify UI
  packages. `app/pnpm-lock.yaml` pins each one by its sha512 integrity.
  `BUNDLED-PACKAGES.md` identifies their distribution licenses because the
  unmodified tarballs do not contain license files.

Change them upstream, not here.

## Reporting

- Bugs: the [bug form](https://github.com/kombifyio/TechStack/issues/new?template=bug_report.yml) in this repository.
- Ideas: [Discussions](https://github.com/kombifyio/TechStack/discussions).
- Questions: see [SUPPORT.md](SUPPORT.md).
- Vulnerabilities: privately, see [SECURITY.md](SECURITY.md).
