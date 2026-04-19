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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
	ssmmetrics "github.com/payback159/enshrouded-operator/internal/metrics"
)

// EnshroudedServerReconciler reconciles a EnshroudedServer object
type EnshroudedServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	finalizerName   = "enshrouded.enshrouded.io/finalizer"
	configSecretKey = "enshrouded_server.json"
	configMountPath = "/config"
)

// enshroudedConfigJSON mirrors the Enshrouded server JSON config format.
type enshroudedConfigJSON struct {
	Name          string          `json:"name"`
	SaveDirectory string          `json:"saveDirectory"`
	LogDirectory  string          `json:"logDirectory"`
	IP            string          `json:"ip"`
	QueryPort     int32           `json:"queryPort"`
	SlotCount     int32           `json:"slotCount"`
	UserGroups    []userGroupJSON `json:"userGroups"`
}

type userGroupJSON struct {
	Name                 string `json:"name"`
	Password             string `json:"password"`
	CanKickBan           bool   `json:"canKickBan"`
	CanAccessInventories bool   `json:"canAccessInventories"`
	CanEditWorld         bool   `json:"canEditWorld"`
	CanEditBase          bool   `json:"canEditBase"`
	CanExtendBase        bool   `json:"canExtendBase"`
	ReservedSlots        int32  `json:"reservedSlots"`
}

