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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// bootstrapNamespaceClient reuses successful namespace-existence probes within
// one serial bootstrap attempt. Role and binding creates still go through normal
// API admission; a concurrent namespace deletion makes reconciliation fail and
// the next bootstrap attempt starts with a new, empty cache.
//
// It is only used by the reconcilers' best-effort namespace creation helper, not
// for reading RBAC policy or making authorization decisions.
type bootstrapNamespaceClient struct {
	corev1client.NamespaceInterface
	existing map[string]*corev1.Namespace
}

func (c *bootstrapNamespaceClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Namespace, error) {
	if options.ResourceVersion == "" {
		if namespace := c.existing[name]; namespace != nil {
			return namespace.DeepCopy(), nil
		}
	}
	namespace, err := c.NamespaceInterface.Get(ctx, name, options)
	if err == nil && namespace != nil && options.ResourceVersion == "" {
		c.existing[name] = namespace.DeepCopy()
	}
	return namespace, err
}

func (c *bootstrapNamespaceClient) Create(ctx context.Context, namespace *corev1.Namespace, options metav1.CreateOptions) (*corev1.Namespace, error) {
	created, err := c.NamespaceInterface.Create(ctx, namespace, options)
	if err == nil && created != nil {
		c.existing[created.Name] = created.DeepCopy()
	}
	return created, err
}
