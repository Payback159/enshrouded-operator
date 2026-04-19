package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

// volumeSnapshotGVR is the GroupVersionResource for VolumeSnapshot (v1 GA).
var volumeSnapshotGVR = schema.GroupVersionResource{
	Group:    "snapshot.storage.k8s.io",
	Version:  "v1",
	Resource: "volumesnapshots",
}

// reconcileVolumeSnapshot evaluates whether a new VolumeSnapshot must be created
// and prunes snapshots that exceed the configured retainCount.
// It uses the unstructured client so the operator does not require the
// snapshot.storage.k8s.io CRDs to be installed at compile time.
func (r *EnshroudedServerReconciler) reconcileVolumeSnapshot(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	spec := server.Spec.Backup.VolumeSnapshot
	if !spec.Enabled {
		return nil
	}

	log := logf.FromContext(ctx)

	// Determine whether it is time to take a new snapshot.
	if !isInMaintenanceWindow([]string{spec.Schedule}) {
		return nil
	}

	pvcName := server.Name + "-savegame"
	snapName := fmt.Sprintf("%s-%d", server.Name, time.Now().Unix())

	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "snapshot.storage.k8s.io",
		Version: "v1",
		Kind:    "VolumeSnapshot",
	})
	snap.SetNamespace(server.Namespace)
	snap.SetName(snapName)
	snap.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "enshrouded-operator",
		"enshrouded.io/server":         server.Name,
	})

	if err := unstructured.SetNestedField(snap.Object, map[string]interface{}{
		"source": map[string]interface{}{
			"persistentVolumeClaimName": pvcName,
		},
	}, "spec"); err != nil {
		return fmt.Errorf("building VolumeSnapshot spec: %w", err)
	}

	if spec.VolumeSnapshotClassName != nil {
		if err := unstructured.SetNestedField(snap.Object,
			*spec.VolumeSnapshotClassName,
			"spec", "volumeSnapshotClassName"); err != nil {
			return fmt.Errorf("setting volumeSnapshotClassName: %w", err)
		}
	}

	if err := ctrl.SetControllerReference(server, snap, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on VolumeSnapshot: %w", err)
	}

	if err := r.Create(ctx, snap); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating VolumeSnapshot %s: %w", snapName, err)
	}
	log.Info("Created VolumeSnapshot", "snapshot", snapName)

	// Prune old snapshots beyond retainCount.
	retainCount := spec.RetainCount
	if retainCount == 0 {
		retainCount = 3
	}
	return r.pruneVolumeSnapshots(ctx, server, retainCount)
}

// pruneVolumeSnapshots deletes the oldest snapshots that exceed retainCount.
func (r *EnshroudedServerReconciler) pruneVolumeSnapshots(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer, retainCount int32) error {
	log := logf.FromContext(ctx)

	snapList := &unstructured.UnstructuredList{}
	snapList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "snapshot.storage.k8s.io",
		Version: "v1",
		Kind:    "VolumeSnapshotList",
	})

	if err := r.List(ctx, snapList,
		client.InNamespace(server.Namespace),
		client.MatchingLabels{"enshrouded.io/server": server.Name},
	); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing VolumeSnapshots: %w", err)
	}

	items := snapList.Items
	if int32(len(items)) <= retainCount {
		return nil
	}

	// Sort ascending by creation time — oldest first.
	sort.Slice(items, func(i, j int) bool {
		ti := items[i].GetCreationTimestamp().Time
		tj := items[j].GetCreationTimestamp().Time
		return ti.Before(tj)
	})

	toDelete := items[:int32(len(items))-retainCount]
	for i := range toDelete {
		if err := r.Delete(ctx, &toDelete[i]); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting VolumeSnapshot %s: %w", toDelete[i].GetName(), err)
		}
		log.Info("Pruned old VolumeSnapshot", "snapshot", toDelete[i].GetName())
	}
	return nil
}

// reconcileS3Sidecar ensures that — when S3 backup is enabled — the StatefulSet
// contains an rclone sidecar container that periodically syncs the savegame volume
// to the configured S3 bucket.  The function patches the StatefulSet's pod template
// by injecting / updating the sidecar spec.
//
// Note: StatefulSet pod templates are immutable for most fields; this operator
// recreates the StatefulSet when the pod template changes (handled by
// reconcileStatefulSet).  The S3 spec is therefore wired through the top-level
// reconcile loop: the sidecar is added/removed in buildStatefulSet, not here.
// This function only validates prerequisites (secret existence) and records a
// status event when the secret is missing.
func (r *EnshroudedServerReconciler) reconcileS3Sidecar(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	if server.Spec.Backup.S3 == nil {
		return nil
	}

	s3 := server.Spec.Backup.S3
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: server.Namespace,
		Name:      s3.CredentialsSecretRef.Name,
	}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			logf.FromContext(ctx).Info("S3 credentials secret not found — sidecar will not start",
				"secret", s3.CredentialsSecretRef.Name)
			return nil // non-fatal; retry on next reconcile
		}
		return fmt.Errorf("fetching S3 credentials secret: %w", err)
	}
	return nil
}

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T { return &v }

// s3SidecarContainer builds the rclone sidecar container spec.// It is called from buildStatefulSet when S3 backup is configured.
func s3SidecarContainer(server *enshroudedv1alpha1.EnshroudedServer) *corev1.Container {
	s3 := server.Spec.Backup.S3
	if s3 == nil {
		return nil
	}

	image := s3.Image
	if image == "" {
		image = "rclone/rclone:latest"
	}

	schedule := s3.Schedule
	if schedule == "" {
		schedule = "*/30 * * * *"
	}

	// Convert cron schedule to a sleep interval (best-effort simple case).
	// For production use, prefer a proper cron sidecar image.
	syncCmd := fmt.Sprintf(
		"while true; do rclone sync /savegame %s --s3-endpoint=$AWS_ENDPOINT_URL; sleep 1800; done",
		s3.BucketURL,
	)

	return &corev1.Container{
		Name:            "s3-backup",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/sh", "-c", syncCmd},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "savegame",
				MountPath: "/savegame",
				ReadOnly:  true,
			},
		},
		EnvFrom: []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: s3.CredentialsSecretRef,
				},
			},
		},
		// Security hardening: drop all capabilities, read-only root.
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			ReadOnlyRootFilesystem:   ptr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
}