// +kubebuilder:rbac:groups=enshrouded.enshrouded.io,resources=enshroudedservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=enshrouded.enshrouded.io,resources=enshroudedservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=enshrouded.enshrouded.io,resources=enshroudedservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.k8s.io,resources=verticalpodautoscalers,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the current cluster state closer to the desired state defined
// by the EnshroudedServer resource.
func (r *EnshroudedServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	server := &enshroudedv1alpha1.EnshroudedServer{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion via finalizer.
	if !server.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, server)
	}

	// Ensure our finalizer is registered.
	if !controllerutil.ContainsFinalizer(server, finalizerName) {
		controllerutil.AddFinalizer(server, finalizerName)
		if err := r.Update(ctx, server); err != nil {
			return ctrl.Result{}, err
		}
		// Continue reconciliation — the Update triggers a re-queue anyway.
	}

	// Compute the annotation hash used for rolling-restart detection.
	// In UserGroups mode we hash the entire generated config JSON (includes all passwords).
	// In simple mode we hash the server password secret value.
	var annotationHash string
	if len(server.Spec.UserGroups) > 0 {
		hash, err := r.reconcileConfigSecret(ctx, server)
		if err != nil {
			return ctrl.Result{}, err
		}
		annotationHash = hash
	} else {
		// Clean up config Secret when UserGroups is removed.
		if err := r.cleanupConfigSecret(ctx, server); err != nil {
			return ctrl.Result{}, err
		}
		// Validate ServerPasswordSecretRef and compute a hash for change detection.
		if server.Spec.ServerPasswordSecretRef != nil {
			secret := &corev1.Secret{}
			secretKey := types.NamespacedName{
				Namespace: server.Namespace,
				Name:      server.Spec.ServerPasswordSecretRef.Name,
			}
			if err := r.Get(ctx, secretKey, secret); err != nil {
				if apierrors.IsNotFound(err) {
					msg := fmt.Sprintf("referenced Secret %q not found", secretKey.Name)
					log.Error(err, msg)
					return r.setPhaseError(ctx, server, msg)
				}
				return ctrl.Result{}, err
			}
			if _, ok := secret.Data[server.Spec.ServerPasswordSecretRef.Key]; !ok {
				msg := fmt.Sprintf("key %q not found in Secret %q", server.Spec.ServerPasswordSecretRef.Key, secretKey.Name)
				log.Error(nil, msg)
				return r.setPhaseError(ctx, server, msg)
			}
			h := sha256.Sum256(secret.Data[server.Spec.ServerPasswordSecretRef.Key])
			annotationHash = hex.EncodeToString(h[:])
		}
	}

	if err := r.reconcilePVC(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileMetricsSidecarRBAC(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcilePDB(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileVPA(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.applyVPARecommendation(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileNetworkPolicy(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	// Deferred-update check: if deferWhilePlaying is enabled and the server currently
	// has active players, skip the StatefulSet update (but still reconcile PVC/Service).
	// We re-queue after 1 minute to check again.
	updateDeferred := false
	if server.Spec.UpdatePolicy.DeferWhilePlaying && server.Status.ActivePlayers > 0 {
		if !isInMaintenanceWindow(server.Spec.UpdatePolicy.MaintenanceWindows) {
			log.Info("Deferring StatefulSet update — players connected",
				"activePlayers", server.Status.ActivePlayers)
			updateDeferred = true
		}
	}

	if !updateDeferred {
		if err := r.reconcileStatefulSet(ctx, server, annotationHash); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileService(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileS3Sidecar(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileVolumeSnapshot(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	maintenanceWindowActive := isInMaintenanceWindow(server.Spec.UpdatePolicy.MaintenanceWindows)
	if err := r.updateStatus(ctx, server, updateDeferred, maintenanceWindowActive); err != nil {
		return ctrl.Result{}, err
	}

	if updateDeferred {
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// handleDeletion runs cleanup logic and removes the finalizer so the CR can be garbage-collected.
func (r *EnshroudedServerReconciler) handleDeletion(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(server, finalizerName) {
		return ctrl.Result{}, nil
	}

	retain := server.Spec.Storage.RetainOnDelete == nil || *server.Spec.Storage.RetainOnDelete
	if !retain {
		pvcName := fmt.Sprintf("%s-savegame", server.Name)
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: server.Namespace}, pvc)
		if err == nil {
			log.Info("Deleting PVC on CR deletion (retainOnDelete=false)", "pvc", pvcName)
			if delErr := r.Delete(ctx, pvc); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, delErr
			}
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else {
		log.Info("Retaining PVC on CR deletion (retainOnDelete=true)", "server", server.Name)
	}

	controllerutil.RemoveFinalizer(server, finalizerName)
	if err := r.Update(ctx, server); err != nil {
		return ctrl.Result{}, err
	}
	// Clean up Prometheus metrics for this server.
	ssmmetrics.DeleteServerMetrics(server.Namespace, server.Name, string(server.Status.Phase))
	return ctrl.Result{}, nil
}

// reconcilePVC ensures the save-data PVC exists for the given server.
// The PVC intentionally has no OwnerReference so that save data is retained
// when the EnshroudedServer CR is deleted (controlled by retainOnDelete).
func (r *EnshroudedServerReconciler) reconcilePVC(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)

	storageSize := server.Spec.Storage.Size
	if storageSize.IsZero() {
		storageSize = resource.MustParse("10Gi")
	}

	pvcName := fmt.Sprintf("%s-savegame", server.Name)
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: server.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		desired := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: server.Namespace,
				Labels:    labelsForServer(server.Name),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				StorageClassName: server.Spec.Storage.StorageClassName,
			},
		}
		log.Info("Creating PVC", "pvc", pvcName)
		return r.Create(ctx, desired)
	}

	return nil
}

// buildServerConfigJSON assembles the Enshrouded server JSON config from the CR spec,
// resolving each UserGroup's password from its referenced Secret.
// Returns the raw JSON bytes and a SHA-256 hex hash of that content.
func (r *EnshroudedServerReconciler) buildServerConfigJSON(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) ([]byte, string, error) {
	serverName := server.Spec.ServerName
	if serverName == "" {
		serverName = "Enshrouded Server"
	}
	port := server.Spec.Port
	if port == 0 {
		port = 15637
	}
	serverSlots := server.Spec.ServerSlots
	if serverSlots == 0 {
		serverSlots = 16
	}
	serverIP := server.Spec.ServerIP
	if serverIP == "" {
		serverIP = "0.0.0.0"
	}

	groups := make([]userGroupJSON, 0, len(server.Spec.UserGroups))
	for _, g := range server.Spec.UserGroups {
		password := ""
		if g.PasswordSecretRef != nil {
			secret := &corev1.Secret{}
			key := types.NamespacedName{Namespace: server.Namespace, Name: g.PasswordSecretRef.Name}
			if err := r.Get(ctx, key, secret); err != nil {
				return nil, "", fmt.Errorf("UserGroup %q: Secret %q: %w", g.Name, g.PasswordSecretRef.Name, err)
			}
			password = string(secret.Data[g.PasswordSecretRef.Key])
		}
		groups = append(groups, userGroupJSON{
			Name:                 g.Name,
			Password:             password,
			CanKickBan:           g.CanKickBan,
			CanAccessInventories: g.CanAccessInventories,
			CanEditWorld:         g.CanEditWorld,
			CanEditBase:          g.CanEditBase,
			CanExtendBase:        g.CanExtendBase,
			ReservedSlots:        g.ReservedSlots,
		})
	}

	cfg := enshroudedConfigJSON{
		Name:          serverName,
		SaveDirectory: "./savegame",
		LogDirectory:  "./logs",
		IP:            serverIP,
		QueryPort:     port,
		SlotCount:     serverSlots,
		UserGroups:    groups,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:]), nil
}

// reconcileConfigSecret creates or updates the Secret that holds the generated server JSON config.
// Returns the SHA-256 hash of the config content (used as pod annotation for rolling restarts).
func (r *EnshroudedServerReconciler) reconcileConfigSecret(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) (string, error) {
	log := logf.FromContext(ctx)

	data, configHash, err := r.buildServerConfigJSON(ctx, server)
	if err != nil {
		return "", err
	}

	secretName := server.Name + "-config"
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Data: map[string][]byte{
			configSecretKey: data,
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return "", err
	}

	existing := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: server.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if apierrors.IsNotFound(err) {
		log.Info("Creating config Secret", "secret", secretName)
		return configHash, r.Create(ctx, desired)
	}
	existing.Data = desired.Data
	log.Info("Updating config Secret", "secret", secretName)
	return configHash, r.Update(ctx, existing)
}

// cleanupConfigSecret deletes the config Secret when UserGroups is no longer configured.
func (r *EnshroudedServerReconciler) cleanupConfigSecret(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	secretName := server.Name + "-config"
	existing := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: server.Namespace}, existing); err != nil {
		return client.IgnoreNotFound(err)
	}
	logf.FromContext(ctx).Info("Removing config Secret (UserGroups cleared)", "secret", secretName)
	return client.IgnoreNotFound(r.Delete(ctx, existing))
}

// reconcileStatefulSet ensures the StatefulSet for the server is up-to-date.
func (r *EnshroudedServerReconciler) reconcileStatefulSet(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer, secretHash string) error {
	log := logf.FromContext(ctx)

	desired := r.buildStatefulSet(server, secretHash)
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		log.Info("Creating StatefulSet", "statefulset", server.Name)
		return r.Create(ctx, desired)
	}

	// Apply the UpgradeStrategy: optionally create a VolumeSnapshot before updating.
	strategy := server.Spec.UpdatePolicy.UpgradeStrategy
	if strategy == "" {
		strategy = enshroudedv1alpha1.UpgradeStrategyNoSnapshot
	}
	if strategy != enshroudedv1alpha1.UpgradeStrategyNoSnapshot && len(existing.Spec.Template.Spec.Containers) > 0 {
		currentImage := existing.Spec.Template.Spec.Containers[0].Image
		if currentImage != desired.Spec.Template.Spec.Containers[0].Image {
			strict := strategy == enshroudedv1alpha1.UpgradeStrategyStrictSnapshotBeforeUpdate
			if snapErr := r.snapshotBeforeUpdate(ctx, server, strict); snapErr != nil {
				// strict=true: error already contains context, block the update.
				return fmt.Errorf("strict pre-update snapshot failed, blocking update: %w", snapErr)
			}
		}
	}

	// Only update mutable fields to avoid conflicts with immutable StatefulSet fields.
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	log.Info("Updating StatefulSet", "statefulset", server.Name)
	return r.Update(ctx, existing)
}

// reconcileService ensures the UDP Service for the server is up-to-date.
func (r *EnshroudedServerReconciler) reconcileService(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)

	port := server.Spec.Port
	if port == 0 {
		port = 15637
	}
	steamPort := server.Spec.SteamPort
	if steamPort == 0 {
		steamPort = 27015
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      server.Name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: labelsForServer(server.Name),
			Ports: []corev1.ServicePort{
				{
					Name:     "query",
					Port:     port,
					Protocol: corev1.ProtocolUDP,
				},
				{
					Name:     "steam",
					Port:     steamPort,
					Protocol: corev1.ProtocolUDP,
				},
			},
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		log.Info("Creating Service", "service", server.Name)
		return r.Create(ctx, desired)
	}

	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Type = desired.Spec.Type
	log.Info("Updating Service", "service", server.Name)
	return r.Update(ctx, existing)
}

// buildStatefulSet constructs the desired StatefulSet for the given server.
// annotationHash is a SHA-256 hash that triggers a rolling restart when its value changes
// (it is either the server password hash or the full config JSON hash for UserGroups mode).
func (r *EnshroudedServerReconciler) buildStatefulSet(server *enshroudedv1alpha1.EnshroudedServer, annotationHash string) *appsv1.StatefulSet {
	serverName := server.Spec.ServerName
	if serverName == "" {
		serverName = "Enshrouded Server"
	}
	port := server.Spec.Port
	if port == 0 {
		port = 15637
	}
	steamPort := server.Spec.SteamPort
	if steamPort == 0 {
		steamPort = 27015
	}
	serverSlots := server.Spec.ServerSlots
	if serverSlots == 0 {
		serverSlots = 16
	}
	serverIP := server.Spec.ServerIP
	if serverIP == "" {
		serverIP = "0.0.0.0"
	}
	imageRepo := server.Spec.Image.Repository
	if imageRepo == "" {
		imageRepo = "ghcr.io/payback159/enshrouded-server"
	}
	imageTag := server.Spec.Image.Tag
	if imageTag == "" {
		imageTag = "latest"
	}
	pullPolicy := server.Spec.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	image := fmt.Sprintf("%s:%s", imageRepo, imageTag)
	pvcName := fmt.Sprintf("%s-savegame", server.Name)

	uid := int64(10000)
	gid := int64(10000)

	podAnnotations := map[string]string{}
	if annotationHash != "" {
		podAnnotations["enshrouded.enshrouded.io/password-secret-hash"] = annotationHash
	}

	volumes := []corev1.Volume{
		{
			Name: "savegame",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{Name: "savegame", MountPath: "/home/steam/enshrouded/savegame"},
	}

	var env []corev1.EnvVar
	if len(server.Spec.UserGroups) > 0 {
		// EXTERNAL_CONFIG mode: all config comes from the mounted JSON file.
		// The FSGroup=10000 on the pod security context makes the file owned
		// by 0:10000, which satisfies the entrypoint's ownership check.
		configSecretName := server.Name + "-config"
		configFilePath := configMountPath + "/" + configSecretKey
		env = []corev1.EnvVar{
			{Name: "EXTERNAL_CONFIG", Value: "1"},
			{Name: "ENSHROUDED_CONFIG", Value: configFilePath},
		}
		volumes = append(volumes, corev1.Volume{
			Name: "server-config",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: configSecretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "server-config",
			MountPath: configMountPath,
			ReadOnly:  true,
		})
	} else {
		// Simple ENV-var mode.
		env = []corev1.EnvVar{
			{Name: "SERVER_NAME", Value: serverName},
			{Name: "PORT", Value: fmt.Sprintf("%d", port)},
			{Name: "SERVER_SLOTS", Value: fmt.Sprintf("%d", serverSlots)},
			{Name: "SERVER_IP", Value: serverIP},
		}
		if server.Spec.ServerPasswordSecretRef != nil {
			env = append(env, corev1.EnvVar{
				Name: "SERVER_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: server.Spec.ServerPasswordSecretRef,
				},
			})
		}
	}

	replicas := int32(1)

	// When the metrics sidecar is enabled, inject a readiness probe on the game
	// server container that defers "Ready" until the sidecar's /readyz endpoint
	// returns HTTP 200 (i.e. the server responds to A2S queries).
	// This prevents the Service from sending game traffic to a pod that is still
	// booting or downloading world data.
	var gameServerReadinessProbe *corev1.Probe
	if server.Spec.MetricsSidecar.Enabled {
		metricsPort := server.Spec.MetricsSidecar.MetricsPort
		if metricsPort == 0 {
			metricsPort = 9090
		}
		gameServerReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz",
					Port: intstr.FromInt32(metricsPort),
				},
			},
			InitialDelaySeconds: 60,
			PeriodSeconds:       15,
			FailureThreshold:    3,
			SuccessThreshold:    1,
		}
	}

	// Optionally inject the metrics sidecar.
	containers := []corev1.Container{
		{
			Name:            "enshrouded-server",
			Image:           image,
			ImagePullPolicy: pullPolicy,
			Env:             env,
			Ports: []corev1.ContainerPort{
				{Name: "query", ContainerPort: port, Protocol: corev1.ProtocolUDP},
				{Name: "steam", ContainerPort: steamPort, Protocol: corev1.ProtocolUDP},
			},
			Resources:      server.Spec.Resources,
			VolumeMounts:   volumeMounts,
			ReadinessProbe: gameServerReadinessProbe,
		},
	}
	if c := metricsSidecarContainer(server, port); c != nil {
		containers = append(containers, *c)
	}

	// Use the sidecar's dedicated ServiceAccount when the sidecar is enabled,
	// otherwise fall back to the namespace default.
	serviceAccountName := ""
	if server.Spec.MetricsSidecar.Enabled {
		serviceAccountName = sidecarServiceAccountName(server.Name)
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      server.Name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: server.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForServer(server.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labelsForServer(server.Name),
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &uid,
						RunAsGroup: &gid,
						FSGroup:    &gid,
					},
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}
}

