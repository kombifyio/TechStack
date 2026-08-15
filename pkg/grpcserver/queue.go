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

// DefaultQueueConfig returns the default queue configuration.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		MaxSize:          1000,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}
}

// QueueStats holds statistics about the queue.
type QueueStats struct {
	CurrentSize      int64
	MaxSize          int64
	TotalEnqueued    int64
	TotalDequeued    int64
	TotalRejected    int64
	TotalDropped     int64
	WarningThreshold int64
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

	// Metrics (atomic for lock-free reads)
	totalEnqueued atomic.Int64
	totalDequeued atomic.Int64
	totalRejected atomic.Int64
	totalDropped  atomic.Int64

	// Warning state to avoid log spam
	warningLogged atomic.Bool
	lastWarning   atomic.Int64 // Unix timestamp of last warning

	// Channel for signaling new items (for consumers waiting)
	notify chan struct{}
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
		notify:           make(chan struct{}, 1),
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
				q.totalDropped.Add(1)
				q.log.Warn("queue_overflow_dropped_oldest",
					"queue", q.name,
					"current_size", len(q.items),
					"max_size", q.maxSize,
				)
			}
		case OverflowReject:
			fallthrough
		default:
			q.totalRejected.Add(1)
			return &QueueFullError{
				QueueName:   q.name,
				CurrentSize: currentSize,
				MaxSize:     q.maxSize,
			}
		}
	}

	q.items = append(q.items, item)
	q.totalEnqueued.Add(1)

	// Signal waiting consumers
	select {
	case q.notify <- struct{}{}:
	default:
	}

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
	q.totalDequeued.Add(1)

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
			q.totalDequeued.Add(1)
			q.checkResetWarning()
			return item, true
		}
	}

	return zero, false
}

// Peek returns the first item without removing it.
// Returns the item and true if queue is not empty.
func (q *BackpressureQueue[T]) Peek() (T, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var zero T
	if len(q.items) == 0 {
		return zero, false
	}
	return q.items[0], true
}

// Size returns the current number of items in the queue.
func (q *BackpressureQueue[T]) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items)
}

// IsFull returns true if the queue has reached its maximum size.
func (q *BackpressureQueue[T]) IsFull() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items) >= q.maxSize
}

// IsAboveThreshold returns true if queue size exceeds the warning threshold.
func (q *BackpressureQueue[T]) IsAboveThreshold() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	threshold := (q.maxSize * q.warningThreshold) / 100
	return len(q.items) >= threshold
}

// Stats returns current queue statistics.
func (q *BackpressureQueue[T]) Stats() QueueStats {
	q.mu.RLock()
	currentSize := len(q.items)
	q.mu.RUnlock()

	return QueueStats{
		CurrentSize:      int64(currentSize),
		MaxSize:          int64(q.maxSize),
		TotalEnqueued:    q.totalEnqueued.Load(),
		TotalDequeued:    q.totalDequeued.Load(),
		TotalRejected:    q.totalRejected.Load(),
		TotalDropped:     q.totalDropped.Load(),
		WarningThreshold: int64(q.warningThreshold),
	}
}

// NotifyChan returns a channel that receives a signal when new items are added.
// Useful for consumers that want to wait for new items.
func (q *BackpressureQueue[T]) NotifyChan() <-chan struct{} {
	return q.notify
}

// Clear removes all items from the queue.
func (q *BackpressureQueue[T]) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = q.items[:0]
	q.warningLogged.Store(false)
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

// TofuCommandQueue is a backpressure-aware queue for TofuCommand entries.
type TofuCommandQueue = BackpressureQueue[*tofuCommandEntry]

// StackKitCommandQueue is the dedicated typed StackKits lifecycle queue.
type StackKitCommandQueue = BackpressureQueue[*stackKitCommandEntry]

// NewCommandQueue creates a new command queue with the given configuration.
func NewCommandQueue(cfg QueueConfig, log *logger.Logger) *CommandQueue {
	return NewBackpressureQueue[*AgentCommand]("command", cfg, log)
}

// NewTofuCommandQueue creates a new tofu command queue with the given configuration.
func NewTofuCommandQueue(cfg QueueConfig, log *logger.Logger) *TofuCommandQueue {
	return NewBackpressureQueue[*tofuCommandEntry]("tofu-command", cfg, log)
}

