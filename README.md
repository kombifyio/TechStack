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
kombify account is required. Windows releases are intentionally unsigned and
include `UNSIGNED-WINDOWS-RELEASE.txt`; Windows SmartScreen may ask for
confirmation. A code-signing certificate is not a release prerequisite.

## Run with Docker Compose

```bash
cd deploy/selfhosted
cp .env.example .env
# When building from a source checkout, set IMAGE_TAG to a published vX.Y.Z tag.
# The .env.example attached to a GitHub Release already contains that tag.
# Generate one value each for TECHSTACK_V2_SESSION_SECRET and POSTGRES_PASSWORD:
openssl rand -hex 32
# Generate TECHSTACK_ENCRYPTION_KEY (exactly 32 text bytes):
openssl rand -base64 24
docker compose up -d
```

The default binds the HTTP and gRPC ports to localhost. Configure a trusted
reverse proxy and HTTPS before exposing the service beyond the host.

Keep the three generated secrets in `.env` stable and preserve them alongside
your data backups. `TECHSTACK_ENCRYPTION_KEY` protects Wallet credentials and
the installation's Advanced signing key; replacing it makes retained secrets
unreadable. Runtime identity and monitoring data live in `techstack-data`, and
control-plane state lives in `techstack-postgres-data`.

After the service starts, obtain the canonical bootstrap password in
your host terminal:

```bash
docker compose exec techstack /app/techstack owner bootstrap
```

For a source installation, run `techstack owner bootstrap` under the installation's
operator account with its existing database and session configuration. This command
does not start another server or initialize a database. HTTP bootstrap reveal is
disabled. The command consumes an available envelope locally; if it expired or was
already consumed, it explicitly replaces the unclaimed bootstrap credential.
**That replacement immediately invalidates the previous bootstrap password.** It
refuses claimed or locked owners, and never returns a secret after a concurrent
change. Keep the printed value private; redirected output is refused.

Open `/client/local`, enter that bootstrap password and choose the owner's email
and permanent password. Then use that owner's current password when revealing a Wallet
credential; this requires no kombify Cloud account.

If owner creation succeeds but a compatibility profile or setup marker fails,
retry the same chosen email and password. The canonical owner must authenticate
before setup can finish; another email/password cannot replace it. The Windows
local client continues using its automatic OS-custodied device session.

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
- Security reports: `SECURITY.md` (private vulnerability reporting or e-mail, never a public issue)
- License: `LICENSE` (dual Apache 2.0 / GPLv3, at your choice); the bundled
  Go source and npm release tarballs are listed with their licenses in
  `BUNDLED-PACKAGES.md`.

Public releases below `1.0.0` are development releases. Functional findings
are reported after publication; `1.0.0` and later require the stable release
gates before publication.
