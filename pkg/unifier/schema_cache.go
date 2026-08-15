// Package unifier provides the schema cache for CUE validation.
package unifier

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// DefaultValidationTimeout ist das Timeout für CUE-Operationen.
// Robustheit: CUE-Validation soll nie die gesamte Pipeline blockieren.
const DefaultValidationTimeout = 5 * time.Second

// SchemaCache verwaltet kompilierte CUE-Schemas mit Thread-Safety und Caching.
// Das reduziert CPU-Last bei wiederholten Validierungen erheblich.
type SchemaCache struct {
	ctx     *cue.Context
	schemas map[string]cue.Value
	mu      sync.RWMutex

	// Metriken für Monitoring
	hits   int64
	misses int64
}

// globalSchemaCache ist der Singleton-Cache für die gesamte Anwendung.
// Thread-safe via sync.RWMutex.
var globalSchemaCache = &SchemaCache{
	schemas: make(map[string]cue.Value),
}

// GetSchemaCache gibt den globalen Schema-Cache zurück.
func GetSchemaCache() *SchemaCache {
	return globalSchemaCache
}

// GetOrCompile holt ein Schema aus dem Cache oder kompiliert es.
// Thread-safe: verwendet Double-Check-Locking für Performance.
func (c *SchemaCache) GetOrCompile(name string, source []byte) (cue.Value, error) {
	// Fast-path: Read-Lock für Cache-Hit
	c.mu.RLock()
	if v, ok := c.schemas[name]; ok {
		c.mu.RUnlock()
		atomic.AddInt64(&c.hits, 1)
		return v, nil
	}
	c.mu.RUnlock()

	// Slow-path: Write-Lock für Cache-Miss
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check nach Lock-Upgrade
	if v, ok := c.schemas[name]; ok {
		atomic.AddInt64(&c.hits, 1)
		return v, nil
	}

	// Lazy-init CUE Context
	if c.ctx == nil {
		c.ctx = cuecontext.New()
	}

	// Schema kompilieren
	v := c.ctx.CompileBytes(source, cue.Filename(name))
	if v.Err() != nil {
		return cue.Value{}, fmt.Errorf("CUE compile error for %s: %w", name, v.Err())
	}

	c.schemas[name] = v
	atomic.AddInt64(&c.misses, 1)
	return v, nil
}

// GetContext gibt den CUE-Context zurück (lazy-init).
func (c *SchemaCache) GetContext() *cue.Context {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ctx == nil {
		c.ctx = cuecontext.New()
	}
	return c.ctx
}

// Stats gibt Cache-Statistiken zurück.
func (c *SchemaCache) Stats() (hits, misses int64) {
	return atomic.LoadInt64(&c.hits), atomic.LoadInt64(&c.misses)
}

// Clear leert den Cache (für Tests oder Hot-Reload).
func (c *SchemaCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schemas = make(map[string]cue.Value)
	c.hits = 0
	c.misses = 0
}

// ValidateWithTimeout führt CUE-Validation mit Timeout aus.
// Robustheit: Gibt bei Timeout einen Fehler zurück, blockiert aber nie ewig.
func ValidateWithTimeout(ctx context.Context, schema, value cue.Value, opts ...cue.Option) error {
	done := make(chan error, 1)

	go func() {
		unified := schema.Unify(value)
		// Default-Options wenn keine angegeben
		if len(opts) == 0 {
			opts = []cue.Option{cue.Concrete(true)}
		}
		done <- unified.Validate(opts...)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("CUE validation timeout after %v: %w", DefaultValidationTimeout, ctx.Err())
	}
}

// ValidateWithDefaultTimeout ist ein Convenience-Wrapper.
func ValidateWithDefaultTimeout(schema, value cue.Value) error {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultValidationTimeout)
	defer cancel()
	return ValidateWithTimeout(ctx, schema, value)
}

// UnifyWithTimeout führt CUE-Unification mit Timeout aus.
// Gibt den unified Value zurück oder einen Fehler bei Timeout.
func UnifyWithTimeout(ctx context.Context, base, overlay cue.Value) (cue.Value, error) {
	type result struct {
		value cue.Value
		err   error
	}
	done := make(chan result, 1)

	go func() {
		unified := base.Unify(overlay)
		if unified.Err() != nil {
			done <- result{err: unified.Err()}
		} else {
			done <- result{value: unified}
		}
	}()

	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		return cue.Value{}, fmt.Errorf("CUE unification timeout: %w", ctx.Err())
	}
}
