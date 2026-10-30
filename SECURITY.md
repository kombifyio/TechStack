# Security Policy

## Reporting a vulnerability

Report suspected vulnerabilities in kombify Techstack Open Core privately through
[GitHub private vulnerability reporting](https://github.com/kombifyio/TechStack/security/advisories/new) or by e-mail to [info@kombify.io](mailto:info@kombify.io) (contact file:
https://kombify.io/.well-known/security.txt, policy: https://kombify.io/security).
Never open a public issue, pull request or discussion for a security problem.

Include the affected component and version, steps to reproduce (a minimal
proof of concept if possible), the impact you see and a suggested mitigation
if you have one. Never include live credentials or personal data.

## Response targets

These are targets, not contractual promises.

- Acknowledgement: within 48 hours
- High and critical severity: a fix or mitigation plan within 14 days
- Lower severity: scheduled into the normal release process

## Supported versions

Fixes go to the latest release of each release channel (Stable and Early
Access).

## Scope

In scope: the self-hosted server and operator UI, the Windows local client and
its installer, the container image, and the Compose deployment in this
repository.

Out of scope: infrastructure you operate yourself (hosts, reverse proxies,
DNS, and the StackKits you deploy with Techstack), and hosted kombify services,
which have their own reporting path on [kombify.io](https://kombify.io).

## Release integrity

Every release lists SHA-256 checksums for its assets. Windows installers are
intentionally unsigned on every release channel and ship with
`UNSIGNED-WINDOWS-RELEASE.txt`; verify the checksum before you install it.

## Disclosure

We coordinate disclosure with you. For a confirmed vulnerability that affects
users we publish an advisory once a fix is released, crediting you unless you
ask us not to.
