# Contributing

kombify Techstack Open Core is a Go control plane with a SvelteKit operator UI
and a Windows local client.

## This repository is a generated release mirror

`kombifyio/TechStack` is produced from an explicit allowlist in the upstream
development repository. Every release replaces the tree here with the curated
export of that release; `.techstack-public-export.json` records the exact
upstream commit and the digest of the exported tree. Pull requests cannot be
merged into this repository, because the next export would overwrite them.

Contributions are welcome and land through the upstream tree:

1. Open an issue or a Discussion that describes the change and, if you have
   one, links to your branch or fork.
2. A maintainer ports the patch upstream, reviews and merges it there, and it
   appears here with the next release. You are credited in the release notes.

## Local development

You need Go (version in `go.mod`), Node.js 24, and pnpm 11. No account,
private registry, or hosted kombify service is required.

```sh
go build ./...
cd app
pnpm install --frozen-lockfile
PUBLIC_KOMBIFY_EDITION=selfhost-oss pnpm build
```

The container image builds from the repository root with
`docker build -t techstack:selfhost .`; `deploy/selfhosted/` holds the Compose
setup and its `.env.example`.

## Bundled third-party source

- `internal/gocommon/` is the unmodified source of the shared kombify Go
  library packages this tree imports, under their own Apache-2.0 `LICENSE`.
- `app/third_party/npm/` holds the unmodified release tarballs of the kombify UI
  packages. `app/pnpm-lock.yaml` pins each one by its sha512 integrity.
  `BUNDLED-PACKAGES.md` identifies their distribution licenses because the
  unmodified tarballs do not contain license files.

Change them upstream, not here.

## Reporting

- Bugs: open an issue with the release version and reproduction steps.
- Questions and ideas: GitHub Discussions (see `SUPPORT.md`).
- Security vulnerabilities: report privately, never in a public issue. See
  `SECURITY.md`.

Do not add internal infrastructure details, private service URLs, or secrets
to public docs, tests, workflows, or examples.
