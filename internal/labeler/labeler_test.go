package labeler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// --- mocks ---

type mockResolver struct {
	namespaces []string
	err        error
}

func (m *mockResolver) AllowedNamespaces(_ context.Context, _ string, _ []string) ([]string, error) {
	return m.namespaces, m.err
}

type mockCache struct {
	data map[string][]string
}

func newMockCache() *mockCache {
	return &mockCache{data: make(map[string][]string)}
}

func (m *mockCache) Get(user string) ([]string, bool) {
	ns, ok := m.data[user]
	return ns, ok
}

func (m *mockCache) Set(user string, namespaces []string) {
	m.data[user] = namespaces
}

// --- tests ---

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestExtractLabel_MissingHeader(t *testing.T) {
	l := NewRBACLabeler("X-Forwarded-User", "X-Forwarded-Groups",
		&mockResolver{}, newMockCache(), testLogger())

	handler := l.ExtractLabel(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestExtractLabel_CacheHit(t *testing.T) {
	c := newMockCache()
	c.Set("alice", []string{"prod", "dev"})

	var called bool
	l := NewRBACLabeler("X-Forwarded-User", "X-Forwarded-Groups",
		&mockResolver{err: errors.New("should not be called")}, c, testLogger())

	handler := l.ExtractLabel(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestExtractLabel_CacheMiss_ResolvesAndCaches(t *testing.T) {
	c := newMockCache()
	resolver := &mockResolver{namespaces: []string{"staging"}}

	var called bool
	l := NewRBACLabeler("X-Forwarded-User", "X-Forwarded-Groups",
		resolver, c, testLogger())

	handler := l.ExtractLabel(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called")
	}
	// Check that the cache was populated
	if ns, ok := c.Get("bob"); !ok || len(ns) != 1 || ns[0] != "staging" {
		t.Fatalf("cache was not populated correctly: %v", ns)
	}
}

func TestExtractLabel_NoNamespaces(t *testing.T) {
	l := NewRBACLabeler("X-Forwarded-User", "X-Forwarded-Groups",
		&mockResolver{namespaces: []string{}}, newMockCache(), testLogger())

	handler := l.ExtractLabel(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "nobody")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestExtractLabel_ResolverError(t *testing.T) {
	l := NewRBACLabeler("X-Forwarded-User", "X-Forwarded-Groups",
		&mockResolver{err: errors.New("k8s down")}, newMockCache(), testLogger())

	handler := l.ExtractLabel(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestParseGroups(t *testing.T) {
	tests := []struct {
		name   string
		input  []string
		expect []string
	}{
		{"empty", nil, nil},
		{"single value", []string{"team-a"}, []string{"team-a"}},
		{"comma separated", []string{"team-a, team-b"}, []string{"team-a", "team-b"}},
		{"multiple headers", []string{"team-a", "team-b,team-c"}, []string{"team-a", "team-b", "team-c"}},
		{"whitespace only", []string{" , "}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGroups(tt.input)
			if len(got) != len(tt.expect) {
				t.Fatalf("expected %v, got %v", tt.expect, got)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					t.Fatalf("at index %d: expected %q, got %q", i, tt.expect[i], got[i])
				}
			}
		})
	}
}
