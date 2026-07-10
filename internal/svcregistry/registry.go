package svcregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Registry is the in-memory service table backed by a JSON file on disk.
// Construct via New(path). All public methods are safe for concurrent use.
type Registry struct {
	path     string
	mu       sync.RWMutex
	services map[string]Service
	metrics  *Metrics
}

// New returns a Registry backed by path. It does NOT load from disk —
// call Load for that.
func New(path string, opts ...Option) *Registry {
	r := &Registry{
		path:     path,
		services: make(map[string]Service),
		metrics:  newMetrics(),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Option is a functional option for Registry construction.
type Option func(*Registry)

// WithMetrics replaces the default metrics collector. The supplied
// Metrics is NOT registered with prometheus.DefaultRegisterer — the
// caller owns that lifecycle.
func WithMetrics(m *Metrics) Option {
	return func(r *Registry) {
		r.metrics = m
	}
}

// Path returns the configured backing-file path.
func (r *Registry) Path() string { return r.path }

// Load reads the JSON snapshot from path. A missing file yields an empty
// registry (no error). A corrupt file yields an error so the operator
// can inspect before overwriting.
func (r *Registry) Load() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("svcregistry: read %s: %w", r.path, err)
	}
	if len(data) == 0 {
		return nil
	}
	var snap []Service
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("svcregistry: decode %s: %w", r.path, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services = make(map[string]Service, len(snap))
	for _, s := range snap {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("svcregistry: invalid entry %s: %w", s.Key(), err)
		}
		r.services[s.Key()] = s
	}
	r.metrics.Inc(OpList, StatusOK)
	return nil
}

// Register adds (or updates) a service. Returns ErrPortConflict if another
// registered service is bound to the same (host, port, protocol) tuple.
func (r *Registry) Register(s Service) error {
	if err := s.Validate(); err != nil {
		r.metrics.Inc(OpRegister, StatusError)
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.findByPort(s.Host, s.Port, s.Protocol); ok && existing.Key() != s.Key() {
		r.metrics.Inc(OpConflict, StatusOK)
		r.metrics.Inc(OpRegister, StatusError)
		return fmt.Errorf("%w: %s:%d/%s held by %s",
			ErrPortConflict, s.Host, s.Port, s.Protocol, existing.Name)
	}
	r.services[s.Key()] = s.WithDefaults()
	r.metrics.Inc(OpRegister, StatusOK)
	return nil
}

// Unregister removes a service. Returns ErrNotFound if absent.
func (r *Registry) Unregister(host, name string) error {
	k := host + "/" + name
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.services[k]; !ok {
		r.metrics.Inc(OpUnregister, StatusError)
		return fmt.Errorf("%w: %s", ErrNotFound, k)
	}
	delete(r.services, k)
	r.metrics.Inc(OpUnregister, StatusOK)
	return nil
}

// List returns all services in deterministic (host, name) order.
func (r *Registry) List() []Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.metrics.Inc(OpList, StatusOK)
	out := make([]Service, 0, len(r.services))
	for _, s := range r.services {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Get returns the service at host/name and true, or zero-value and false.
func (r *Registry) Get(host, name string) (Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.services[host+"/"+name]
	return s, ok
}

// Size returns the number of registered services.
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.services)
}

// Conflicts returns all sets of services that share the same (host, port,
// protocol). Empty list means no collisions.
func (r *Registry) Conflicts() [][]Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	groups := make(map[string][]Service)
	for _, s := range r.services {
		k := fmt.Sprintf("%s:%d/%s", s.Host, s.Port, s.Protocol)
		groups[k] = append(groups[k], s)
	}
	var out [][]Service
	for _, g := range groups {
		if len(g) > 1 {
			sort.Slice(g, func(i, j int) bool { return g[i].Name < g[j].Name })
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i][0].Host < out[j][0].Host ||
			(out[i][0].Host == out[j][0].Host && out[i][0].Port < out[j][0].Port)
	})
	return out
}

// Save writes the snapshot to path atomically (write-temp + rename).
// The parent directory is created with 0o755 if missing.
func (r *Registry) Save() error {
	snap := r.List()
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("svcregistry: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("svcregistry: mkdir %s: %w", filepath.Dir(r.path), err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("svcregistry: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("svcregistry: rename %s: %w", r.path, err)
	}
	return nil
}

// findByPort returns the first service bound to (host, port, protocol).
// Caller must hold r.mu (read or write).
func (r *Registry) findByPort(host string, port int, proto string) (Service, bool) {
	for _, s := range r.services {
		if s.Host == host && s.Port == port && s.Protocol == proto {
			return s, true
		}
	}
	return Service{}, false
}

// rawInsert bypasses conflict detection and writes s directly into the
// map. It is intended for tests that need to seed a known-conflict
// state. Returns true if s.Validate passes.
func (r *Registry) rawInsert(s Service) bool {
	if err := s.Validate(); err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[s.Key()] = s
	return true
}
