package authlocal

import (
	"context"
	"time"
)

// Record is the break-glass admin singleton record.
type Record struct {
	// Email is the login identifier. Bootstrap creates it as
	// [BreakGlassEmail] (or [Config.BootstrapEmail]) but Claim() lets the
	// operator rename it.
	Email string
	// PasswordHash is bcrypt(password). Empty means the record has not been
	// initialized yet.
	PasswordHash string
	// ShowPassword carries the plaintext password while the envelope is
	// alive. Empty after Reveal() consumed it or PasswordEnvelopeTTL elapsed.
	ShowPassword string
	// ShowUntil is the envelope expiry. Zero means no pending envelope.
	ShowUntil time.Time
	// Claimed reports whether the operator has rotated the bootstrap secret
	// at least once via Claim(). After claim, Reveal() returns 410.
	Claimed bool
	// Locked is the persisted operator lock. It prevents every bootstrap
	// mutation independently of Config.ClaimLocked.
	Locked bool
	// CreatedAt and UpdatedAt are auto-stamped by the store.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store persists the singleton break-glass admin record.
//
// Get may return a copy. Save is retained for store administration and legacy
// readers; Service never uses it. Mutating Service operations require AtomicStore.
type Store interface {
	// Get returns the current record or nil if it has never been written.
	Get(ctx context.Context) (*Record, error)
	// Save creates or replaces the singleton record.
	Save(ctx context.Context, r *Record) error
}

// AtomicStore makes bootstrap transitions safe across independent services and
// processes. CompareAndSwap replaces the singleton only if its current record
// matches expected. A nil expected permits creation only when no record exists.
// Implementations compare all credential, envelope, claim and lock fields, and
// preserve store-owned identity and custody fields outside Record. A conflict
// returns false without changing anything; it must never overwrite a claimed or
// locked record. Get and CompareAndSwap must not retain mutable caller pointers.
type AtomicStore interface {
	Store
	CompareAndSwap(ctx context.Context, expected, next *Record) (bool, error)
}
