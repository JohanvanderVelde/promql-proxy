package rbac

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func newFakeClient(namespaces []string, allowedNamespaces map[string]bool) *fake.Clientset {
	var nsObjects []runtime.Object
	for _, ns := range namespaces {
		nsObjects = append(nsObjects, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})
	}
	client := fake.NewSimpleClientset(nsObjects...)

	client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		ns := sar.Spec.ResourceAttributes.Namespace
		sar.Status.Allowed = allowedNamespaces[ns]
		return true, sar, nil
	})

	return client
}

func TestAllowedNamespaces(t *testing.T) {
	client := newFakeClient(
		[]string{"default", "kube-system", "prod", "dev"},
		map[string]bool{"prod": true, "dev": true},
	)

	checker := NewChecker(client, Config{Resource: "pods", Verb: "get"})
	got, err := checker.AllowedNamespaces(context.Background(), "alice", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	allowed := make(map[string]bool)
	for _, ns := range got {
		allowed[ns] = true
	}

	if !allowed["prod"] || !allowed["dev"] {
		t.Fatalf("expected prod and dev, got %v", got)
	}
	if allowed["default"] || allowed["kube-system"] {
		t.Fatalf("expected default and kube-system to be excluded, got %v", got)
	}
}

func TestAllowedNamespaces_NoneAllowed(t *testing.T) {
	client := newFakeClient(
		[]string{"default", "kube-system"},
		map[string]bool{},
	)

	checker := NewChecker(client, Config{Resource: "pods", Verb: "get"})
	got, err := checker.AllowedNamespaces(context.Background(), "alice", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestAllowedNamespaces_AllAllowed(t *testing.T) {
	client := newFakeClient(
		[]string{"ns1", "ns2", "ns3"},
		map[string]bool{"ns1": true, "ns2": true, "ns3": true},
	)

	checker := NewChecker(client, Config{Resource: "pods", Verb: "get"})
	got, err := checker.AllowedNamespaces(context.Background(), "admin", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 namespaces, got %v", got)
	}
}

func TestAllowedNamespaces_ContextCancelled(t *testing.T) {
	// Use a reactor that returns an error to simulate a cancelled context scenario.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
	)

	client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.Canceled
	})

	checker := NewChecker(client, Config{Resource: "pods", Verb: "get"})
	_, err := checker.AllowedNamespaces(context.Background(), "alice", nil)
	if err == nil {
		t.Fatal("expected error when SAR calls fail")
	}
}
