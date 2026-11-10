/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rest

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac/bootstrappolicy"
)

func namespaceActions(client *fake.Clientset, verb string) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "namespaces" && action.GetVerb() == verb {
			count++
		}
	}
	return count
}

func TestBootstrapNamespaceProbes(t *testing.T) {
	client := fake.NewSimpleClientset()
	policy := &PolicyData{
		Roles:        bootstrappolicy.NamespaceRoles(),
		RoleBindings: bootstrappolicy.NamespaceRoleBindings(),
	}
	if done, err := ensureRBACPolicy(policy, client); err != nil || !done {
		t.Fatalf("bootstrap = (%v, %v), want (true, nil)", done, err)
	}
	namespaces := map[string]bool{}
	for ns := range policy.Roles {
		namespaces[ns] = true
	}
	for ns := range policy.RoleBindings {
		namespaces[ns] = true
	}
	if got := namespaceActions(client, "get"); got != len(namespaces) {
		t.Errorf("namespace Gets = %d, want one for each of %d bootstrap namespaces", got, len(namespaces))
	}
	for ns, roles := range policy.Roles {
		for _, role := range roles {
			if _, err := client.RbacV1().Roles(ns).Get(context.TODO(), role.Name, metav1.GetOptions{}); err != nil {
				t.Errorf("bootstrap Role %s/%s missing: %v", ns, role.Name, err)
			}
		}
	}
	for ns, bindings := range policy.RoleBindings {
		for _, binding := range bindings {
			if _, err := client.RbacV1().RoleBindings(ns).Get(context.TODO(), binding.Name, metav1.GetOptions{}); err != nil {
				t.Errorf("bootstrap RoleBinding %s/%s missing: %v", ns, binding.Name, err)
			}
		}
	}
}

func TestBootstrapNamespaceClient(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "existing"}})
	probe := &bootstrapNamespaceClient{NamespaceInterface: client.CoreV1().Namespaces(), existing: map[string]*corev1.Namespace{}}
	first, err := probe.Get(ctx, "existing", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first.Labels = map[string]string{"mutated": "true"}
	second, err := probe.Get(ctx, "existing", metav1.GetOptions{})
	if err != nil || len(second.Labels) != 0 || namespaceActions(client, "get") != 1 {
		t.Fatalf("successful probe was not reused independently: namespace=%v err=%v", second, err)
	}
	if _, err := probe.Get(ctx, "missing", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing namespace error = %v", err)
	}
	if _, err := probe.Get(ctx, "missing", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("second missing namespace error = %v", err)
	}
	if got := namespaceActions(client, "get"); got != 3 {
		t.Errorf("failed probes must not be cached: Gets=%d, want 3", got)
	}
	if _, err := probe.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "created"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Get(ctx, "created", metav1.GetOptions{}); err != nil || namespaceActions(client, "get") != 3 {
		t.Fatalf("successful Create was not reused: err=%v", err)
	}
	if _, err := probe.Get(ctx, "existing", metav1.GetOptions{ResourceVersion: "42"}); err != nil || namespaceActions(client, "get") != 4 {
		t.Fatalf("explicit resourceVersion should delegate, err=%v", err)
	}
}

func TestBootstrapNamespaceDeletionRetriesWithFreshProbe(t *testing.T) {
	const ns = "bootstrap"
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	policy := &PolicyData{Roles: map[string][]rbacv1.Role{ns: {{
		ObjectMeta: metav1.ObjectMeta{Name: "role", Namespace: ns},
		Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}}},
	}}}}
	namespaceGVR := corev1.SchemeGroupVersion.WithResource("namespaces")
	deleteOnce := true
	client.PrependReactor("get", "namespaces", func(action clienttesting.Action) (bool, runtime.Object, error) {
		handled, obj, err := clienttesting.ObjectReaction(client.Tracker())(action)
		if err == nil && deleteOnce {
			deleteOnce = false
			if err := client.Tracker().Delete(namespaceGVR, "", ns); err != nil {
				t.Fatal(err)
			}
		}
		return handled, obj, err
	})
	// Model NamespaceLifecycle admission, which the fake client does not run.
	client.PrependReactor("create", "roles", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if _, err := client.Tracker().Get(namespaceGVR, "", action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	if done, err := ensureRBACPolicy(policy, client); err != nil || done {
		t.Fatalf("deleted namespace must fail the attempt: done=%v err=%v", done, err)
	}
	if done, err := ensureRBACPolicy(policy, client); err != nil || !done {
		t.Fatalf("next attempt must repair namespace with a fresh probe: done=%v err=%v", done, err)
	}
	if got := namespaceActions(client, "get"); got != 2 {
		t.Errorf("namespace must be rechecked in the next attempt: Gets=%d, want 2", got)
	}
}
