# Embedded StackKit Fixtures

This directory contains StackKit fixtures that can be compiled into the
kombify TechStack binary as an offline fallback.

## Purpose

Embedded StackKits provide offline fallback capabilities when:

- No GitHub access is available
- Local StackKit directory is not configured
- Network connectivity is limited

## Adding Or Updating Fixtures

Author real StackKits in `kombify-StackKits`. Only copy a minimal fixture here
when TechStack needs an offline fallback or test sample.

1. Copy the StackKit directory (e.g., `base-kit/`) into this `embedded/` folder
2. Ensure the following files are present:
   - `stackkit.yaml` - StackKit metadata
   - `stackfile.cue` - CUE schema (optional)
   - `default-spec.yaml` - Default specification (optional)
   - `templates/` - Template files (optional)
   - `variants/` - OS/compute variants (optional)

3. Rebuild kombify TechStack: `mise run build`

## Default StackKits

The following fixture is expected to be embedded:

- `base-kit` - Basic homelab configuration

## Build-Time Embedding

During release builds, the process should:

1. Clone/copy the required fixture from the StackKits repository
2. Place them in this directory
3. Build the binary with embedded assets

Expose any supported automation through `mise.toml` before documenting it here.
