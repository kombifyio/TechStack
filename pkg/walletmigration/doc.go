// Package walletmigration plans the consolidation of the Techstack wallet
// (wallet_items rows protected by TECHSTACK_ENCRYPTION_KEY) into the kombify
// Wallet (workspace Wallet plan, slice P3.3).
//
// The planner is pure: it maps rows that a caller has already read to Wallet
// item plans, resolves each row's owner, quarantines rows whose owner or
// tenant is ambiguous, and summarizes the result as a report that carries
// item ids and counts only. It opens no database, holds no key and decrypts
// nothing. Stored values travel as SourceValue, which never formats, marshals
// or logs its content.
//
// The Executor takes the stores, the Wallet and the legacy decryptor as
// dependencies and moves the eligible rows into the owners' kombify Wallets through
// the Wallet product-service API (kombifywallet): each created item is
// delivered back and compared before the row is touched, an item is found
// again by a hashed migration token, and the row records the Wallet item id in
// a compare-and-swap. Its report carries ids, counts and reason codes only.
// The migration design lives in docs/plans/wallet-p3.3-consolidation.md.
package walletmigration
