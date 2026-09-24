# Kombify.Client.Shell

Shared Windows shell seams used by Kombify product clients. The package owns
strict `ClientConnectionProfile v1` validation and Windows Credential Manager
access. Product shells keep their branding, routes, runtime arguments and UI in
their product adapters.

This package never owns product data, authentication policy or domain logic.
