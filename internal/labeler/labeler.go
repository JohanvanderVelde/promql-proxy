package labeler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus-community/prom-label-proxy/injectproxy"
)

// NamespaceResolver resolves the namespaces a user has access to.
type NamespaceResolver interface {
	AllowedNamespaces(ctx context.Context, username string, groups []string) ([]string, error)
}

// NamespaceCache caches user-to-namespace mappings.
type NamespaceCache interface {
	Get(user string) ([]string, bool)
	Set(user string, namespaces []string)
}

// RBACLabeler implements the prom-label-proxy ExtractLabeler interface.
// It extracts the username from an HTTP header, resolves allowed namespaces
// through Kubernetes RBAC (with caching), and stores them in the request context.
type RBACLabeler struct {
	headerName   string
	groupsHeader string
	checker      NamespaceResolver
	cache        NamespaceCache
	logger       *slog.Logger
}

// NewRBACLabeler creates a new RBACLabeler.
func NewRBACLabeler(headerName, groupsHeader string, checker NamespaceResolver, nsCache NamespaceCache, logger *slog.Logger) *RBACLabeler {
	return &RBACLabeler{
		headerName:   headerName,
		groupsHeader: groupsHeader,
		checker:      checker,
		cache:        nsCache,
		logger:       logger,
	}
}

// ExtractLabel implements injectproxy.ExtractLabeler.
func (l *RBACLabeler) ExtractLabel(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(l.headerName)
		if user == "" {
			http.Error(w, fmt.Sprintf("missing required header %q", l.headerName), http.StatusBadRequest)
			return
		}

		namespaces, ok := l.cache.Get(user)
		if !ok {
			groups := parseGroups(r.Header.Values(l.groupsHeader))

			var err error
			namespaces, err = l.checker.AllowedNamespaces(r.Context(), user, groups)
			if err != nil {
				l.logger.Error("RBAC check failed", "user", user, "error", err)
				http.Error(w, "failed to determine namespace access", http.StatusInternalServerError)
				return
			}

			l.cache.Set(user, namespaces)
			l.logger.Info("resolved namespaces", "user", user, "count", len(namespaces), "namespaces", strings.Join(namespaces, "|"))
		}

		if len(namespaces) == 0 {
			http.Error(w, "no accessible namespaces for user", http.StatusForbidden)
			return
		}

		l.logger.Info("proxying request", "user", user, "namespaces", strings.Join(namespaces, "|"), "path", r.URL.Path)

		ctx := injectproxy.WithLabelValues(r.Context(), namespaces)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// parseGroups splits header values that may be comma-separated.
func parseGroups(values []string) []string {
	var groups []string
	for _, v := range values {
		for _, g := range strings.Split(v, ",") {
			g = strings.TrimSpace(g)
			if g != "" {
				groups = append(groups, g)
			}
		}
	}
	return groups
}
