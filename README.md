# kombify Techstack Open Core

Techstack Open Core is the self-hostable orchestration UI and control plane for
[StackKits](https://github.com/kombifyio/StackKits). It runs with
`KOMBIFY_EDITION=selfhost-oss` (the default) and keeps runtime data, identity,
credentials, and infrastructure authority under the operator's control. It
needs no kombify account and no hosted kombify service.

The hosted kombify SaaS service is built and deployed from a separate private
development repository. Hosted provider credentials, company operations,
internal planning material, and private platform integrations are not part of
this source distribution.

## Windows local client

Download `kombify-Techstack-Setup.exe` from the
[latest release](https://github.com/kombifyio/TechStack/releases/latest) and
compare its SHA-256 with `SHA256SUMS.txt` from the same release. The
installer contains the server, the operator UI, and the database runtime; no
kombify account is required. Until Windows code signing is available, releases
include `UNSIGNED-WINDOWS-RELEASE.txt` and Windows SmartScreen asks for
confirmation.

## Run with Docker Compose

```bash
cd deploy/selfhosted
cp .env.example .env
# Generate one value each for TECHSTACK_V2_SESSION_SECRET and POSTGRES_PASSWORD:
openssl rand -hex 32
docker compose up -d
```

The default binds the HTTP and gRPC ports to localhost. Configure a trusted
reverse proxy and HTTPS before exposing the service beyond the host.

## Build from source

```bash
go build ./...
cd app && corepack enable && pnpm install --frozen-lockfile && pnpm build
cd .. && docker build -t techstack:selfhost .
```

No private registry, token, or account is needed. See `BUNDLED-PACKAGES.md`
for the licenses of the bundled packages and `CONTRIBUTING.md` for how
contributions land.

## Support, security, and license

- Support: `SUPPORT.md`
- Security reports: `SECURITY.md` (private advisories only)
- License: `LICENSE` (dual Apache 2.0 / GPLv3, at your choice); the bundled
  Go source and npm release tarballs are listed with their licenses in
  `BUNDLED-PACKAGES.md`.

Public releases below `1.0.0` are development releases. Functional findings
are reported after publication; `1.0.0` and later require the stable release
gates before publication.