// updateStatus reconciles the status of the EnshroudedServer based on the StatefulSet state.
// updateDeferred indicates whether a pending update is currently being held back.
// maintenanceWindowActive indicates whether the current time is inside a configured maintenance window.
func (r *EnshroudedServerReconciler) updateStatus(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer, updateDeferred bool, maintenanceWindowActive bool) error {
	oldPhase := string(server.Status.Phase)

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, sts)

	var readyStatus metav1.ConditionStatus
	var readyReason, readyMessage string

	if err != nil {
		if apierrors.IsNotFound(err) {
			server.Status.Phase = enshroudedv1alpha1.EnshroudedServerPhasePending
			server.Status.ReadyReplicas = 0
			readyStatus = metav1.ConditionFalse
			readyReason = "StatefulSetNotFound"
			readyMessage = "StatefulSet has not been created yet"
		} else {
			return err
		}
	} else {
		server.Status.ReadyReplicas = sts.Status.ReadyReplicas
		switch {
		case sts.Status.ReadyReplicas > 0:
			server.Status.Phase = enshroudedv1alpha1.EnshroudedServerPhaseRunning
			readyStatus = metav1.ConditionTrue
			readyReason = "ServerRunning"
			readyMessage = "Server is running and accepting connections"
		case sts.Status.CurrentRevision != "" && sts.Status.CurrentRevision != sts.Status.UpdateRevision:
			server.Status.Phase = enshroudedv1alpha1.EnshroudedServerPhaseUpdating
			readyStatus = metav1.ConditionFalse
			readyReason = "ServerUpdating"
			readyMessage = "Server is being updated"
		default:
			server.Status.Phase = enshroudedv1alpha1.EnshroudedServerPhasePending
			readyStatus = metav1.ConditionFalse
			readyReason = "ServerStarting"
			readyMessage = "Server is starting up"
		}
	}

	server.Status.UpdateDeferred = updateDeferred

	apimeta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
		Type:               enshroudedv1alpha1.ConditionReady,
		Status:             readyStatus,
		Reason:             readyReason,
		Message:            readyMessage,
		ObservedGeneration: server.Generation,
	})

	if updateDeferred {
		apimeta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
			Type:               enshroudedv1alpha1.ConditionUpdateDeferred,
			Status:             metav1.ConditionTrue,
			Reason:             "PlayersConnected",
			Message:            fmt.Sprintf("Update deferred: %d player(s) connected", server.Status.ActivePlayers),
			ObservedGeneration: server.Generation,
		})
	} else {
		apimeta.RemoveStatusCondition(&server.Status.Conditions, enshroudedv1alpha1.ConditionUpdateDeferred)
	}

	if err := r.Status().Update(ctx, server); err != nil {
		return err
	}

	// Publish operator-level Prometheus metrics (application metrics come from the sidecar).
	ssmmetrics.SetServerMetrics(
		server.Namespace, server.Name,
		server.Status.ReadyReplicas,
		server.Status.UpdateDeferred, string(server.Status.Phase), oldPhase,
		maintenanceWindowActive,
	)
	return nil
}

