package rbac

import (
	"context"
	"fmt"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Config defines which Kubernetes resource and verb to check for RBAC access.
type Config struct {
	Resource string // e.g. "pods"
	APIGroup string // e.g. "" (core) or "metrics.k8s.io"
	Verb     string // e.g. "get" or "list"
}

// Checker performs Kubernetes RBAC checks using SubjectAccessReview.
type Checker struct {
	client      kubernetes.Interface
	config      Config
	concurrency int
}

// NewChecker creates a Checker that queries the Kubernetes authorization API.
func NewChecker(client kubernetes.Interface, config Config) *Checker {
	return &Checker{
		client:      client,
		config:      config,
		concurrency: 20,
	}
}

// AllowedNamespaces returns the list of namespaces where the given user (with
// optional groups) has the configured access level.
func (c *Checker) AllowedNamespaces(ctx context.Context, username string, groups []string) ([]string, error) {
	nsList, err := c.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		allowed  []string
		checkErr error
	)

	sem := make(chan struct{}, c.concurrency)

	for _, ns := range nsList.Items {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			sar := &authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					User:   username,
					Groups: groups,
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: namespace,
						Verb:      c.config.Verb,
						Resource:  c.config.Resource,
						Group:     c.config.APIGroup,
					},
				},
			}

			result, err := c.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
			if err != nil {
				mu.Lock()
				if checkErr == nil {
					checkErr = fmt.Errorf("SubjectAccessReview for namespace %q: %w", namespace, err)
				}
				mu.Unlock()
				cancel()
				return
			}

			if result.Status.Allowed {
				mu.Lock()
				allowed = append(allowed, namespace)
				mu.Unlock()
			}
		}(ns.Name)
	}

	wg.Wait()
	if checkErr != nil {
		return nil, checkErr
	}

	return allowed, nil
}
