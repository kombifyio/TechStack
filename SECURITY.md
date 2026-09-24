# Security Policy

## Reporting a vulnerability

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/kombifyio/TechStack/security/advisories/new)
on this repository. Do not open a public issue for a security problem.

You will receive an acknowledgement, a severity assessment, and a fix or
mitigation timeline. Coordinated disclosure is preferred; credit is given in
the release notes unless you ask otherwise.

## Supported versions

Only the latest public release on
[github.com/kombifyio/TechStack/releases](https://github.com/kombifyio/TechStack/releases)
receives security fixes. Earlier Alpha releases are superseded and unsupported.

## Scope

In scope: the self-hosted server and operator UI, the Windows local client and
its installer, the container image, and the Compose deployment in this
repository.

Out of scope: infrastructure you operate yourself (hosts, reverse proxies,
DNS, and the StackKits you deploy with Techstack), and hosted kombify services,
which have their own reporting path on [kombify.io](https://kombify.io).

## Release integrity

Every release lists SHA-256 checksums for its assets. Until Windows code
signing is available, the Windows installer ships with
`UNSIGNED-WINDOWS-RELEASE.txt`; verify the checksum before you install it.
