/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

var vpaGVK = schema.GroupVersionKind{
	Group:   "autoscaling.k8s.io",
	Version: "v1",
	Kind:    "VerticalPodAutoscaler",
}

// reconcileVPA ensures a VerticalPodAutoscaler exists (or is removed) for the
// EnshroudedServer's StatefulSet.
//
// Uses the unstructured client so the operator does not require the
// autoscaling.k8s.io CRDs to be present at compile time. If the CRDs are
// absent the function logs a warning and returns nil (non-fatal).
func (r *EnshroudedServerReconciler) reconcileVPA(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)

	nn := types.NamespacedName{Name: server.Name, Namespace: server.Namespace}

	// If VPA integration is disabled, delete any existing VPA and return.
	if !server.Spec.VerticalScaling.Enabled {
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(vpaGVK)
		if err := r.Get(ctx, nn, existing); err == nil {
			log.Info("Deleting VerticalPodAutoscaler (verticalScaling disabled)", "vpa", server.Name)
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("deleting VerticalPodAutoscaler %s: %w", server.Name, err)
			}
		}
		return nil
	}

	// Check whether the VPA CRD is registered by probing a dummy Get.
	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(vpaGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: "__crd-probe__", Namespace: server.Namespace}, probe); err != nil {
		if errors.IsNotFound(err) {
			// CRD exists but the object simply doesn't exist — that's fine.
		} else if isNoCRDError(err) {
			log.Info("VPA CRD not installed — skipping VerticalPodAutoscaler reconciliation")
			return nil
		}
	}

	// Determine the updateMode to write into the VPA object.
	//   Off      → VPA mode "Off"   (observe only)
	//   WhenIdle → VPA mode "Off"   (operator drives recommendation application)
	//   InPlace  → VPA mode "InPlace" (VPA applies directly)
	vpaUpdateMode := "Off"
	if server.Spec.VerticalScaling.UpdateMode == enshroudedv1alpha1.VPAUpdateModeInPlace {
		vpaUpdateMode = "InPlace"
	}

	// Build the container resource policy.
	containerPolicy := map[string]any{
		"containerName": "enshrouded-server",
	}
	if len(server.Spec.VerticalScaling.MinAllowed) > 0 {
		containerPolicy["minAllowed"] = resourceListToMap(server.Spec.VerticalScaling.MinAllowed)
	}
	if len(server.Spec.VerticalScaling.MaxAllowed) > 0 {
		containerPolicy["maxAllowed"] = resourceListToMap(server.Spec.VerticalScaling.MaxAllowed)
	}

	desired := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "autoscaling.k8s.io/v1",
			"kind":       "VerticalPodAutoscaler",
			"metadata": map[string]any{
				"name":      server.Name,
				"namespace": server.Namespace,
				"labels":    labelsForServerMap(server.Name),
			},
			"spec": map[string]any{
				"targetRef": map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "StatefulSet",
					"name":       server.Name,
				},
				"updatePolicy": map[string]any{
					"updateMode": vpaUpdateMode,
				},
				"resourcePolicy": map[string]any{
					"containerPolicies": []any{containerPolicy},
				},
			},
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on VerticalPodAutoscaler %s: %w", server.Name, err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(vpaGVK)
	err := r.Get(ctx, nn, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("getting VerticalPodAutoscaler %s: %w", server.Name, err)
		}
		log.Info("Creating VerticalPodAutoscaler", "vpa", server.Name, "updateMode", vpaUpdateMode)
		if err := r.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating VerticalPodAutoscaler %s: %w", server.Name, err)
		}
		return nil
	}

	// Update if updateMode or resource policy changed.
	existingMode, _, _ := unstructured.NestedString(existing.Object, "spec", "updatePolicy", "updateMode")
	if existingMode != vpaUpdateMode {
		if err := unstructured.SetNestedField(existing.Object, vpaUpdateMode, "spec", "updatePolicy", "updateMode"); err != nil {
			return fmt.Errorf("setting updateMode on VerticalPodAutoscaler %s: %w", server.Name, err)
		}
		if err := unstructured.SetNestedField(existing.Object, []any{containerPolicy}, "spec", "resourcePolicy", "containerPolicies"); err != nil {
			return fmt.Errorf("setting containerPolicies on VerticalPodAutoscaler %s: %w", server.Name, err)
		}
		log.Info("Updating VerticalPodAutoscaler", "vpa", server.Name, "updateMode", vpaUpdateMode)
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating VerticalPodAutoscaler %s: %w", server.Name, err)
		}
	}
	return nil
}

