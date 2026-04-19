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

// Package metrics provides Prometheus gauges for EnshroudedServer instances.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ReadyReplicas tracks the number of ready StatefulSet replicas per server.
	ReadyReplicas = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "enshrouded",
			Subsystem: "operator",
			Name:      "ready_replicas",
			Help:      "Number of ready replicas in the StatefulSet managed by this operator instance.",
		},
		[]string{"namespace", "name"},
	)

	// UpdateDeferred is 1 when a pending update is deferred, 0 otherwise.
	UpdateDeferred = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "enshrouded",
			Subsystem: "operator",
			Name:      "update_deferred",
			Help:      "1 if a pending StatefulSet update is deferred because players are connected, 0 otherwise.",
		},
		[]string{"namespace", "name"},
	)

	// Phase exposes the lifecycle phase as a labelled gauge (value always 1).
	Phase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "enshrouded",
			Subsystem: "operator",
			Name:      "phase_info",
			Help:      "Lifecycle phase of each EnshroudedServer (label 'phase' carries the value; gauge is always 1).",
		},
		[]string{"namespace", "name", "phase"},
	)

	// MaintenanceWindowActive is 1 when the server is currently inside a configured
	// maintenance window, 0 otherwise. Can be used to trigger alerts or silence
	// other alert rules during planned maintenance.
	MaintenanceWindowActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "enshrouded",
			Subsystem: "operator",
			Name:      "maintenance_window_active",
			Help:      "1 if the current time falls inside a configured maintenance window, 0 otherwise.",
		},
		[]string{"namespace", "name"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(ReadyReplicas, UpdateDeferred, Phase, MaintenanceWindowActive)
}

// SetServerMetrics refreshes all operator-level gauges for a single EnshroudedServer.
// oldPhase is the previous phase so the old label-set can be deleted when it changes.
// Application-level metrics (active_players, server_up, etc.) are reported by
// the metrics sidecar running inside the game server pod.
func SetServerMetrics(namespace, name string, readyReplicas int32, updateDeferred bool, phase, oldPhase string, maintenanceWindowActive bool) {
	ReadyReplicas.WithLabelValues(namespace, name).Set(float64(readyReplicas))

	deferred := float64(0)
	if updateDeferred {
		deferred = 1
	}
	UpdateDeferred.WithLabelValues(namespace, name).Set(deferred)

	if oldPhase != "" && oldPhase != phase {
		Phase.DeleteLabelValues(namespace, name, oldPhase)
	}
	Phase.WithLabelValues(namespace, name, phase).Set(1)

	inMaintenance := float64(0)
	if maintenanceWindowActive {
		inMaintenance = 1
	}
	MaintenanceWindowActive.WithLabelValues(namespace, name).Set(inMaintenance)
}

// DeleteServerMetrics removes all operator-level metrics for a server that has been deleted.
func DeleteServerMetrics(namespace, name, phase string) {
	ReadyReplicas.DeleteLabelValues(namespace, name)
	UpdateDeferred.DeleteLabelValues(namespace, name)
	Phase.DeleteLabelValues(namespace, name, phase)
	MaintenanceWindowActive.DeleteLabelValues(namespace, name)
}
