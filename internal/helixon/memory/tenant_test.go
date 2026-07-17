package memory

import (
	"context"
	"testing"
)

// TestTenant_CrossTenantIsolation_InMemoryBackend verifies that two tenants
// (tenant-a, tenant-b) cannot see each other's memory entries, even when
// they share the same AppID and UserID. This is the core multi-tenancy
// invariant for the memory backend per v18680-3 pattern + v18684-4.
func TestTenant_CrossTenantIsolation_InMemoryBackend(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()

	// Tenant A stores 2 memories.
	for i, content := range []string{"alpha", "beta"} {
		entry := &Memory{
			Content:  content,
			AppID:    "shared-app",
			UserID:   "shared-user",
			TenantID: "tenant-a",
		}
		if i == 0 {
			entry.ID = "a-1"
		} else {
			entry.ID = "a-2"
		}
		if err := b.Store(ctx, entry); err != nil {
			t.Fatalf("tenant-a Store[%d]: %v", i, err)
		}
	}

	// Tenant B stores 1 memory with the same AppID + UserID but different tenant.
	bEntry := &Memory{
		ID:       "b-1",
		Content:  "gamma",
		AppID:    "shared-app",
		UserID:   "shared-user",
		TenantID: "tenant-b",
	}
	if err := b.Store(ctx, bEntry); err != nil {
		t.Fatalf("tenant-b Store: %v", err)
	}

	// Tenant A's Search must see only its 2 memories.
	aResults, err := b.Search(ctx, "", "shared-app", "shared-user", "tenant-a", 10)
	if err != nil {
		t.Fatalf("tenant-a Search: %v", err)
	}
	if len(aResults) != 2 {
		t.Errorf("tenant-a Search returned %d results, want 2 (tenant-b entry must not leak)",
			len(aResults))
		for i, r := range aResults {
			t.Logf("  [%d] id=%q tenant=%q content=%q", i, r.ID, r.TenantID, r.Content)
		}
	}

	// Tenant B's Search must see only its 1 memory.
	bResults, err := b.Search(ctx, "", "shared-app", "shared-user", "tenant-b", 10)
	if err != nil {
		t.Fatalf("tenant-b Search: %v", err)
	}
	if len(bResults) != 1 {
		t.Errorf("tenant-b Search returned %d results, want 1 (tenant-a entries must not leak)",
			len(bResults))
	}

	// Tenant A's Recall of tenant B's entry must return not-found.
	if _, err := b.Recall(ctx, "b-1", "tenant-a"); err == nil {
		t.Errorf("tenant-a Recall(b-1) succeeded; should return ErrMemoryNotFound")
	}

	// Tenant B's Recall of tenant A's entry must return not-found.
	if _, err := b.Recall(ctx, "a-1", "tenant-b"); err == nil {
		t.Errorf("tenant-b Recall(a-1) succeeded; should return ErrMemoryNotFound")
	}

	// Owner can still recall their own.
	got, err := b.Recall(ctx, "a-1", "tenant-a")
	if err != nil {
		t.Errorf("tenant-a Recall(a-1): %v", err)
	}
	if got != nil && got.TenantID != "tenant-a" {
		t.Errorf("tenant-a Recall(a-1).TenantID = %q, want tenant-a", got.TenantID)
	}
}

// TestTenant_EmptyTenantID_SeesAll verifies backward compatibility:
// entries with empty TenantID are visible to all tenant-filtered queries.
// This matches the v18680-3 backward-compat semantics.
func TestTenant_EmptyTenantID_SeesAll(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()

	// Legacy entry with no tenant.
	legacy := &Memory{
		ID:      "legacy-1",
		Content: "pre-tenant-migration",
		AppID:   "shared-app",
		UserID:  "u1",
	}
	if err := b.Store(ctx, legacy); err != nil {
		t.Fatalf("legacy Store: %v", err)
	}

	// Any tenantID filter returns the legacy entry.
	for _, tenantID := range []string{"", "tenant-a", "tenant-b"} {
		results, err := b.Search(ctx, "", "shared-app", "u1", tenantID, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", tenantID, err)
		}
		if len(results) != 1 {
			t.Errorf("Search(tenantID=%q) returned %d, want 1 (legacy entry must be visible)",
				tenantID, len(results))
		}
	}
}

// TestTenant_Store_StampsTenantID verifies Store normalises the TenantID
// field (empty string stays empty; non-empty stays non-empty). This is
// the data-layer equivalent of "row-level security".
func TestTenant_Store_StampsTenantID(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()

	entry := &Memory{
		Content:  "tenant-a-secret",
		AppID:    "app",
		UserID:   "u1",
		TenantID: "tenant-a",
	}
	if err := b.Store(ctx, entry); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if entry.TenantID != "tenant-a" {
		t.Errorf("Store altered TenantID: got %q, want tenant-a", entry.TenantID)
	}

	got, err := b.Recall(ctx, entry.ID, "tenant-a")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if got.TenantID != "tenant-a" {
		t.Errorf("Recall.TenantID = %q, want tenant-a", got.TenantID)
	}
}