// applyVPARecommendation reads the VPA's current recommendation and applies it
// to the StatefulSet's resource requests. Only active in WhenIdle mode.
//
// Rules:
//   - CPU: always apply when the recommendation differs.
//   - Memory scale-up: always apply.
//   - Memory scale-down: deferred while players are connected (prevents OOM-kill
//     during active game sessions on Proton/Wine-based servers).
func (r *EnshroudedServerReconciler) applyVPARecommendation(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	if !server.Spec.VerticalScaling.Enabled ||
		server.Spec.VerticalScaling.UpdateMode != enshroudedv1alpha1.VPAUpdateModeWhenIdle {
		return nil
	}

	log := logf.FromContext(ctx)
	nn := types.NamespacedName{Name: server.Name, Namespace: server.Namespace}

	// Fetch VPA object.
	vpa := &unstructured.Unstructured{}
	vpa.SetGroupVersionKind(vpaGVK)
	if err := r.Get(ctx, nn, vpa); err != nil {
		if errors.IsNotFound(err) || isNoCRDError(err) {
			return nil
		}
		return fmt.Errorf("getting VerticalPodAutoscaler %s for recommendation: %w", server.Name, err)
	}

	// Extract target recommendation for the game server container.
	recs, found, err := unstructured.NestedSlice(vpa.Object, "status", "recommendation", "containerRecommendations")
	if err != nil || !found || len(recs) == 0 {
		return nil // no recommendation available yet
	}
	var targetCPU, targetMemory string
	for _, rec := range recs {
		recMap, ok := rec.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(recMap, "containerName"); name != "enshrouded-server" {
			continue
		}
		targetCPU, _, _ = unstructured.NestedString(recMap, "target", "cpu")
		targetMemory, _, _ = unstructured.NestedString(recMap, "target", "memory")
		break
	}
	if targetCPU == "" && targetMemory == "" {
		return nil
	}

	// Fetch current StatefulSet.
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, nn, sts); err != nil {
		return fmt.Errorf("getting StatefulSet %s for VPA recommendation apply: %w", server.Name, err)
	}

	// Find the game server container.
	containerIdx := -1
	for i, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "enshrouded-server" {
			containerIdx = i
			break
		}
	}
	if containerIdx < 0 {
		return nil
	}

	current := sts.Spec.Template.Spec.Containers[containerIdx].Resources.Requests
	if current == nil {
		current = corev1.ResourceList{}
	}
	updated := current.DeepCopy()
	changed := false

	// CPU: always apply.
	if targetCPU != "" {
		newCPU := resource.MustParse(targetCPU)
		if curCPU, ok := current[corev1.ResourceCPU]; !ok || !curCPU.Equal(newCPU) {
			updated[corev1.ResourceCPU] = newCPU
			changed = true
			log.Info("VPA: applying CPU recommendation", "server", server.Name, "cpu", targetCPU)
		}
	}

	// Memory: apply scale-up always; defer scale-down while players are connected.
	if targetMemory != "" {
		newMem := resource.MustParse(targetMemory)
		curMem, hasCur := current[corev1.ResourceMemory]
		isScaleDown := hasCur && newMem.Cmp(curMem) < 0
		if isScaleDown && server.Status.ActivePlayers > 0 {
			log.Info("VPA: deferring memory scale-down — players connected",
				"server", server.Name,
				"current", curMem.String(),
				"recommended", targetMemory,
				"activePlayers", server.Status.ActivePlayers,
			)
		} else if !hasCur || !curMem.Equal(newMem) {
			updated[corev1.ResourceMemory] = newMem
			changed = true
			log.Info("VPA: applying memory recommendation", "server", server.Name, "memory", targetMemory)
		}
	}

	if !changed {
		return nil
	}

	patch := sts.DeepCopy()
	patch.Spec.Template.Spec.Containers[containerIdx].Resources.Requests = updated
	if err := r.Update(ctx, patch); err != nil {
		return fmt.Errorf("applying VPA recommendation to StatefulSet %s: %w", server.Name, err)
	}
	return nil
}

// isNoCRDError returns true when the error indicates the CRD is not registered
// in the cluster (NoKind / NoMatch from the REST mapper).
func isNoCRDError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "no kind is registered") ||
		contains(msg, "no matches for kind") ||
		contains(msg, "the server could not find the requested resource")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

// resourceListToMap converts a corev1.ResourceList to the map[string]interface{}
// format required by unstructured.Unstructured.
func resourceListToMap(rl corev1.ResourceList) map[string]any {
	m := make(map[string]any, len(rl))
	for k, v := range rl {
		m[string(k)] = v.String()
	}
	return m
}

// labelsForServerMap returns the same label set as labelsForServer but as
// map[string]interface{} for use in unstructured objects.
func labelsForServerMap(name string) map[string]any {
	labels := labelsForServer(name)
	m := make(map[string]any, len(labels))
	for k, v := range labels {
		m[k] = v
	}
	return m
}
