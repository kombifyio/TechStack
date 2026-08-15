# Techstack Open Core

Techstack Open Core is the self-hostable orchestration UI and control plane for
StackKits. It runs with `KOMBIFY_EDITION=selfhost-oss` and keeps runtime data,
identity, credentials, and infrastructure authority under the operator's
control.

The hosted Kombify SaaS service is built and deployed from a separate private
development repository. Hosted provider credentials, company operations,
internal planning material, and private platform integrations are not part of
this source distribution.

## Run with Docker Compose

```bash
cd deploy/selfhosted
cp .env.example .env
openssl rand -hex 32
# Put the generated value into TECHSTACK_V2_SESSION_SECRET in .env.
docker compose up -d
```

The default binds the HTTP and gRPC ports to localhost. Configure a trusted
reverse proxy and HTTPS before exposing the service beyond the host.

## Build from source

```bash
go test ./...
cd app && corepack enable && pnpm install --frozen-lockfile && pnpm build
docker build -t techstack:selfhost .
```

Public releases below `1.0.0` are development releases. Functional findings
are reported after publication; `1.0.0` and later require the stable release
gates before publication.
