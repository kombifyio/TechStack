// Package grpcserver implements the gRPC server for agent communication.
// queue.go provides backpressure-aware command queue implementation (S7).
package grpcserver

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

// OverflowStrategy defines how the queue handles overflow situations.
type OverflowStrategy string

const (
	// OverflowReject rejects new commands when queue is full (recommended for user commands).
	OverflowReject OverflowStrategy = "reject"
	// OverflowDropOldest drops the oldest command to make room (for heartbeats/status updates).
	OverflowDropOldest OverflowStrategy = "drop-oldest"
)

// QueueConfig holds configuration for the backpressure-aware queue.
type QueueConfig struct {
	// MaxSize is the maximum number of commands in the queue.
	MaxSize int
	// OverflowStrategy determines behavior when queue is full.
	OverflowStrategy OverflowStrategy
	// WarningThreshold is the percentage (0-100) at which warnings are logged.
	// Default: 80 (log warnings when queue reaches 80% capacity).
	WarningThreshold int
}

// BackpressureQueue is a thread-safe queue with backpressure support.
// It provides configurable overflow handling and metrics collection.
type BackpressureQueue[T any] struct {
	mu               sync.RWMutex
	items            []T
	maxSize          int
	overflowStrategy OverflowStrategy
	warningThreshold int
	log              *logger.Logger
	name             string // Queue name for logging/metrics

	// Warning state to avoid log spam
	warningLogged atomic.Bool
	lastWarning   atomic.Int64 // Unix timestamp of last warning
}

// NewBackpressureQueue creates a new backpressure-aware queue.
func NewBackpressureQueue[T any](name string, cfg QueueConfig, log *logger.Logger) *BackpressureQueue[T] {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 1000
	}
	if cfg.WarningThreshold <= 0 || cfg.WarningThreshold > 100 {
		cfg.WarningThreshold = 80
	}
	if cfg.OverflowStrategy == "" {
		cfg.OverflowStrategy = OverflowReject
	}

	return &BackpressureQueue[T]{
		items:            make([]T, 0, cfg.MaxSize),
		maxSize:          cfg.MaxSize,
		overflowStrategy: cfg.OverflowStrategy,
		warningThreshold: cfg.WarningThreshold,
		log:              log.WithComponent("queue-" + name),
		name:             name,
	}
}

// Enqueue adds an item to the queue with backpressure handling.
// Returns an error if the queue is full and strategy is "reject".
func (q *BackpressureQueue[T]) Enqueue(item T) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	currentSize := len(q.items)

	// Check warning threshold
	q.checkWarningThreshold(currentSize)

	// Check if queue is full
	if currentSize >= q.maxSize {
		switch q.overflowStrategy {
		case OverflowDropOldest:
			// Drop the oldest item and add the new one
			if len(q.items) > 0 {
				q.items = q.items[1:]
				q.log.Warn("queue_overflow_dropped_oldest",
					"queue", q.name,
					"current_size", len(q.items),
					"max_size", q.maxSize,
				)
			}
		case OverflowReject:
			fallthrough
		default:
			return &QueueFullError{
				QueueName:   q.name,
				CurrentSize: currentSize,
				MaxSize:     q.maxSize,
			}
		}
	}

	q.items = append(q.items, item)

	return nil
}

// TryDequeue attempts to dequeue an item without blocking.
// Returns the item and true if successful, or zero value and false if queue is empty.
func (q *BackpressureQueue[T]) TryDequeue() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var zero T
	if len(q.items) == 0 {
		return zero, false
	}

	item := q.items[0]
	q.items = q.items[1:]

	// Reset warning flag if we're below threshold
	q.checkResetWarning()

	return item, true
}

// DequeueWithFilter dequeues the first item matching the filter function.
// Items that don't match are skipped (remain in queue).
// Returns the item and true if found, or zero value and false if no match.
func (q *BackpressureQueue[T]) DequeueWithFilter(filter func(T) bool) (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var zero T
	for i, item := range q.items {
		if filter(item) {
			// Remove item at index i
			q.items = append(q.items[:i], q.items[i+1:]...)
			q.checkResetWarning()
			return item, true
		}
	}

	return zero, false
}

// Size returns the current number of items in the queue.
func (q *BackpressureQueue[T]) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items)
}

// checkWarningThreshold logs a warning if queue is above threshold.
// Must be called with lock held.
func (q *BackpressureQueue[T]) checkWarningThreshold(currentSize int) {
	threshold := (q.maxSize * q.warningThreshold) / 100
	if currentSize >= threshold {
		// Only log warning once per minute to avoid spam
		now := time.Now().Unix()
		lastWarn := q.lastWarning.Load()
		if !q.warningLogged.Load() || (now-lastWarn) > 60 {
			q.warningLogged.Store(true)
			q.lastWarning.Store(now)
			q.log.Warn("queue_high_watermark",
				"queue", q.name,
				"current_size", currentSize,
				"max_size", q.maxSize,
				"threshold_pct", q.warningThreshold,
				"strategy", string(q.overflowStrategy),
			)
		}
	}
}

// checkResetWarning resets the warning flag if below threshold.
// Must be called with lock held.
func (q *BackpressureQueue[T]) checkResetWarning() {
	threshold := (q.maxSize * q.warningThreshold) / 100
	if len(q.items) < threshold/2 { // Reset when well below threshold
		q.warningLogged.Store(false)
	}
}

// QueueFullError is returned when attempting to enqueue to a full queue.
type QueueFullError struct {
	QueueName   string
	CurrentSize int
	MaxSize     int
}

func (e *QueueFullError) Error() string {
	return fmt.Sprintf("%s queue full: %d/%d items", e.QueueName, e.CurrentSize, e.MaxSize)
}

// IsQueueFullError checks if an error is a QueueFullError.
func IsQueueFullError(err error) bool {
	_, ok := err.(*QueueFullError)
	return ok
}

// =============================================================================
// Command Queue Types (specialized for gRPC commands)
// =============================================================================

// CommandQueue is a backpressure-aware queue for AgentCommand.
type CommandQueue = BackpressureQueue[*AgentCommand]

// StackKitCommandQueue is the dedicated typed StackKits lifecycle queue.
type StackKitCommandQueue = BackpressureQueue[*stackKitCommandEntry]

// NewCommandQueue creates a new command queue with the given configuration.
func NewCommandQueue(cfg QueueConfig, log *logger.Logger) *CommandQueue {
	return NewBackpressureQueue[*AgentCommand]("command", cfg, log)
}

// NewStackKitCommandQueue creates a queue that cannot carry generic shell
// commands.
func NewStackKitCommandQueue(cfg QueueConfig, log *logger.Logger) *StackKitCommandQueue {
	return NewBackpressureQueue[*stackKitCommandEntry]("stackkit-command", cfg, log)
}
