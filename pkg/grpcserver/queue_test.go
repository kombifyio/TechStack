package grpcserver

import (
	"sync"
	"testing"
	"time"

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

	// Test peek
	item, ok := q.Peek()
	if !ok || item != "item1" {
		t.Errorf("Peek failed: got %s, want item1", item)
	}

	// Test dequeue
	item, ok = q.TryDequeue()
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

	if !q.IsFull() {
		t.Error("Queue should be full")
	}

	// Try to add one more - should fail
	err := q.Enqueue(3)
	if err == nil {
		t.Error("Enqueue to full queue should fail with reject strategy")
	}

	if !IsQueueFullError(err) {
		t.Errorf("Error should be QueueFullError, got: %v", err)
	}

	// Verify stats
	stats := q.Stats()
	if stats.TotalRejected != 1 {
		t.Errorf("TotalRejected should be 1, got %d", stats.TotalRejected)
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

	// Verify stats
	stats := q.Stats()
	if stats.TotalDropped != 1 {
		t.Errorf("TotalDropped should be 1, got %d", stats.TotalDropped)
	}

	// First item should now be 1 (0 was dropped)
	item, ok := q.TryDequeue()
	if !ok || item != 1 {
		t.Errorf("First item should be 1 (0 was dropped), got %d", item)
	}
}

// TestBackpressureQueueWarningThreshold tests warning threshold behavior.
func TestBackpressureQueueWarningThreshold(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          10,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 50, // 50% = 5 items
	}

	q := NewBackpressureQueue[int]("test-warning", cfg, log)

	// Fill to just below threshold
	for i := 0; i < 4; i++ {
		q.Enqueue(i)
	}

	if q.IsAboveThreshold() {
		t.Error("Should not be above threshold at 4/10")
	}

	// Add one more to reach threshold
	q.Enqueue(4)
	if !q.IsAboveThreshold() {
		t.Error("Should be above threshold at 5/10 with 50% threshold")
	}
}

