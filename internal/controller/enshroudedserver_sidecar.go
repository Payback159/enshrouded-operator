package controller

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

// sidecarServiceAccountName returns the name of the metrics sidecar's ServiceAccount.
func sidecarServiceAccountName(serverName string) string {
	return serverName + "-metrics"
}

// reconcileMetricsSidecarRBAC ensures the ServiceAccount, Role, and RoleBinding
// exist that allow the metrics sidecar container to patch the EnshroudedServer
// CR status with the live player count.
//
// All three objects are owned by the EnshroudedServer resource so they are
// cleaned up automatically when the CR is deleted.
func (r *EnshroudedServerReconciler) reconcileMetricsSidecarRBAC(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	if !server.Spec.MetricsSidecar.Enabled {
		return nil
	}

	if err := r.reconcileSidecarServiceAccount(ctx, server); err != nil {
		return err
	}
	if err := r.reconcileSidecarRole(ctx, server); err != nil {
		return err
	}
	return r.reconcileSidecarRoleBinding(ctx, server)
}

// reconcileSidecarServiceAccount creates or verifies the metrics sidecar ServiceAccount.
func (r *EnshroudedServerReconciler) reconcileSidecarServiceAccount(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)
	name := sidecarServiceAccountName(server.Name)

	desired := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on ServiceAccount %s: %w", name, err)
	}

	existing := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("getting ServiceAccount %s: %w", name, err)
		}
		log.Info("Creating metrics sidecar ServiceAccount", "serviceaccount", name)
		if err := r.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ServiceAccount %s: %w", name, err)
		}
	}
	return nil
}

// reconcileSidecarRole creates or updates the Role granting the sidecar permission
// to patch the EnshroudedServer status subresource.
func (r *EnshroudedServerReconciler) reconcileSidecarRole(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)
	name := sidecarServiceAccountName(server.Name)

	desired := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{enshroudedv1alpha1.GroupVersion.Group},
				Resources: []string{"enshroudedservers"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{enshroudedv1alpha1.GroupVersion.Group},
				Resources: []string{"enshroudedservers/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on Role %s: %w", name, err)
	}

	existing := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("getting Role %s: %w", name, err)
		}
		log.Info("Creating metrics sidecar Role", "role", name)
		if err := r.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Role %s: %w", name, err)
		}
		return nil
	}

	// Update rules if they changed.
	existing.Rules = desired.Rules
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating Role %s: %w", name, err)
	}
	return nil
}

// reconcileSidecarRoleBinding binds the sidecar ServiceAccount to its Role.
func (r *EnshroudedServerReconciler) reconcileSidecarRoleBinding(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)
	name := sidecarServiceAccountName(server.Name)

	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      name,
				Namespace: server.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     name,
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on RoleBinding %s: %w", name, err)
	}

	existing := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("getting RoleBinding %s: %w", name, err)
		}
		log.Info("Creating metrics sidecar RoleBinding", "rolebinding", name)
		if err := r.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating RoleBinding %s: %w", name, err)
		}
	}
	return nil
}

// metricsSidecarContainer builds the metrics sidecar container spec.
// Returns nil when the sidecar is disabled or the spec is the zero value.
func metricsSidecarContainer(server *enshroudedv1alpha1.EnshroudedServer, queryPort int32) *corev1.Container {
	spec := server.Spec.MetricsSidecar
	if !spec.Enabled {
		return nil
	}

	image := spec.Image
	if image == "" {
		image = "ghcr.io/payback159/enshrouded-metrics-sidecar:latest"
	}
	metricsPort := spec.MetricsPort
	if metricsPort == 0 {
		metricsPort = 9090
	}
	scrapeInterval := spec.ScrapeIntervalSeconds
	if scrapeInterval == 0 {
		scrapeInterval = 15
	}

	return &corev1.Container{
		Name:            "metrics-sidecar",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{Name: "metrics", ContainerPort: metricsPort, Protocol: corev1.ProtocolTCP},
		},
		Env: []corev1.EnvVar{
			{Name: "QUERY_HOST", Value: "127.0.0.1"},
			{Name: "QUERY_PORT", Value: strconv.Itoa(int(queryPort))},
			{Name: "METRICS_ADDR", Value: fmt.Sprintf(":%d", metricsPort)},
			{Name: "SCRAPE_INTERVAL", Value: fmt.Sprintf("%ds", scrapeInterval)},
			{
				Name: "SERVER_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
			{Name: "SERVER_NAME", Value: server.Name},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(metricsPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			ReadOnlyRootFilesystem:   ptr(true),
			RunAsNonRoot:             ptr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}
