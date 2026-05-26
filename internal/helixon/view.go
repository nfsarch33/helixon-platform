package helixon

import "time"

// AgentID returns the configured agent identifier.
func (r *Runtime) AgentID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.AgentID
}

// PhaseString returns Phase() as a plain string so adapters that depend on
// a string-typed view (e.g. the dashboard package) can avoid importing the
// Phase type. Phase() is preserved for typed callers.
func (r *Runtime) PhaseString() string {
	return string(r.Phase())
}

// HeartbeatEvery returns the configured heartbeat interval.
func (r *Runtime) HeartbeatEvery() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.HeartbeatEvery
}

// ChannelCount returns the number of registered channels.
func (r *Runtime) ChannelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.channels)
}

// RegisteredToolCount returns the number of registered tools (0 before Init).
func (r *Runtime) RegisteredToolCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.registry == nil {
		return 0
	}
	return len(r.registry.Names())
}