// NewStackKitCommandQueue creates a queue that cannot carry generic shell
// commands.
func NewStackKitCommandQueue(cfg QueueConfig, log *logger.Logger) *StackKitCommandQueue {
	return NewBackpressureQueue[*stackKitCommandEntry]("stackkit-command", cfg, log)
}

// =============================================================================
// Queue Metrics Integration
// =============================================================================

// QueueMetrics holds Prometheus-compatible metrics for queue monitoring.
// These are designed to integrate with the existing metrics in internal/routes/metrics.go
type QueueMetrics struct {
	// Current queue size
	Size func() float64
	// Maximum queue size
	MaxSize float64
	// Total items enqueued
	TotalEnqueued func() float64
	// Total items dequeued
	TotalDequeued func() float64
	// Total items rejected (queue full)
	TotalRejected func() float64
	// Total items dropped (overflow strategy: drop-oldest)
	TotalDropped func() float64
}

// GetMetrics returns metrics collectors for the queue.
func (q *BackpressureQueue[T]) GetMetrics() QueueMetrics {
	return QueueMetrics{
		Size:          func() float64 { return float64(q.Size()) },
		MaxSize:       float64(q.maxSize),
		TotalEnqueued: func() float64 { return float64(q.totalEnqueued.Load()) },
		TotalDequeued: func() float64 { return float64(q.totalDequeued.Load()) },
		TotalRejected: func() float64 { return float64(q.totalRejected.Load()) },
		TotalDropped:  func() float64 { return float64(q.totalDropped.Load()) },
	}
}

// =============================================================================
// Per-Agent Queue Support (for future per-agent command routing)
// =============================================================================

// AgentQueueManager manages per-agent command queues.
// This enables more granular backpressure control per agent.
type AgentQueueManager struct {
	mu        sync.RWMutex
	queues    map[string]*CommandQueue
	cfg       QueueConfig
	log       *logger.Logger
	maxAgents int
}

// NewAgentQueueManager creates a new per-agent queue manager.
func NewAgentQueueManager(cfg QueueConfig, log *logger.Logger, maxAgents int) *AgentQueueManager {
	if maxAgents <= 0 {
		maxAgents = 100 // Default max agents
	}
	return &AgentQueueManager{
		queues:    make(map[string]*CommandQueue),
		cfg:       cfg,
		log:       log.WithComponent("agent-queue-mgr"),
		maxAgents: maxAgents,
	}
}

// GetOrCreateQueue returns the queue for an agent, creating it if necessary.
func (m *AgentQueueManager) GetOrCreateQueue(agentID string) (*CommandQueue, error) {
	m.mu.RLock()
	q, exists := m.queues[agentID]
	m.mu.RUnlock()

	if exists {
		return q, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if q, exists = m.queues[agentID]; exists {
		return q, nil
	}

	// Check max agents limit
	if len(m.queues) >= m.maxAgents {
		return nil, fmt.Errorf("maximum number of agent queues reached: %d", m.maxAgents)
	}

	// Create new queue for agent
	q = NewBackpressureQueue[*AgentCommand](
		"agent-"+agentID,
		m.cfg,
		m.log,
	)
	m.queues[agentID] = q

	m.log.Info("agent_queue_created",
		"agent_id", agentID,
		"max_size", m.cfg.MaxSize,
	)

	return q, nil
}

// RemoveQueue removes the queue for an agent.
func (m *AgentQueueManager) RemoveQueue(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if q, exists := m.queues[agentID]; exists {
		stats := q.Stats()
		m.log.Info("agent_queue_removed",
			"agent_id", agentID,
			"final_size", stats.CurrentSize,
			"total_enqueued", stats.TotalEnqueued,
			"total_rejected", stats.TotalRejected,
		)
		delete(m.queues, agentID)
	}
}

// AllStats returns statistics for all agent queues.
func (m *AgentQueueManager) AllStats() map[string]QueueStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]QueueStats, len(m.queues))
	for agentID, q := range m.queues {
		stats[agentID] = q.Stats()
	}
	return stats
}
