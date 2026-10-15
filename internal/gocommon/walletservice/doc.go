// Package walletservice is the shared Go client of the kombify Wallet's
// product-service API (`POST /v1/wallet/service/*` on the Gateway;
// kombify-Gateway `services/wallet-store/AGENT-ACCESS.md`, "Product services"
// and "SSH certificates"). A kombify product backend (Techstack, Paperwork, AI
// Platform, Simulate, Cloud) uses it to work with its users' Wallet items
// without a person present. The product acts there as its own service for the
// owner named in each call, never as the owner and never as an agent.
//
// Operations: List, CaptureKey, Create (a captured value or, for an SSH CA, a
// key generated inside custody), AddVersion, Deliver, Broker, Mint and Sign.
//
// Values travel only as `kombify.wallet-transport.v1` JWEs. A value the
// service stores is sealed to the Wallet Store's capture key. A value the
// Wallet delivers or mints is sealed to the service's own P-256 recipient key
// and is opened only when the whole transport context the Wallet returned
// equals the one the call expects (owner, item version, operation, operation
// id, the service's audience, recipient key thumbprint and generation, and the
// exact expiry the request asked for), after the envelope's own authenticated
// header has been checked. What the Wallet releases arrives as a Secret that
// prints as a fixed marker and can be wiped.
//
// The client is configured entirely from outside (Config) and never reads the
// environment. With anything missing or invalid it is disabled: every call
// returns an ErrUnavailable error without touching the network, and no caller
// may fall back to another source for an item that lives in the Wallet. It
// signs only with the product's own Wallet service secret; the platform's
// shared SERVICE_AUTH_SECRET never admits these routes and must not be passed.
//
// This is a SaaS-only package: it speaks to the Gateway and has no self-hosted
// behaviour. Product authorization, entitlements, which item belongs to which
// feature, and persistence of item ids stay with the consuming product.
package walletservice
