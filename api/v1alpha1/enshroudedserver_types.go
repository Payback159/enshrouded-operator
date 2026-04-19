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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageSpec defines the container image configuration.
type ImageSpec struct {
	// repository is the container image repository.
	// +kubebuilder:default="sknnr/enshrouded-dedicated-server"
	Repository string `json:"repository"`

	// tag is the container image tag.
	// +kubebuilder:default="latest"
	Tag string `json:"tag"`

	// pullPolicy is the image pull policy.
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// StorageSpec defines the persistent storage for the server's save data.
type StorageSpec struct {
	// size is the size of the PersistentVolumeClaim for save data.
	// +kubebuilder:default="10Gi"
	Size resource.Quantity `json:"size"`

	// storageClassName is the StorageClass to use for the PVC.
	// If empty, the cluster default StorageClass is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// retainOnDelete controls whether the PVC is kept when the EnshroudedServer is deleted.
	// Defaults to true to prevent accidental save-data loss.
	// +kubebuilder:default=true
	// +optional
	RetainOnDelete *bool `json:"retainOnDelete,omitempty"`
}

// UserGroup defines an access group for the Enshrouded server.
// When any UserGroups are configured, the operator generates an external JSON
// config file (EXTERNAL_CONFIG=1) and mounts it into the server container.
type UserGroup struct {
	// name is the display name of the user group.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// passwordSecretRef references a Secret key containing this group's join password.
	// If omitted, the group has no password.
	// +optional
	PasswordSecretRef *corev1.SecretKeySelector `json:"passwordSecretRef,omitempty"`

	// canKickBan controls whether group members can kick or ban other players.
	// +optional
	CanKickBan bool `json:"canKickBan,omitempty"`

	// canAccessInventories controls whether group members can open other players' inventories.
	// +optional
	CanAccessInventories bool `json:"canAccessInventories,omitempty"`

	// canEditWorld controls whether group members can edit the world.
	// +optional
	CanEditWorld bool `json:"canEditWorld,omitempty"`

	// canEditBase controls whether group members can edit bases.
	// +optional
	CanEditBase bool `json:"canEditBase,omitempty"`

	// canExtendBase controls whether group members can extend bases.
	// +optional
	CanExtendBase bool `json:"canExtendBase,omitempty"`

	// reservedSlots is the number of reserved player slots for this group.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ReservedSlots int32 `json:"reservedSlots,omitempty"`
}

// EnshroudedServerSpec defines the desired state of EnshroudedServer.
type EnshroudedServerSpec struct {
	// serverName is the display name of the Enshrouded dedicated server.
	// +kubebuilder:default="Enshrouded Server"
	// +optional
	ServerName string `json:"serverName,omitempty"`

	// serverPasswordSecretRef references a Secret key containing the server password.
	// If not set, the server will be publicly accessible (no password required).
	// +optional
	ServerPasswordSecretRef *corev1.SecretKeySelector `json:"serverPasswordSecretRef,omitempty"`

	// port is the UDP query port the server listens on.
	// +kubebuilder:default=15637
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// steamPort is the UDP Steam communication port.
	// +kubebuilder:default=27015
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	SteamPort int32 `json:"steamPort,omitempty"`

	// serverSlots is the maximum number of concurrent players.
	// +kubebuilder:default=16
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	ServerSlots int32 `json:"serverSlots,omitempty"`

	// serverIP is the IP address the server binds to.
	// +kubebuilder:default="0.0.0.0"
	// +optional
	ServerIP string `json:"serverIP,omitempty"`

	// image configures the container image for the server.
	// +optional
	Image ImageSpec `json:"image,omitempty"`

	// resources defines compute resource requirements for the server container.
	// For a hosting platform, we recommend setting at least a memory limit.
	// Enshrouded can be very bursty on large worlds — use resources.limits.cpu
	// with a value >= 4 and resources.limits.memory >= 16Gi for a full server.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// storage configures the PersistentVolumeClaim for the server's save data.
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// userGroups configures access groups with individual per-group passwords.
	// When set, the server switches to EXTERNAL_CONFIG mode and serverPasswordSecretRef
	// is ignored — each group controls access via its own PasswordSecretRef.
	// +optional
	UserGroups []UserGroup `json:"userGroups,omitempty"`

	// updatePolicy controls how image/config updates are applied to a running server.
	// +optional
	UpdatePolicy UpdatePolicySpec `json:"updatePolicy,omitempty"`

	// backup configures automated backup of the savegame PVC.
	// +optional
	Backup BackupSpec `json:"backup,omitempty"`

	// metricsSidecar configures the Prometheus metrics sidecar that runs alongside
	// the game server container. When enabled, the sidecar queries the Enshrouded
	// server via the Steam A2S query protocol, exposes Prometheus metrics
	// (active players, server availability, server info) on its own HTTP port,
	// and writes the live player count back to this resource's status so the
	// DeferWhilePlaying feature has accurate data.
	// +optional
	MetricsSidecar MetricsSidecarSpec `json:"metricsSidecar,omitempty"`
}

// MetricsSidecarSpec configures the metrics sidecar injected into the game server pod.
type MetricsSidecarSpec struct {
	// enabled activates the metrics sidecar. Set to true to inject the sidecar
	// into the game server pod.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// image is the container image for the metrics sidecar.
	// +kubebuilder:default="ghcr.io/payback159/enshrouded-metrics-sidecar:latest"
	// +optional
	Image string `json:"image,omitempty"`

	// metricsPort is the TCP port on which the sidecar exposes its /metrics endpoint.
	// Prometheus should be configured to scrape this port.
	// +kubebuilder:default=9090
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=65535
	// +optional
	MetricsPort int32 `json:"metricsPort,omitempty"`

	// scrapeIntervalSeconds controls how often the sidecar queries the game server.
	// +kubebuilder:default=15
	// +kubebuilder:validation:Minimum=5
	// +optional
	ScrapeIntervalSeconds int32 `json:"scrapeIntervalSeconds,omitempty"`
}

// BackupSpec defines automated backup behaviour for the server's savegame data.
type BackupSpec struct {
	// volumeSnapshot configures automatic VolumeSnapshot creation.
	// +optional
	VolumeSnapshot VolumeSnapshotBackupSpec `json:"volumeSnapshot,omitempty"`

	// s3 configures periodic rsync/rclone sync of the savegame directory to an S3 bucket.
	// +optional
	S3 *S3BackupSpec `json:"s3,omitempty"`
}

// VolumeSnapshotBackupSpec controls VolumeSnapshot creation.
type VolumeSnapshotBackupSpec struct {
	// enabled turns on automatic VolumeSnapshot creation.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// schedule is a cron expression that controls how often snapshots are taken.
	// Example: "0 */6 * * *" — every 6 hours.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// retainCount is the number of most-recent snapshots to keep.
	// Older snapshots are deleted automatically. Default: 3.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	RetainCount int32 `json:"retainCount,omitempty"`

	// volumeSnapshotClassName is the VolumeSnapshotClass to use.
	// If empty the cluster default is used.
	// +optional
	VolumeSnapshotClassName *string `json:"volumeSnapshotClassName,omitempty"`
}

// S3BackupSpec configures rsync/rclone of savegame data to an S3-compatible bucket.
type S3BackupSpec struct {
	// bucketURL is the target URL, e.g. "s3://my-bucket/enshrouded/my-server".
	// +kubebuilder:validation:MinLength=1
	BucketURL string `json:"bucketURL"`

	// credentialsSecretRef references a Secret with AWS_ACCESS_KEY_ID,
	// AWS_SECRET_ACCESS_KEY and (optionally) AWS_ENDPOINT_URL keys.
	// +kubebuilder:validation:Required
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`

	// schedule is a cron expression for the sync interval.
	// Example: "*/30 * * * *" — every 30 minutes.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// image is the rclone container image used for the backup sidecar.
	// +kubebuilder:default="rclone/rclone:latest"
	// +optional
	Image string `json:"image,omitempty"`
}

// UpdatePolicySpec defines when and how updates are applied to the server.
type UpdatePolicySpec struct {
	// deferWhilePlaying defers a rolling update as long as the server has active
	// players. The operator will requeue and retry every requeueInterval until
	// the server is empty.
	// +optional
	DeferWhilePlaying bool `json:"deferWhilePlaying,omitempty"`

	// maintenanceWindows is an optional list of time windows during which updates
	// are allowed even if players are connected (overrides deferWhilePlaying).
	// Each entry is a cron-style spec in the format "TZ=<tz> <min> <hour> * * <weekday>".
	// Example: "TZ=Europe/Berlin 0 3 * * 1-5" (Mon-Fri 03:00 Berlin time).
	// +optional
	MaintenanceWindows []string `json:"maintenanceWindows,omitempty"`

	// snapshotBeforeUpdate triggers a VolumeSnapshot immediately before a
	// StatefulSet pod-template change is applied. Requires VolumeSnapshot CRDs
	// to be installed and a VolumeSnapshotClass to be available.
	// If the snapshot fails the update proceeds anyway (best-effort).
	// +optional
	SnapshotBeforeUpdate bool `json:"snapshotBeforeUpdate,omitempty"`
}

// EnshroudedServerPhase represents the lifecycle phase of an EnshroudedServer.
// +kubebuilder:validation:Enum=Pending;Running;Updating;Error
type EnshroudedServerPhase string

const (
	// EnshroudedServerPhasePending means the server is being created.
	EnshroudedServerPhasePending EnshroudedServerPhase = "Pending"
	// EnshroudedServerPhaseRunning means the server is running and ready.
	EnshroudedServerPhaseRunning EnshroudedServerPhase = "Running"
	// EnshroudedServerPhaseUpdating means the server is being updated.
	EnshroudedServerPhaseUpdating EnshroudedServerPhase = "Updating"
	// EnshroudedServerPhaseError means the server encountered a configuration error.
	EnshroudedServerPhaseError EnshroudedServerPhase = "Error"

	// ConditionReady is the condition type indicating overall server readiness.
	ConditionReady = "Ready"
	// ConditionUpdateDeferred is set when a pending update is deferred because
	// players are connected and deferWhilePlaying is enabled.
	ConditionUpdateDeferred = "UpdateDeferred"
)

// EnshroudedServerStatus defines the observed state of EnshroudedServer.
type EnshroudedServerStatus struct {
	// phase summarizes the overall status of the EnshroudedServer.
	// +optional
	Phase EnshroudedServerPhase `json:"phase,omitempty"`

	// readyReplicas is the number of ready replicas in the underlying StatefulSet.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// activePlayers is the last-observed number of connected players.
	// Updated by the metrics sidecar running inside the game server pod.
	// +optional
	ActivePlayers int32 `json:"activePlayers,omitempty"`

	// gameVersion is the game server version string reported via the A2S protocol.
	// Updated by the metrics sidecar running inside the game server pod.
	// +optional
	GameVersion string `json:"gameVersion,omitempty"`

	// updateDeferred is true while a pending update is held back because
	// players are connected and deferWhilePlaying is enabled.
	// +optional
	UpdateDeferred bool `json:"updateDeferred,omitempty"`

	// conditions represent the current state of the EnshroudedServer resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Players",type="integer",JSONPath=".status.activePlayers"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.gameVersion"
// +kubebuilder:printcolumn:name="Deferred",type="boolean",JSONPath=".status.updateDeferred"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EnshroudedServer is the Schema for the enshroudedservers API.
type EnshroudedServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of EnshroudedServer
	// +required
	Spec EnshroudedServerSpec `json:"spec"`

	// status defines the observed state of EnshroudedServer
	// +optional
	Status EnshroudedServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// EnshroudedServerList contains a list of EnshroudedServer.
type EnshroudedServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EnshroudedServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EnshroudedServer{}, &EnshroudedServerList{})
}
