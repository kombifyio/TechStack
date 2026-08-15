# StackKits advanced capability issuer

This package is Techstack's account-free issuance boundary for
`stackkit.advanced-capability/v1`.

The composition root injects a `crypto.Signer`. The package accepts only an
Ed25519 public key, derives `keyId` as
`ed25519://sha256/<lowercase hex SHA-256 of the raw 32-byte public key>`, and
never reads a key, credential, command, environment variable, provider, or
endpoint itself.

The signature input is:

```text
SHA-256(
  UTF-8("stackkit.advanced-capability/v1") ||
  0x00 ||
  RFC8785_JCS(document without the exact top-level signature field)
)
```

The resulting 64-byte Ed25519 signature uses unpadded standard Base64.
Capabilities bind one local `ownerRef`, one Stack ID, an explicit sorted
operation set, and logical UI Manager/RIL approval references. Their maximum
lifetime is 30 days.

`testdata/advanced-capability-v1.json` and
`testdata/advanced-capability-v1.vector.json` are the fixed cross-repository
conformance vectors StackKits consumes for its offline verifier.
