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
	"context"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var enshroudedserverlog = logf.Log.WithName("enshroudedserver-resource")

const (
	defaultServerIP        = "0.0.0.0"
	defaultImageRepository = "ghcr.io/payback159/enshrouded-server"
)

// SetupEnshroudedServerWebhookWithManager registers the webhook for EnshroudedServer in the manager.
func SetupEnshroudedServerWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &enshroudedv1alpha1.EnshroudedServer{}).
		WithValidator(&EnshroudedServerCustomValidator{}).
		WithDefaulter(&EnshroudedServerCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-enshrouded-enshrouded-io-v1alpha1-enshroudedserver,mutating=true,failurePolicy=fail,sideEffects=None,groups=enshrouded.enshrouded.io,resources=enshroudedservers,verbs=create;update,versions=v1alpha1,name=menshroudedserver-v1alpha1.kb.io,admissionReviewVersions=v1

// EnshroudedServerCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind EnshroudedServer when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type EnshroudedServerCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind EnshroudedServer.
func (d *EnshroudedServerCustomDefaulter) Default(_ context.Context, obj *enshroudedv1alpha1.EnshroudedServer) error {
	enshroudedserverlog.Info("Defaulting for EnshroudedServer", "name", obj.GetName())

	spec := &obj.Spec

	if spec.ServerName == "" {
		spec.ServerName = "Enshrouded Server"
	}
	if spec.Port == 0 {
		spec.Port = 15637
	}
	if spec.SteamPort == 0 {
		spec.SteamPort = 27015
	}
	if spec.ServerSlots == 0 {
		spec.ServerSlots = 16
	}
	if spec.ServerIP == "" {
		spec.ServerIP = defaultServerIP
	}
	if spec.Image.Repository == "" {
		spec.Image.Repository = defaultImageRepository
	}
	if spec.Image.Tag == "" {
		spec.Image.Tag = "latest"
	}
	if spec.Image.PullPolicy == "" {
		spec.Image.PullPolicy = corev1.PullIfNotPresent
	}
	if spec.Storage.Size.IsZero() {
		spec.Storage.Size = resource.MustParse("10Gi")
	}
	if spec.Storage.RetainOnDelete == nil {
		t := true
		spec.Storage.RetainOnDelete = &t
	}

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-enshrouded-enshrouded-io-v1alpha1-enshroudedserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=enshrouded.enshrouded.io,resources=enshroudedservers,verbs=create;update,versions=v1alpha1,name=venshroudedserver-v1alpha1.kb.io,admissionReviewVersions=v1

// EnshroudedServerCustomValidator struct is responsible for validating the EnshroudedServer resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type EnshroudedServerCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type EnshroudedServer.
func (v *EnshroudedServerCustomValidator) ValidateCreate(_ context.Context, obj *enshroudedv1alpha1.EnshroudedServer) (admission.Warnings, error) {
	enshroudedserverlog.Info("Validation for EnshroudedServer upon creation", "name", obj.GetName())
	return validateEnshroudedServer(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type EnshroudedServer.
func (v *EnshroudedServerCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *enshroudedv1alpha1.EnshroudedServer) (admission.Warnings, error) {
	enshroudedserverlog.Info("Validation for EnshroudedServer upon update", "name", newObj.GetName())

	warns, err := validateEnshroudedServer(newObj)
	if err != nil {
		return warns, err
	}

	// Immutable: storage size may not shrink
	oldSize := oldObj.Spec.Storage.Size
	newSize := newObj.Spec.Storage.Size
	if !oldSize.IsZero() && newSize.Cmp(oldSize) < 0 {
		return warns, field.Invalid(
			field.NewPath("spec", "storage", "size"),
			newSize.String(),
			fmt.Sprintf("storage size may not be reduced (current: %s)", oldSize.String()),
		)
	}

	return warns, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type EnshroudedServer.
func (v *EnshroudedServerCustomValidator) ValidateDelete(_ context.Context, obj *enshroudedv1alpha1.EnshroudedServer) (admission.Warnings, error) {
	enshroudedserverlog.Info("Validation for EnshroudedServer upon deletion", "name", obj.GetName())
	return nil, nil
}

// validateEnshroudedServer runs all validations and returns a combined field error list.
func validateEnshroudedServer(obj *enshroudedv1alpha1.EnshroudedServer) (admission.Warnings, error) {
	var allErrs field.ErrorList
	spec := obj.Spec
	specPath := field.NewPath("spec")

	// Port and SteamPort must not be identical
	if spec.Port != 0 && spec.SteamPort != 0 && spec.Port == spec.SteamPort {
		allErrs = append(allErrs, field.Invalid(
			specPath.Child("steamPort"),
			spec.SteamPort,
			"steamPort must differ from port",
		))
	}

	// serverIP must be a valid IP address (if set to non-default)
	if spec.ServerIP != "" && spec.ServerIP != defaultServerIP {
		if ip := net.ParseIP(spec.ServerIP); ip == nil {
			allErrs = append(allErrs, field.Invalid(
				specPath.Child("serverIP"),
				spec.ServerIP,
				"must be a valid IP address",
			))
		}
	}

	// serverPasswordSecretRef: name and key must both be non-empty when set
	if ref := spec.ServerPasswordSecretRef; ref != nil {
		if ref.Name == "" {
			allErrs = append(allErrs, field.Required(
				specPath.Child("serverPasswordSecretRef", "name"),
				"secret name must not be empty",
			))
		}
		if ref.Key == "" {
			allErrs = append(allErrs, field.Required(
				specPath.Child("serverPasswordSecretRef", "key"),
				"secret key must not be empty",
			))
		}
	}

	// image repository must not be empty
	if spec.Image.Repository == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("image", "repository"),
			"image repository must not be empty",
		))
	}

	// storage size must be positive
	if !spec.Storage.Size.IsZero() && spec.Storage.Size.Sign() < 0 {
		allErrs = append(allErrs, field.Invalid(
			specPath.Child("storage", "size"),
			spec.Storage.Size.String(),
			"storage size must be positive",
		))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}