// TestBackpressureQueueDequeueWithFilter tests filtered dequeue.
func TestBackpressureQueueDequeueWithFilter(t *testing.T) {
	log := logger.New("error", "text")

	cfg := DefaultQueueConfig()
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

// TestBackpressureQueueStats tests statistics collection.
func TestBackpressureQueueStats(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          5,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[int]("test-stats", cfg, log)

	// Enqueue some items
	for i := 0; i < 3; i++ {
		q.Enqueue(i)
	}

	// Dequeue one
	q.TryDequeue()

	// Try to overflow
	for i := 0; i < 10; i++ {
		q.Enqueue(i + 100)
	}

	stats := q.Stats()

	if stats.MaxSize != 5 {
		t.Errorf("MaxSize should be 5, got %d", stats.MaxSize)
	}

	if stats.TotalEnqueued < 5 {
		t.Errorf("TotalEnqueued should be at least 5, got %d", stats.TotalEnqueued)
	}

	if stats.TotalDequeued != 1 {
		t.Errorf("TotalDequeued should be 1, got %d", stats.TotalDequeued)
	}

	if stats.TotalRejected == 0 {
		t.Error("TotalRejected should be > 0 after overflow attempts")
	}
}

// TestBackpressureQueueClear tests the Clear method.
func TestBackpressureQueueClear(t *testing.T) {
	log := logger.New("error", "text")

	cfg := DefaultQueueConfig()
	q := NewBackpressureQueue[int]("test-clear", cfg, log)

	// Add items
	for i := 0; i < 10; i++ {
		q.Enqueue(i)
	}

	if q.Size() != 10 {
		t.Errorf("Size should be 10, got %d", q.Size())
	}

	q.Clear()

	if q.Size() != 0 {
		t.Errorf("Size should be 0 after Clear, got %d", q.Size())
	}
}

// TestCommandQueueIntegration tests the command queue wrapper.
func TestCommandQueueIntegration(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          100,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewCommandQueue(cfg, log)

	cmd := &AgentCommand{
		ID:      "test-cmd",
		AgentID: "test-agent",
		Type:    "health_check",
		Command: "echo hello",
	}

	if err := q.Enqueue(cmd); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	retrieved, ok := q.TryDequeue()
	if !ok {
		t.Fatal("TryDequeue should succeed")
	}

	if retrieved.ID != cmd.ID {
		t.Errorf("Command ID mismatch: got %s, want %s", retrieved.ID, cmd.ID)
	}
}

// TestQueueFullError tests the QueueFullError type.
func TestQueueFullError(t *testing.T) {
	err := &QueueFullError{
		QueueName:   "test",
		CurrentSize: 100,
		MaxSize:     100,
	}

	if !IsQueueFullError(err) {
		t.Error("IsQueueFullError should return true")
	}

	expectedMsg := "test queue full: 100/100 items"
	if err.Error() != expectedMsg {
		t.Errorf("Error message mismatch: got %q, want %q", err.Error(), expectedMsg)
	}

	// Test with non-QueueFullError
	if IsQueueFullError(nil) {
		t.Error("IsQueueFullError(nil) should return false")
	}
}

// TestAgentQueueManager tests per-agent queue management.
func TestAgentQueueManager(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          50,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	mgr := NewAgentQueueManager(cfg, log, 10)

	// Get or create queue for agent-1
	q1, err := mgr.GetOrCreateQueue("agent-1")
	if err != nil {
		t.Fatalf("GetOrCreateQueue failed: %v", err)
	}

	// Enqueue a command
	cmd := &AgentCommand{ID: "cmd1", AgentID: "agent-1", Type: "health_check"}
	if err := q1.Enqueue(cmd); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Get the same queue again
	q1Again, err := mgr.GetOrCreateQueue("agent-1")
	if err != nil {
		t.Fatalf("GetOrCreateQueue failed: %v", err)
	}

	if q1Again.Size() != 1 {
		t.Error("Should get the same queue instance")
	}

	// Get queue for different agent
	q2, err := mgr.GetOrCreateQueue("agent-2")
	if err != nil {
		t.Fatalf("GetOrCreateQueue failed: %v", err)
	}

	if q2.Size() != 0 {
		t.Error("New agent queue should be empty")
	}

	// Check all stats
	allStats := mgr.AllStats()
	if len(allStats) != 2 {
		t.Errorf("Should have 2 agent queues, got %d", len(allStats))
	}

	// Remove queue
	mgr.RemoveQueue("agent-1")
	allStats = mgr.AllStats()
	if len(allStats) != 1 {
		t.Errorf("Should have 1 agent queue after removal, got %d", len(allStats))
	}
}

// TestAgentQueueManagerMaxAgents tests the max agents limit.
func TestAgentQueueManagerMaxAgents(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          50,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	mgr := NewAgentQueueManager(cfg, log, 3)

	// Create max agents
	for i := 0; i < 3; i++ {
		_, err := mgr.GetOrCreateQueue("agent-" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("GetOrCreateQueue failed: %v", err)
		}
	}

	// Try to create one more - should fail
	_, err := mgr.GetOrCreateQueue("agent-overflow")
	if err == nil {
		t.Error("Should fail when max agents exceeded")
	}
}

// TestQueueMetrics tests the metrics collection.
func TestQueueMetrics(t *testing.T) {
	log := logger.New("error", "text")

	cfg := QueueConfig{
		MaxSize:          10,
		OverflowStrategy: OverflowReject,
		WarningThreshold: 80,
	}

	q := NewBackpressureQueue[int]("test-metrics", cfg, log)

	// Enqueue some items
	for i := 0; i < 5; i++ {
		q.Enqueue(i)
	}

	// Dequeue one
	q.TryDequeue()

	metrics := q.GetMetrics()

	if metrics.Size() != 4 {
		t.Errorf("Size metric should be 4, got %f", metrics.Size())
	}

	if metrics.MaxSize != 10 {
		t.Errorf("MaxSize metric should be 10, got %f", metrics.MaxSize)
	}

	if metrics.TotalEnqueued() != 5 {
		t.Errorf("TotalEnqueued metric should be 5, got %f", metrics.TotalEnqueued())
	}

	if metrics.TotalDequeued() != 1 {
		t.Errorf("TotalDequeued metric should be 1, got %f", metrics.TotalDequeued())
	}
}

// TestBackpressureQueueNotifyChannel tests the notification channel.
func TestBackpressureQueueNotifyChannel(t *testing.T) {
	log := logger.New("error", "text")

	cfg := DefaultQueueConfig()
	q := NewBackpressureQueue[int]("test-notify", cfg, log)

	notifyCh := q.NotifyChan()

	// Enqueue should trigger notification
	go func() {
		time.Sleep(10 * time.Millisecond)
		q.Enqueue(1)
	}()

	select {
	case <-notifyCh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Should receive notification after enqueue")
	}
}

// TestDefaultQueueConfig tests the default configuration.
func TestDefaultQueueConfig(t *testing.T) {
	cfg := DefaultQueueConfig()

	if cfg.MaxSize != 1000 {
		t.Errorf("Default MaxSize should be 1000, got %d", cfg.MaxSize)
	}

	if cfg.OverflowStrategy != OverflowReject {
		t.Errorf("Default OverflowStrategy should be reject, got %s", cfg.OverflowStrategy)
	}

	if cfg.WarningThreshold != 80 {
		t.Errorf("Default WarningThreshold should be 80, got %d", cfg.WarningThreshold)
	}
}