// setPhaseError sets the server phase to Error and returns an error to trigger requeue.
func (r *EnshroudedServerReconciler) setPhaseError(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer, msg string) (ctrl.Result, error) {
	server.Status.Phase = enshroudedv1alpha1.EnshroudedServerPhaseError
	_ = r.Status().Update(ctx, server)
	return ctrl.Result{}, fmt.Errorf("%s", msg)
}

// reconcileNetworkPolicy ensures a NetworkPolicy exists that:
//   - allows inbound UDP on the game query port and Steam port from anywhere
//   - denies all other inbound traffic
//   - denies all egress to the Kubernetes API server CIDR (169.254.0.0/16 + service CIDR)
//     so game-server pods cannot reach the K8s API.
func (r *EnshroudedServerReconciler) reconcileNetworkPolicy(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)

	port := server.Spec.Port
	if port == 0 {
		port = 15637
	}
	steamPort := server.Spec.SteamPort
	if steamPort == 0 {
		steamPort = 27015
	}

	queryPortInt := intstr.FromInt(int(port))
	steamPortInt := intstr.FromInt(int(steamPort))
	udpProto := corev1.ProtocolUDP

	// Block egress to the Kubernetes API server. The well-known link-local address
	// 169.254.169.254 (cloud metadata) and the in-cluster service IP (first IP of
	// the service CIDR, typically 10.96.0.1) are blocked. We use a deny-all-then-
	// allow approach: only DNS (UDP/TCP 53) and the game ports are allowed outbound.
	dnsPort := intstr.FromInt(53)
	tcpProto := corev1.ProtocolTCP
	httpsPort := intstr.FromInt(443)

	desired := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      server.Name + "-netpol",
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: labelsForServer(server.Name),
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			// Ingress: allow the two game UDP ports from anywhere.
			// When the metrics sidecar is enabled, also allow TCP on the metrics port.
			Ingress: func() []networkingv1.NetworkPolicyIngressRule {
				rules := []networkingv1.NetworkPolicyIngressRule{
					{
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &udpProto, Port: &queryPortInt},
							{Protocol: &udpProto, Port: &steamPortInt},
						},
					},
				}
				if server.Spec.MetricsSidecar.Enabled {
					mp := server.Spec.MetricsSidecar.MetricsPort
					if mp == 0 {
						mp = 9090
					}
					metricsPortInt := intstr.FromInt(int(mp))
					rules = append(rules, networkingv1.NetworkPolicyIngressRule{
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &tcpProto, Port: &metricsPortInt},
						},
					})
				}
				return rules
			}(),
			// Egress: allow DNS + SteamCMD HTTPS outbound; block everything else
			// (in particular the K8s API on port 443 of the cluster service IP).
			// For the game server to reach Steam servers we allow all HTTPS, but
			// deny egress to the link-local range (cloud metadata + K8s API).
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Allow DNS over UDP and TCP so SteamCMD can resolve hostnames.
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProto, Port: &dnsPort},
						{Protocol: &tcpProto, Port: &dnsPort},
					},
				},
				// Allow HTTPS egress to the public internet (Steam), but NOT to
				// the link-local range. We express this as "allow HTTPS to
				// everything except 169.254.0.0/16".
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProto, Port: &httpsPort},
					},
					To: []networkingv1.NetworkPolicyPeer{
						{
							// Allow to all IPs except the link-local range
							// (cloud metadata service and K8s API on some providers).
							IPBlock: &networkingv1.IPBlock{
								CIDR:   "0.0.0.0/0",
								Except: []string{"169.254.0.0/16"},
							},
						},
					},
				},
				// Allow outbound game UDP (Steam peer-to-peer) on the game ports.
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProto, Port: &queryPortInt},
						{Protocol: &udpProto, Port: &steamPortInt},
					},
				},
			},
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return err
	}

	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: server.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		log.Info("Creating NetworkPolicy", "networkpolicy", desired.Name)
		return r.Create(ctx, desired)
	}
	existing.Spec = desired.Spec
	log.Info("Updating NetworkPolicy", "networkpolicy", desired.Name)
	return r.Update(ctx, existing)
}

