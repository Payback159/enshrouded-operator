package controller

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

// reconcilePDB ensures a PodDisruptionBudget exists for the EnshroudedServer.
//
// The budget prevents involuntary disruptions (e.g. node drains) from
// terminating the game server pod while players are connected:
//
//   - maxUnavailable=0  when deferWhilePlaying is enabled, players are present,
//     and the cluster is not currently in a maintenance window.
//   - maxUnavailable=1  in all other cases (allows rolling updates and
//     node-drain operations to proceed normally).
func (r *EnshroudedServerReconciler) reconcilePDB(ctx context.Context, server *enshroudedv1alpha1.EnshroudedServer) error {
	log := logf.FromContext(ctx)

	maxUnavailable := intstr.FromInt32(1)
	if server.Spec.UpdatePolicy.DeferWhilePlaying &&
		server.Status.ActivePlayers > 0 &&
		!isInMaintenanceWindow(server.Spec.UpdatePolicy.MaintenanceWindows) {
		maxUnavailable = intstr.FromInt32(0)
	}

	desired := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      server.Name,
			Namespace: server.Namespace,
			Labels:    labelsForServer(server.Name),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForServer(server.Name),
			},
		},
	}
	if err := ctrl.SetControllerReference(server, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on PodDisruptionBudget %s: %w", server.Name, err)
	}

	existing := &policyv1.PodDisruptionBudget{}
	err := r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("getting PodDisruptionBudget %s: %w", server.Name, err)
		}
		log.Info("Creating PodDisruptionBudget", "pdb", server.Name, "maxUnavailable", maxUnavailable.String())
		if err := r.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating PodDisruptionBudget %s: %w", server.Name, err)
		}
		return nil
	}

	// Update if maxUnavailable changed.
	if existing.Spec.MaxUnavailable == nil ||
		existing.Spec.MaxUnavailable.String() != maxUnavailable.String() {
		existing.Spec.MaxUnavailable = &maxUnavailable
		log.Info("Updating PodDisruptionBudget", "pdb", server.Name, "maxUnavailable", maxUnavailable.String())
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating PodDisruptionBudget %s: %w", server.Name, err)
		}
	}
	return nil
}
