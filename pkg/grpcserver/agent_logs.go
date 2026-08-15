package grpcserver

import (
	"fmt"
	"sync"
)

// addAgentLog appends a log entry to the per-agent ring buffer
// and broadcasts it to subscribers.
func (s *Server) addAgentLog(agentID string, entry AgentLogEntry) {
	entry.AgentID = agentID
	if agent, ok := s.GetAgent(agentID); ok && entry.TenantID == "" {
		entry.TenantID = agent.Tenant
	}
	if entry.Source == "" {
		entry.Source = "agent-grpc"
	}
	entry = s.appendRuntimeLog(entry)

	s.agentLogsMu.Lock()
	defer s.agentLogsMu.Unlock()

	if s.agentLogs == nil {
		s.agentLogs = make(map[string][]AgentLogEntry)
	}
	if s.agentLogSubscribers == nil {
		s.agentLogSubscribers = make(map[string]map[chan AgentLogEntry]struct{})
	}

	const maxLogsPerAgent = 500
	logs := append(s.agentLogs[agentID], entry)
	if len(logs) > maxLogsPerAgent {
		logs = logs[len(logs)-maxLogsPerAgent:]
	}
	s.agentLogs[agentID] = logs

	for ch := range s.agentLogSubscribers[agentID] {
		select {
		case ch <- entry:
		default:
			// Drop if subscriber is too slow. Keeps CommandStream responsive.
		}
	}
}

// GetAgentLogs returns up to limit latest logs for an agent.
// If limit <= 0, all buffered logs are returned.
func (s *Server) GetAgentLogs(agentID string, limit int) ([]AgentLogEntry, bool) {
	// Ensure agent exists (avoid leaking IDs)
	if _, ok := s.GetAgent(agentID); !ok {
		return nil, false
	}

	s.agentLogsMu.RLock()
	defer s.agentLogsMu.RUnlock()

	logs := s.agentLogs[agentID]
	if len(logs) == 0 {
		return []AgentLogEntry{}, true
	}

	if limit > 0 && len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}

	out := make([]AgentLogEntry, len(logs))
	copy(out, logs)
	return out, true
}

// SubscribeAgentLogs subscribes to future log entries for an agent.
// The returned cancel function MUST be called.
func (s *Server) SubscribeAgentLogs(agentID string, buffer int) (<-chan AgentLogEntry, func(), error) {
	if _, ok := s.GetAgent(agentID); !ok {
		return nil, nil, fmt.Errorf("agent not found: %s", agentID)
	}

	if buffer <= 0 {
		buffer = 100
	}

	ch := make(chan AgentLogEntry, buffer)

	s.agentLogsMu.Lock()
	if s.agentLogSubscribers == nil {
		s.agentLogSubscribers = make(map[string]map[chan AgentLogEntry]struct{})
	}
	if s.agentLogSubscribers[agentID] == nil {
		s.agentLogSubscribers[agentID] = make(map[chan AgentLogEntry]struct{})
	}
	s.agentLogSubscribers[agentID][ch] = struct{}{}
	s.agentLogsMu.Unlock()

	once := sync.Once{}
	cancel := func() {
		once.Do(func() {
			s.agentLogsMu.Lock()
			if subs, ok := s.agentLogSubscribers[agentID]; ok {
				delete(subs, ch)
				if len(subs) == 0 {
					delete(s.agentLogSubscribers, agentID)
				}
			}
			s.agentLogsMu.Unlock()
			close(ch)
		})
	}

	return ch, cancel, nil
}

func (s *Server) cleanupAgentLogs(agentID string) {
	s.agentLogsMu.Lock()
	defer s.agentLogsMu.Unlock()

	// Close active subscribers
	if subs, ok := s.agentLogSubscribers[agentID]; ok {
		for ch := range subs {
			close(ch)
		}
		delete(s.agentLogSubscribers, agentID)
	}

	delete(s.agentLogs, agentID)
}