// isInMaintenanceWindow returns true if the current time falls inside any of the
// provided cron-style maintenance window expressions. Each entry may optionally
// start with "TZ=<tz> " followed by a standard 5-field cron spec (min hour dom mon dow).
// A nil or empty list always returns false.
func isInMaintenanceWindow(windows []string) bool {
	if len(windows) == 0 {
		return false
	}
	now := time.Now()
	for _, w := range windows {
		if inWindow(now, w) {
			return true
		}
	}
	return false
}

// inWindow checks whether t falls within the 1-hour slot described by a cron expression.
// Only the hour and weekday fields are evaluated (minute is ignored for simplicity).
// Format: optional "TZ=<tz> " prefix + "<min> <hour> * * <weekday>".
func inWindow(t time.Time, expr string) bool {
	// Strip optional TZ prefix.
	loc := time.UTC
	rest := expr
	if len(expr) > 3 && expr[:3] == "TZ=" {
		end := 3
		for end < len(expr) && expr[end] != ' ' {
			end++
		}
		tzName := expr[3:end]
		if l, err := time.LoadLocation(tzName); err == nil {
			loc = l
		}
		if end < len(expr) {
			rest = expr[end+1:]
		}
	}
	t = t.In(loc)

	// Parse the 5-field cron spec: min hour dom mon dow
	fields := splitFields(rest)
	if len(fields) != 5 {
		return false
	}
	// We only check hour (field[1]) and weekday (field[4]).
	if !matchCronField(fields[1], t.Hour()) {
		return false
	}
	if !matchCronField(fields[4], int(t.Weekday())) {
		return false
	}
	return true
}

