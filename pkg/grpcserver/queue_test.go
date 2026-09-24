package grpcserver

import (
	"sync"
	"testing"

	"github.com/kombifyio/techstack/pkg/logger"
)

// TestBackpressureQueueBasicOperations tests basic enqueue/dequeue operations.
func TestBackpressureQueueBasicOperations(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          10,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[string]("test", cfg, log)

	// Test enqueue
	if err := q.Enqueue("item1"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if q.Size() != 1 {
		t.Errorf("Size mismatch: got %d, want 1", q.Size())
	}

	// Test dequeue
	item, ok := q.TryDequeue()
	if !ok || item != "item1" {
		t.Errorf("TryDequeue failed: got %s, want item1", item)
	}

	if q.Size() != 0 {
		t.Errorf("Size should be 0 after dequeue, got %d", q.Size())
	}

	// Test empty dequeue
	_, ok = q.TryDequeue()
	if ok {
		t.Error("TryDequeue should return false for empty queue")
	}
}

// TestBackpressureQueueRejectStrategy tests the reject overflow strategy.
func TestBackpressureQueueRejectStrategy(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          3,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[int]("test-reject", cfg, log)

	// Fill the queue
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(i); err != nil {
			t.Fatalf("Enqueue %d failed: %v", i, err)
		}
	}

	// Try to add one more - should fail
	err := q.Enqueue(3)
	if err == nil {
		t.Error("Enqueue to full queue should fail with reject strategy")
	}

	if !IsQueueFullError(err) {
		t.Errorf("Error should be QueueFullError, got: %v", err)
	}

}

// TestBackpressureQueueDropOldestStrategy tests the drop-oldest overflow strategy.
func TestBackpressureQueueDropOldestStrategy(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          3,
		OverflowStrategy: OverflowDropOldest,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[int]("test-drop", cfg, log)

	// Fill the queue
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(i); err != nil {
			t.Fatalf("Enqueue %d failed: %v", i, err)
		}
	}

	// Add one more - should drop oldest
	if err := q.Enqueue(3); err != nil {
		t.Fatalf("Enqueue with drop strategy should not fail: %v", err)
	}

	// First item should now be 1 (0 was dropped)
	item, ok := q.TryDequeue()
	if !ok || item != 1 {
		t.Errorf("First item should be 1 (0 was dropped), got %d", item)
	}
}

// TestBackpressureQueueDequeueWithFilter tests filtered dequeue.
func TestBackpressureQueueDequeueWithFilter(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{MaxSize: 10, OverflowStrategy: OverflowReject, WarningThreshold: 80}
	q := NewBackpressureQueue[*AgentCommand]("test-filter", cfg, log)

	// Add commands for different agents
	cmd1 := &AgentCommand{ID: "cmd1", AgentID: "agent-1", Type: "health_check"}
	cmd2 := &AgentCommand{ID: "cmd2", AgentID: "agent-2", Type: "health_check"}
	cmd3 := &AgentCommand{ID: "cmd3", AgentID: "agent-1", Type: "health_check"}

	q.Enqueue(cmd1)
	q.Enqueue(cmd2)
	q.Enqueue(cmd3)

	// Find command for agent-2
	found, ok := q.DequeueWithFilter(func(c *AgentCommand) bool {
		return c.AgentID == "agent-2"
	})

	if !ok {
		t.Fatal("Should find command for agent-2")
	}
	if found.ID != "cmd2" {
		t.Errorf("Expected cmd2, got %s", found.ID)
	}

	// Queue should have 2 items left
	if q.Size() != 2 {
		t.Errorf("Queue size should be 2, got %d", q.Size())
	}

	// Next filter for agent-1 should get cmd1 (FIFO order)
	found, ok = q.DequeueWithFilter(func(c *AgentCommand) bool {
		return c.AgentID == "agent-1"
	})
	if !ok || found.ID != "cmd1" {
		t.Errorf("Expected cmd1, got %s", found.ID)
	}
}

// TestBackpressureQueueConcurrentAccess tests thread safety.
func TestBackpressureQueueConcurrentAccess(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          1000,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[int]("test-concurrent", cfg, log)

	var wg sync.WaitGroup
	numGoroutines := 10
	itemsPerGoroutine := 50

	// Concurrent enqueue
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := 0; j < itemsPerGoroutine; j++ {
				q.Enqueue(start*itemsPerGoroutine + j)
			}
		}(i)
	}

	wg.Wait()

	expectedSize := numGoroutines * itemsPerGoroutine
	if q.Size() != expectedSize {
		t.Errorf("Size mismatch after concurrent enqueue: got %d, want %d", q.Size(), expectedSize)
	}

	// Concurrent dequeue
	var dequeued int64
	var dequeueMu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := 0
			for j := 0; j < itemsPerGoroutine; j++ {
				if _, ok := q.TryDequeue(); ok {
					count++
				}
			}
			dequeueMu.Lock()
			dequeued += int64(count)
			dequeueMu.Unlock()
		}()
	}

	wg.Wait()

	if dequeued != int64(expectedSize) {
		t.Errorf("Dequeued count mismatch: got %d, want %d", dequeued, expectedSize)
	}

	if q.Size() != 0 {
		t.Errorf("Queue should be empty after dequeuing all, got %d", q.Size())
	}
}
