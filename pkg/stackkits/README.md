# StackKits Compatibility Fixtures (`pkg/stackkits`)

StackKit authoring belongs to the separate `kombify-StackKits` repository.
TechStack consumes StackKits through the loader path and keeps only
compatibility fixtures here for local development, offline fallback, and tests.

Use `TECHSTACK_STACKKITS_DIR` to point TechStack at a local
`kombify-StackKits` checkout during development. Validate real StackKit CUE in
that repository, not by growing this subtree.

Rules for this directory:

- Keep fixtures small and representative.
- Do not add new product-facing StackKit documentation here.
- Do not document publishing plans here; release/mirror work belongs in
  `kombify-StackKits`.
- If terminology changes, update
  [`docs/getting-started/glossary.md`](../../docs/getting-started/glossary.md)
  instead of inventing local terms.