func splitFields(s string) []string {
	var fields []string
	cur := ""
	for _, c := range s {
		if c == ' ' || c == '\t' {
			if cur != "" {
				fields = append(fields, cur)
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		fields = append(fields, cur)
	}
	return fields
}

// matchCronField returns true if value matches the cron field expression
// (supports *, single values, ranges x-y, and lists a,b,c).
func matchCronField(field string, value int) bool {
	if field == "*" {
		return true
	}
	// List
	for _, part := range splitByComma(field) {
		if matchRange(part, value) {
			return true
		}
	}
	return false
}

func splitByComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	return append(out, cur)
}

func matchRange(part string, value int) bool {
	// Range x-y
	for i, c := range part {
		if c == '-' {
			lo := atoi(part[:i])
			hi := atoi(part[i+1:])
			return value >= lo && value <= hi
		}
	}
	return atoi(part) == value
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// labelsForServer returns the standard labels for resources managed by this operator.
func labelsForServer(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "enshrouded-server",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "enshrouded-operator",
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *EnshroudedServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index EnshroudedServers by all referenced Secret names (server password + UserGroup
	// passwords) so that any Secret change can be mapped back to the correct EnshroudedServer.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&enshroudedv1alpha1.EnshroudedServer{},
		".spec.allSecretRefs",
		func(rawObj client.Object) []string {
			server := rawObj.(*enshroudedv1alpha1.EnshroudedServer)
			var names []string
			if server.Spec.ServerPasswordSecretRef != nil {
				names = append(names, server.Spec.ServerPasswordSecretRef.Name)
			}
			for _, g := range server.Spec.UserGroups {
				if g.PasswordSecretRef != nil {
					names = append(names, g.PasswordSecretRef.Name)
				}
			}
			return names
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&enshroudedv1alpha1.EnshroudedServer{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		// Watch Secrets so that password changes trigger a reconcile and rolling restart.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findServersForSecret)).
		Named("enshroudedserver").
		Complete(r)
}

// findServersForSecret maps a Secret change event to the EnshroudedServer(s) that reference it
// (either via serverPasswordSecretRef or a UserGroup passwordSecretRef).
func (r *EnshroudedServerReconciler) findServersForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	serverList := &enshroudedv1alpha1.EnshroudedServerList{}
	if err := r.List(ctx, serverList,
		client.InNamespace(secret.GetNamespace()),
		client.MatchingFields{".spec.allSecretRefs": secret.GetName()},
	); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, len(serverList.Items))
	for i, server := range serverList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      server.Name,
				Namespace: server.Namespace,
			},
		}
	}
	return requests
}
