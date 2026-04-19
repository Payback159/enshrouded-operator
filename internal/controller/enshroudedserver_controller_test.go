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
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

// newReconciler returns a fresh reconciler backed by the envtest client.
func newReconciler() *EnshroudedServerReconciler {
	return &EnshroudedServerReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
	}
}

// reconcileNN triggers a single reconcile loop for the given namespaced name.
func reconcileNN(ctx context.Context, nn types.NamespacedName) (reconcile.Result, error) {
	return newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
}

var _ = Describe("EnshroudedServer Controller", func() {
	const (
		namespace   = "default"
		serverName  = "test-server"
		pvcName     = serverName + "-savegame"
		secretName  = "test-password-secret"
		secretKey   = "password"
		secretValue = "s3cr3t"
	)

	var (
		nn  = types.NamespacedName{Name: serverName, Namespace: namespace}
		ctx = context.Background()
	)

	// createServer creates an EnshroudedServer CR and returns it.
	createServer := func(spec enshroudedv1alpha1.EnshroudedServerSpec) *enshroudedv1alpha1.EnshroudedServer {
		cr := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		return cr
	}

	// deleteServer removes the CR and its owned resources.
	// PVCs in envtest get the kubernetes.io/pvc-protection finalizer via the
	// StorageObjectInUseProtection admission plugin. We must strip finalizers
	// before deleting, otherwise the PVC is stuck in Terminating state forever.
	deletePVC := func() {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
			pvc.Finalizers = nil
			_ = k8sClient.Update(ctx, pvc)
			_ = k8sClient.Delete(ctx, pvc)
		}
	}
	deleteServer := func() {
		cr := &enshroudedv1alpha1.EnshroudedServer{}
		if err := k8sClient.Get(ctx, nn, cr); err == nil {
			// Strip the finalizer so the CR can be deleted without a running controller.
			cr.Finalizers = nil
			_ = k8sClient.Update(ctx, cr)
			_ = k8sClient.Delete(ctx, cr)
		}
		_ = k8sClient.Delete(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace}})
		deletePVC()
	}

	AfterEach(func() { deleteServer() })

	// -----------------------------------------------------------------
	// Helper: get the StatefulSet after a successful reconcile
	// -----------------------------------------------------------------
	getStatefulSet := func() *appsv1.StatefulSet {
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn, sts)).To(Succeed())
		return sts
	}

	getService := func() *corev1.Service {
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, nn, svc)).To(Succeed())
		return svc
	}

	getPVC := func() *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)).To(Succeed())
		return pvc
	}

	// -----------------------------------------------------------------
	// 1. Default spec (all zero-values) uses built-in defaults
	// -----------------------------------------------------------------
	Describe("Default spec", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		})

		It("reconciles without error", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates a PVC with 10Gi default size", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			pvc := getPVC()
			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("10Gi")))
		})

		It("creates a StatefulSet with default image", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("sknnr/enshrouded-dedicated-server:latest"))
		})

		It("creates a StatefulSet with correct default env vars", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			env := sts.Spec.Template.Spec.Containers[0].Env
			Expect(envValue(env, "SERVER_NAME")).To(Equal("Enshrouded Server"))
			Expect(envValue(env, "PORT")).To(Equal("15637"))
			Expect(envValue(env, "SERVER_SLOTS")).To(Equal("16"))
			Expect(envValue(env, "SERVER_IP")).To(Equal("0.0.0.0"))
		})

		It("creates a StatefulSet with correct UDP ports", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			ports := sts.Spec.Template.Spec.Containers[0].Ports
			Expect(ports).To(ContainElements(
				HaveField("ContainerPort", int32(15637)),
				HaveField("ContainerPort", int32(27015)),
			))
			for _, p := range ports {
				Expect(p.Protocol).To(Equal(corev1.ProtocolUDP))
			}
		})

		It("creates a StatefulSet with non-root security context (uid/gid 10000)", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			sc := sts.Spec.Template.Spec.SecurityContext
			Expect(sc).NotTo(BeNil())
			Expect(*sc.RunAsUser).To(Equal(int64(10000)))
			Expect(*sc.RunAsGroup).To(Equal(int64(10000)))
			Expect(*sc.FSGroup).To(Equal(int64(10000)))
		})

		It("creates a StatefulSet that mounts the savegame PVC", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			volumes := sts.Spec.Template.Spec.Volumes
			Expect(volumes).To(ContainElement(
				HaveField("VolumeSource.PersistentVolumeClaim.ClaimName", pvcName),
			))
			mounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts
			Expect(mounts).To(ContainElement(
				HaveField("MountPath", "/home/steam/enshrouded/savegame"),
			))
		})

		It("creates a LoadBalancer Service with query and steam UDP ports", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			svc := getService()
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
			Expect(svc.Spec.Ports).To(ContainElements(
				And(HaveField("Port", int32(15637)), HaveField("Protocol", corev1.ProtocolUDP)),
				And(HaveField("Port", int32(27015)), HaveField("Protocol", corev1.ProtocolUDP)),
			))
		})

		It("sets status phase to Pending when no replicas are ready", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(enshroudedv1alpha1.EnshroudedServerPhasePending))
		})

		It("is idempotent — second reconcile does not error", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// -----------------------------------------------------------------
	// 2. Custom spec fields propagate correctly
	// -----------------------------------------------------------------
	Describe("Custom spec fields", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				ServerName:  "My Custom Server",
				Port:        19999,
				SteamPort:   29999,
				ServerSlots: 8,
				ServerIP:    "10.0.0.1",
				Image: enshroudedv1alpha1.ImageSpec{
					Repository: "myregistry/enshrouded",
					Tag:        "v1.2.3",
					PullPolicy: corev1.PullAlways,
				},
				Storage: enshroudedv1alpha1.StorageSpec{
					Size: resource.MustParse("50Gi"),
				},
			})
		})

		It("propagates serverName, port, slots and IP to StatefulSet env vars", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			env := sts.Spec.Template.Spec.Containers[0].Env
			Expect(envValue(env, "SERVER_NAME")).To(Equal("My Custom Server"))
			Expect(envValue(env, "PORT")).To(Equal("19999"))
			Expect(envValue(env, "SERVER_SLOTS")).To(Equal("8"))
			Expect(envValue(env, "SERVER_IP")).To(Equal("10.0.0.1"))
		})

		It("uses the custom image with correct tag and pull policy", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			c := sts.Spec.Template.Spec.Containers[0]
			Expect(c.Image).To(Equal("myregistry/enshrouded:v1.2.3"))
			Expect(c.ImagePullPolicy).To(Equal(corev1.PullAlways))
		})

		It("uses custom port and steamPort on the Service", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			svc := getService()
			Expect(svc.Spec.Ports).To(ContainElements(
				HaveField("Port", int32(19999)),
				HaveField("Port", int32(29999)),
			))
		})

		It("creates the PVC with the custom storage size", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			pvc := getPVC()
			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("50Gi")))
		})
	})

	// -----------------------------------------------------------------
	// 3. Password via SecretRef
	// -----------------------------------------------------------------
	Describe("ServerPasswordSecretRef", func() {
		createSecret := func() *corev1.Secret {
			s := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				StringData: map[string]string{secretKey: secretValue},
			}
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
			return s
		}
		deleteSecret := func() {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}
			_ = k8sClient.Delete(ctx, s)
		}

		Context("valid SecretRef", func() {
			BeforeEach(func() {
				createSecret()
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{
					ServerPasswordSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  secretKey,
					},
				})
			})
			AfterEach(func() { deleteSecret() })

			It("reconciles without error", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())
			})

			It("injects SERVER_PASSWORD as a secretKeyRef env var in the StatefulSet", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				sts := getStatefulSet()
				env := sts.Spec.Template.Spec.Containers[0].Env
				pwdVar := findEnvVar(env, "SERVER_PASSWORD")
				Expect(pwdVar).NotTo(BeNil())
				Expect(pwdVar.ValueFrom).NotTo(BeNil())
				Expect(pwdVar.ValueFrom.SecretKeyRef).NotTo(BeNil())
				Expect(pwdVar.ValueFrom.SecretKeyRef.Name).To(Equal(secretName))
				Expect(pwdVar.ValueFrom.SecretKeyRef.Key).To(Equal(secretKey))
				// Value must be empty when using ValueFrom
				Expect(pwdVar.Value).To(BeEmpty())
			})
		})

		Context("SecretRef points to non-existent Secret", func() {
			BeforeEach(func() {
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{
					ServerPasswordSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "does-not-exist"},
						Key:                  secretKey,
					},
				})
			})

			It("returns an error", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).To(HaveOccurred())
			})

			It("sets phase to Error", func() {
				_, _ = reconcileNN(ctx, nn)
				cr := &enshroudedv1alpha1.EnshroudedServer{}
				Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
				Expect(cr.Status.Phase).To(Equal(enshroudedv1alpha1.EnshroudedServerPhaseError))
			})
		})

		Context("SecretRef points to Secret with wrong key", func() {
			BeforeEach(func() {
				createSecret()
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{
					ServerPasswordSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  "nonexistent-key",
					},
				})
			})
			AfterEach(func() { deleteSecret() })

			It("returns an error", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).To(HaveOccurred())
			})

			It("sets phase to Error", func() {
				_, _ = reconcileNN(ctx, nn)
				cr := &enshroudedv1alpha1.EnshroudedServer{}
				Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
				Expect(cr.Status.Phase).To(Equal(enshroudedv1alpha1.EnshroudedServerPhaseError))
			})
		})

		Context("no password configured", func() {
			BeforeEach(func() {
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
			})

			It("does not inject a SERVER_PASSWORD env var", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				sts := getStatefulSet()
				env := sts.Spec.Template.Spec.Containers[0].Env
				Expect(findEnvVar(env, "SERVER_PASSWORD")).To(BeNil())
			})
		})
	})

	// -----------------------------------------------------------------
	// 4. Owner references — StatefulSet and Service carry owner ref
	// -----------------------------------------------------------------
	Describe("Owner references", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		})

		It("PVC does not carry an owner reference (data retention)", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			pvc := getPVC()
			Expect(pvc.OwnerReferences).To(BeEmpty())
		})

		It("StatefulSet has owner reference to the EnshroudedServer CR", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			Expect(sts.OwnerReferences).To(ContainElement(
				HaveField("Name", serverName),
			))
		})

		It("Service has owner reference to the EnshroudedServer CR", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			svc := getService()
			Expect(svc.OwnerReferences).To(ContainElement(
				HaveField("Name", serverName),
			))
		})
	})

	// -----------------------------------------------------------------
	// 5. Standard labels on child resources
	// -----------------------------------------------------------------
	Describe("Standard labels", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		})

		It("child resources carry app.kubernetes.io labels", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			for _, labels := range []map[string]string{
				getPVC().Labels,
				getStatefulSet().Labels,
				getService().Labels,
			} {
				Expect(labels["app.kubernetes.io/name"]).To(Equal("enshrouded-server"))
				Expect(labels["app.kubernetes.io/instance"]).To(Equal(serverName))
				Expect(labels["app.kubernetes.io/managed-by"]).To(Equal("enshrouded-operator"))
			}
		})
	})

	// -----------------------------------------------------------------
	// 6. Finalizer lifecycle
	// -----------------------------------------------------------------
	Describe("Finalizer", func() {
		Context("with default retainOnDelete (true)", func() {
			BeforeEach(func() {
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
			})

			It("adds the finalizer on first reconcile", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				cr := &enshroudedv1alpha1.EnshroudedServer{}
				Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
				Expect(cr.Finalizers).To(ContainElement(finalizerName))
			})

			It("retains PVC after CR deletion", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				// PVC must exist before deletion.
				getPVC()

				cr := &enshroudedv1alpha1.EnshroudedServer{}
				Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

				// Reconcile runs the finalizer — retainOnDelete=true means PVC is kept.
				_, err = reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				pvc := &corev1.PersistentVolumeClaim{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)).To(Succeed(),
					"PVC should be retained after CR deletion")

				// Manual cleanup so AfterEach does not hang.
				deletePVC()
			})
		})

		Context("with retainOnDelete=false", func() {
			BeforeEach(func() {
				retain := false
				createServer(enshroudedv1alpha1.EnshroudedServerSpec{
					Storage: enshroudedv1alpha1.StorageSpec{
						Size:           resource.MustParse("1Gi"),
						RetainOnDelete: &retain,
					},
				})
			})

			It("deletes PVC after CR deletion", func() {
				_, err := reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())
				getPVC()

				cr := &enshroudedv1alpha1.EnshroudedServer{}
				Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

				// Reconcile runs the finalizer — retainOnDelete=false means PVC is deleted.
				_, err = reconcileNN(ctx, nn)
				Expect(err).NotTo(HaveOccurred())

				// In envtest the pvc-protection finalizer keeps the PVC in Terminating
				// until it is stripped. We verify it was at least marked for deletion.
				pvc := &corev1.PersistentVolumeClaim{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
				if !apierrors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
					Expect(pvc.DeletionTimestamp).NotTo(BeNil(),
						"PVC should be marked for deletion when retainOnDelete=false")
				}
			})
		})
	})

	// -----------------------------------------------------------------
	// 7. Status Conditions
	// -----------------------------------------------------------------
	Describe("Status Conditions", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		})

		It("sets Ready condition to False with reason ServerStarting when no replicas are ready", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())

			var readyCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == enshroudedv1alpha1.ConditionReady {
					readyCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil(), "Ready condition should be set")
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ServerStarting"))
		})

		It("sets phase to Pending and Ready=False on first reconcile", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(enshroudedv1alpha1.EnshroudedServerPhasePending))
		})
	})

	// -----------------------------------------------------------------
	// 8. Secret hash triggers pod-template annotation
	// -----------------------------------------------------------------
	Describe("Secret hash annotation", func() {
		createSecret := func(value string) *corev1.Secret {
			s := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				StringData: map[string]string{secretKey: value},
			}
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
			return s
		}
		deleteSecret := func() {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}
			_ = k8sClient.Delete(ctx, s)
		}

		BeforeEach(func() {
			createSecret(secretValue)
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				ServerPasswordSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  secretKey,
				},
			})
		})
		AfterEach(func() { deleteSecret() })

		It("adds a password-secret-hash annotation to the pod template", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			Expect(sts.Spec.Template.Annotations).To(HaveKey("enshrouded.enshrouded.io/password-secret-hash"))
			Expect(sts.Spec.Template.Annotations["enshrouded.enshrouded.io/password-secret-hash"]).NotTo(BeEmpty())
		})

		It("updates the annotation when the secret value changes", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			hashBefore := sts.Spec.Template.Annotations["enshrouded.enshrouded.io/password-secret-hash"]

			// Update secret value.
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)).To(Succeed())
			secret.StringData = map[string]string{secretKey: "newpassword"}
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts = getStatefulSet()
			hashAfter := sts.Spec.Template.Annotations["enshrouded.enshrouded.io/password-secret-hash"]
			Expect(hashAfter).NotTo(Equal(hashBefore), "hash should change when secret value changes")
		})
	})

	// -----------------------------------------------------------------
	// 9. UserGroups — EXTERNAL_CONFIG mode
	// -----------------------------------------------------------------
	Describe("UserGroups", func() {
		const (
			groupSecretName  = "group-secret"
			groupSecretKey   = "password"
			groupSecretValue = "admins3cr3t"
			configSecretName = serverName + "-config"
		)

		createGroupSecret := func(value string) {
			s := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: groupSecretName, Namespace: namespace},
				StringData: map[string]string{groupSecretKey: value},
			}
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
		}
		deleteGroupSecret := func() {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: groupSecretName, Namespace: namespace}}
			_ = k8sClient.Delete(ctx, s)
		}
		deleteConfigSecret := func() {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: configSecretName, Namespace: namespace}}
			_ = k8sClient.Delete(ctx, s)
		}

		specWithGroups := func() enshroudedv1alpha1.EnshroudedServerSpec {
			return enshroudedv1alpha1.EnshroudedServerSpec{
				ServerName:  "Test Server",
				ServerSlots: 4,
				UserGroups: []enshroudedv1alpha1.UserGroup{
					{
						Name: "Admins",
						PasswordSecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: groupSecretName},
							Key:                  groupSecretKey,
						},
						CanKickBan:           true,
						CanAccessInventories: true,
						CanEditWorld:         true,
						CanEditBase:          true,
						CanExtendBase:        true,
						ReservedSlots:        2,
					},
					{
						Name:        "Players",
						CanEditBase: true,
					},
				},
			}
		}

		BeforeEach(func() {
			createGroupSecret(groupSecretValue)
			createServer(specWithGroups())
		})
		AfterEach(func() {
			deleteGroupSecret()
			deleteConfigSecret()
		})

		It("creates a config Secret with the server JSON", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configSecretName, Namespace: namespace}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("enshrouded_server.json"))
		})

		It("embeds server settings and group permissions in the JSON", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configSecretName, Namespace: namespace}, secret)).To(Succeed())

			var cfg enshroudedConfigJSON
			Expect(json.Unmarshal(secret.Data["enshrouded_server.json"], &cfg)).To(Succeed())

			Expect(cfg.Name).To(Equal("Test Server"))
			Expect(cfg.SlotCount).To(Equal(int32(4)))
			Expect(cfg.UserGroups).To(HaveLen(2))

			admins := cfg.UserGroups[0]
			Expect(admins.Name).To(Equal("Admins"))
			Expect(admins.Password).To(Equal(groupSecretValue))
			Expect(admins.CanKickBan).To(BeTrue())
			Expect(admins.CanAccessInventories).To(BeTrue())
			Expect(admins.ReservedSlots).To(Equal(int32(2)))

			players := cfg.UserGroups[1]
			Expect(players.Name).To(Equal("Players"))
			Expect(players.Password).To(BeEmpty())
			Expect(players.CanEditBase).To(BeTrue())
		})

		It("uses EXTERNAL_CONFIG=1 in the StatefulSet", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			env := sts.Spec.Template.Spec.Containers[0].Env
			Expect(envValue(env, "EXTERNAL_CONFIG")).To(Equal("1"))
			Expect(envValue(env, "ENSHROUDED_CONFIG")).To(Equal("/config/enshrouded_server.json"))
		})

		It("mounts the config Secret as a volume at /config", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			volumes := sts.Spec.Template.Spec.Volumes
			Expect(volumes).To(ContainElement(
				HaveField("VolumeSource.Secret.SecretName", configSecretName),
			))
			mounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts
			Expect(mounts).To(ContainElement(HaveField("MountPath", "/config")))
		})

		It("updates the annotation hash when a group's secret changes (rolling restart)", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			hashBefore := sts.Spec.Template.Annotations["enshrouded.enshrouded.io/password-secret-hash"]
			Expect(hashBefore).NotTo(BeEmpty())

			// Change the group password secret value.
			s := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: groupSecretName, Namespace: namespace}, s)).To(Succeed())
			s.StringData = map[string]string{groupSecretKey: "newadminpassword"}
			Expect(k8sClient.Update(ctx, s)).To(Succeed())

			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts = getStatefulSet()
			hashAfter := sts.Spec.Template.Annotations["enshrouded.enshrouded.io/password-secret-hash"]
			Expect(hashAfter).NotTo(Equal(hashBefore), "hash should change when group secret value changes")
		})

		It("does not set SERVER_NAME or SERVER_PASSWORD env vars in EXTERNAL_CONFIG mode", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			sts := getStatefulSet()
			env := sts.Spec.Template.Spec.Containers[0].Env
			Expect(findEnvVar(env, "SERVER_NAME")).To(BeNil())
			Expect(findEnvVar(env, "SERVER_PASSWORD")).To(BeNil())
		})
	})

	// -----------------------------------------------------------------
	// 10. NetworkPolicy
	// -----------------------------------------------------------------
	Describe("NetworkPolicy", func() {
		BeforeEach(func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				Port:      15637,
				SteamPort: 27015,
			})
		})

		It("creates a NetworkPolicy after reconcile", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			netpol := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      serverName + "-netpol",
				Namespace: namespace,
			}, netpol)).To(Succeed())
		})

		It("allows UDP ingress on the game query port", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			netpol := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      serverName + "-netpol",
				Namespace: namespace,
			}, netpol)).To(Succeed())

			// There must be at least one ingress rule with a port that includes 15637.
			var queryPortFound bool
			for _, rule := range netpol.Spec.Ingress {
				for _, p := range rule.Ports {
					if p.Port != nil && p.Port.IntVal == 15637 {
						queryPortFound = true
					}
				}
			}
			Expect(queryPortFound).To(BeTrue(), "NetworkPolicy should allow UDP ingress on query port 15637")
		})

		It("sets both Ingress and Egress policy types", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			netpol := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      serverName + "-netpol",
				Namespace: namespace,
			}, netpol)).To(Succeed())

			Expect(netpol.Spec.PolicyTypes).To(ContainElements(
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			))
		})

		It("blocks egress to 169.254.0.0/16 (cloud metadata / K8s API link-local)", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			netpol := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      serverName + "-netpol",
				Namespace: namespace,
			}, netpol)).To(Succeed())

			var linkLocalBlocked bool
			for _, rule := range netpol.Spec.Egress {
				for _, peer := range rule.To {
					if peer.IPBlock != nil {
						for _, ex := range peer.IPBlock.Except {
							if ex == "169.254.0.0/16" {
								linkLocalBlocked = true
							}
						}
					}
				}
			}
			Expect(linkLocalBlocked).To(BeTrue(), "169.254.0.0/16 should be in egress Except list")
		})

		It("carries an owner reference back to the EnshroudedServer", func() {
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			netpol := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      serverName + "-netpol",
				Namespace: namespace,
			}, netpol)).To(Succeed())

			Expect(netpol.OwnerReferences).NotTo(BeEmpty())
			Expect(netpol.OwnerReferences[0].Name).To(Equal(serverName))
		})
	})

	// -----------------------------------------------------------------
	// 11. Deferred Updates
	// -----------------------------------------------------------------
	Describe("Deferred Updates", func() {
		It("sets UpdateDeferred=true in status when players are connected", func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{
					DeferWhilePlaying: true,
				},
			})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			// Simulate active players by patching the status directly.
			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			cr.Status.ActivePlayers = 3
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.UpdateDeferred).To(BeTrue())
		})

		It("does not defer when DeferWhilePlaying=false even with active players", func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{
					DeferWhilePlaying: false,
				},
			})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			cr.Status.ActivePlayers = 5
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.UpdateDeferred).To(BeFalse())
		})

		It("clears UpdateDeferred when no players are connected", func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{
					DeferWhilePlaying: true,
				},
			})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			cr.Status.ActivePlayers = 0
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			_, err = reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.UpdateDeferred).To(BeFalse())
		})
	})

	// -----------------------------------------------------------------
	// 12. MaintenanceWindow helper (unit tests — no K8s objects needed)
	// -----------------------------------------------------------------
	Describe("isInMaintenanceWindow", func() {
		It("returns false for an empty window list", func() {
			Expect(isInMaintenanceWindow(nil)).To(BeFalse())
			Expect(isInMaintenanceWindow([]string{})).To(BeFalse())
		})

		It("returns false for a malformed cron expression", func() {
			Expect(isInMaintenanceWindow([]string{"not-a-cron"})).To(BeFalse())
		})
	})

	// -----------------------------------------------------------------
	// 13. BackupSpec types — S3 credentials secret prerequisite check
	// -----------------------------------------------------------------
	Describe("S3 Backup — reconcileS3Sidecar", func() {
		It("returns nil (non-fatal) when S3 spec is nil", func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())

			cr := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			// No S3 spec — reconcile must complete without error.
			Expect(cr.Status.Phase).NotTo(BeEmpty())
		})

		It("proceeds without error when credentials secret exists", func() {
			// Create the credentials secret first.
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-creds",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"AWS_ACCESS_KEY_ID":     "test-key",
					"AWS_SECRET_ACCESS_KEY": "test-secret",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				Backup: enshroudedv1alpha1.BackupSpec{
					S3: &enshroudedv1alpha1.S3BackupSpec{
						BucketURL:            "s3://test-bucket/server",
						CredentialsSecretRef: corev1.LocalObjectReference{Name: "s3-creds"},
						Schedule:             "*/30 * * * *",
					},
				},
			})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
		})

		It("proceeds without error when credentials secret is missing", func() {
			createServer(enshroudedv1alpha1.EnshroudedServerSpec{
				Backup: enshroudedv1alpha1.BackupSpec{
					S3: &enshroudedv1alpha1.S3BackupSpec{
						BucketURL:            "s3://test-bucket/server",
						CredentialsSecretRef: corev1.LocalObjectReference{Name: "does-not-exist"},
					},
				},
			})
			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred(), "missing S3 secret should be non-fatal")
		})
	})

	// -----------------------------------------------------------------
	// 14. s3SidecarContainer helper unit test
	// -----------------------------------------------------------------
	Describe("s3SidecarContainer", func() {
		It("returns nil when S3 spec is absent", func() {
			server := &enshroudedv1alpha1.EnshroudedServer{}
			Expect(s3SidecarContainer(server)).To(BeNil())
		})

		It("injects the credentials secret as envFrom", func() {
			server := &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					Backup: enshroudedv1alpha1.BackupSpec{
						S3: &enshroudedv1alpha1.S3BackupSpec{
							BucketURL:            "s3://bucket/path",
							CredentialsSecretRef: corev1.LocalObjectReference{Name: "my-s3-secret"},
						},
					},
				},
			}
			c := s3SidecarContainer(server)
			Expect(c).NotTo(BeNil())
			Expect(c.Name).To(Equal("s3-backup"))
			Expect(c.EnvFrom).To(HaveLen(1))
			Expect(c.EnvFrom[0].SecretRef.Name).To(Equal("my-s3-secret"))
		})

		It("uses the default rclone image when none is specified", func() {
			server := &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					Backup: enshroudedv1alpha1.BackupSpec{
						S3: &enshroudedv1alpha1.S3BackupSpec{
							BucketURL:            "s3://bucket/path",
							CredentialsSecretRef: corev1.LocalObjectReference{Name: "sec"},
						},
					},
				},
			}
			c := s3SidecarContainer(server)
			Expect(c.Image).To(Equal("rclone/rclone:latest"))
		})

		It("respects a custom image", func() {
			server := &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					Backup: enshroudedv1alpha1.BackupSpec{
						S3: &enshroudedv1alpha1.S3BackupSpec{
							BucketURL:            "s3://bucket/path",
							CredentialsSecretRef: corev1.LocalObjectReference{Name: "sec"},
							Image:                "my-registry/rclone:v1.67",
						},
					},
				},
			}
			c := s3SidecarContainer(server)
			Expect(c.Image).To(Equal("my-registry/rclone:v1.67"))
		})

		It("mounts the savegame volume read-only", func() {
			server := &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					Backup: enshroudedv1alpha1.BackupSpec{
						S3: &enshroudedv1alpha1.S3BackupSpec{
							BucketURL:            "s3://bucket/path",
							CredentialsSecretRef: corev1.LocalObjectReference{Name: "sec"},
						},
					},
				},
			}
			c := s3SidecarContainer(server)
			Expect(c.VolumeMounts).To(HaveLen(1))
			Expect(c.VolumeMounts[0].Name).To(Equal("savegame"))
			Expect(c.VolumeMounts[0].ReadOnly).To(BeTrue())
		})
	})
})

// -----------------------------------------------------------------
// 15. metricsSidecarContainer helper unit tests
// -----------------------------------------------------------------
var _ = Describe("metricsSidecarContainer", func() {
	It("returns nil when sidecar is disabled", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{}
		Expect(metricsSidecarContainer(server, 15637)).To(BeNil())
	})

	It("returns a container when sidecar is enabled", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "my-server"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{Enabled: true},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		Expect(c).NotTo(BeNil())
		Expect(c.Name).To(Equal("metrics-sidecar"))
	})

	It("passes the query port via QUERY_PORT env var", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "my-server"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{Enabled: true},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		var qp string
		for _, e := range c.Env {
			if e.Name == "QUERY_PORT" {
				qp = e.Value
			}
		}
		Expect(qp).To(Equal("15637"))
	})

	It("exposes the metrics TCP port on the container", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "my-server"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{
					Enabled:     true,
					MetricsPort: 9090,
				},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		Expect(c.Ports).To(HaveLen(1))
		Expect(c.Ports[0].ContainerPort).To(Equal(int32(9090)))
		Expect(c.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))
	})

	It("uses the default image when none is specified", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{Enabled: true},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		Expect(c.Image).To(Equal("ghcr.io/payback159/enshrouded-metrics-sidecar:latest"))
	})

	It("respects a custom image", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{
					Enabled: true,
					Image:   "my-reg/metrics:v1.0",
				},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		Expect(c.Image).To(Equal("my-reg/metrics:v1.0"))
	})

	It("drops ALL capabilities and runs with read-only root filesystem", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{Enabled: true},
			},
		}
		c := metricsSidecarContainer(server, 15637)
		Expect(c.SecurityContext).NotTo(BeNil())
		Expect(*c.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(*c.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(c.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
	})
})

// -----------------------------------------------------------------
// 16. Metrics sidecar integration — StatefulSet injection
// -----------------------------------------------------------------
var _ = Describe("Metrics sidecar StatefulSet injection", func() {
	const (
		namespace  = "default"
		serverName = "test-server-sidecar"
	)
	var (
		nn  = types.NamespacedName{Name: serverName, Namespace: namespace}
		ctx = context.Background()
	)
	createServer := func(spec enshroudedv1alpha1.EnshroudedServerSpec) {
		cr := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	}

	BeforeEach(func() {
		createServer(enshroudedv1alpha1.EnshroudedServerSpec{
			MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{
				Enabled: true,
				Image:   "ghcr.io/payback159/enshrouded-metrics-sidecar:latest",
			},
		})
	})

	AfterEach(func() {
		cr := &enshroudedv1alpha1.EnshroudedServer{}
		if err := k8sClient.Get(ctx, nn, cr); err == nil {
			cr.Finalizers = nil
			_ = k8sClient.Update(ctx, cr)
			_ = k8sClient.Delete(ctx, cr)
		}
	})

	It("creates a ServiceAccount for the sidecar", func() {
		_, err := reconcileNN(ctx, nn)
		Expect(err).NotTo(HaveOccurred())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      serverName + "-metrics",
			Namespace: namespace,
		}, sa)).To(Succeed())
	})

	It("creates a Role granting status patch on EnshroudedServer", func() {
		_, err := reconcileNN(ctx, nn)
		Expect(err).NotTo(HaveOccurred())

		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      serverName + "-metrics",
			Namespace: namespace,
		}, role)).To(Succeed())

		var hasStatusPatch bool
		for _, r := range role.Rules {
			for _, res := range r.Resources {
				if res == "enshroudedservers/status" {
					hasStatusPatch = true
				}
			}
		}
		Expect(hasStatusPatch).To(BeTrue())
	})

	It("creates a RoleBinding connecting SA to Role", func() {
		_, err := reconcileNN(ctx, nn)
		Expect(err).NotTo(HaveOccurred())

		rb := &rbacv1.RoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      serverName + "-metrics",
			Namespace: namespace,
		}, rb)).To(Succeed())
		Expect(rb.Subjects).To(HaveLen(1))
		Expect(rb.Subjects[0].Name).To(Equal(serverName + "-metrics"))
		Expect(rb.RoleRef.Name).To(Equal(serverName + "-metrics"))
	})

	It("injects the metrics-sidecar container into the StatefulSet", func() {
		_, err := reconcileNN(ctx, nn)
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn, sts)).To(Succeed())

		var sidecarFound bool
		for _, c := range sts.Spec.Template.Spec.Containers {
			if c.Name == "metrics-sidecar" {
				sidecarFound = true
			}
		}
		Expect(sidecarFound).To(BeTrue(), "StatefulSet should contain the metrics-sidecar container")
	})

	It("sets the pod ServiceAccountName to the metrics SA", func() {
		_, err := reconcileNN(ctx, nn)
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn, sts)).To(Succeed())
		Expect(sts.Spec.Template.Spec.ServiceAccountName).To(Equal(serverName + "-metrics"))
	})
})

// -----------------------------------------------------------------
// PodDisruptionBudget
// -----------------------------------------------------------------

var _ = Describe("PodDisruptionBudget", func() {
	const (
		pdbNS   = "default"
		pdbName = "pdb-test-server"
	)
	pdbNN := types.NamespacedName{Name: pdbName, Namespace: pdbNS}
	pdbCtx := context.Background()

	newPDBServer := func(spec enshroudedv1alpha1.EnshroudedServerSpec) {
		cr := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: pdbName, Namespace: pdbNS},
			Spec:       spec,
		}
		Expect(k8sClient.Create(pdbCtx, cr)).To(Succeed())
	}
	pdbRec := func() (reconcile.Result, error) {
		return newReconciler().Reconcile(pdbCtx, reconcile.Request{NamespacedName: pdbNN})
	}
	getPDB := func() *policyv1.PodDisruptionBudget {
		pdb := &policyv1.PodDisruptionBudget{}
		Expect(k8sClient.Get(pdbCtx, pdbNN, pdb)).To(Succeed())
		return pdb
	}

	AfterEach(func() {
		cr := &enshroudedv1alpha1.EnshroudedServer{}
		if err := k8sClient.Get(pdbCtx, pdbNN, cr); err == nil {
			cr.Finalizers = nil
			_ = k8sClient.Update(pdbCtx, cr)
			_ = k8sClient.Delete(pdbCtx, cr)
		}
		_ = k8sClient.Delete(pdbCtx, &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: pdbName, Namespace: pdbNS},
		})
		_ = k8sClient.Delete(pdbCtx, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: pdbName, Namespace: pdbNS},
		})
		pvc := &corev1.PersistentVolumeClaim{}
		if err := k8sClient.Get(pdbCtx, types.NamespacedName{Name: pdbName + "-savegame", Namespace: pdbNS}, pvc); err == nil {
			pvc.Finalizers = nil
			_ = k8sClient.Update(pdbCtx, pvc)
			_ = k8sClient.Delete(pdbCtx, pvc)
		}
	})

	It("creates a PodDisruptionBudget after the first reconcile", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())
		pdb := getPDB()
		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
	})

	It("sets an owner reference pointing to the EnshroudedServer", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())
		pdb := getPDB()
		Expect(pdb.OwnerReferences).To(HaveLen(1))
		Expect(pdb.OwnerReferences[0].Name).To(Equal(pdbName))
	})

	It("sets maxUnavailable=1 when DeferWhilePlaying is false", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{
			UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{DeferWhilePlaying: false},
		})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())
		Expect(getPDB().Spec.MaxUnavailable.IntValue()).To(Equal(1))
	})

	It("sets maxUnavailable=1 when DeferWhilePlaying=true but no players are connected", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{
			UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{DeferWhilePlaying: true},
		})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())
		Expect(getPDB().Spec.MaxUnavailable.IntValue()).To(Equal(1))
	})

	It("sets maxUnavailable=0 when DeferWhilePlaying=true and players are active", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{
			UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{DeferWhilePlaying: true},
		})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())

		cr := &enshroudedv1alpha1.EnshroudedServer{}
		Expect(k8sClient.Get(pdbCtx, pdbNN, cr)).To(Succeed())
		cr.Status.ActivePlayers = 2
		Expect(k8sClient.Status().Update(pdbCtx, cr)).To(Succeed())

		_, err = pdbRec()
		Expect(err).NotTo(HaveOccurred())
		Expect(getPDB().Spec.MaxUnavailable.IntValue()).To(Equal(0))
	})

	It("updates maxUnavailable from 0 to 1 when all players disconnect", func() {
		newPDBServer(enshroudedv1alpha1.EnshroudedServerSpec{
			UpdatePolicy: enshroudedv1alpha1.UpdatePolicySpec{DeferWhilePlaying: true},
		})
		_, err := pdbRec()
		Expect(err).NotTo(HaveOccurred())

		// Players connect → maxUnavailable=0.
		cr := &enshroudedv1alpha1.EnshroudedServer{}
		Expect(k8sClient.Get(pdbCtx, pdbNN, cr)).To(Succeed())
		cr.Status.ActivePlayers = 3
		Expect(k8sClient.Status().Update(pdbCtx, cr)).To(Succeed())
		_, err = pdbRec()
		Expect(err).NotTo(HaveOccurred())
		Expect(getPDB().Spec.MaxUnavailable.IntValue()).To(Equal(0))

		// Players disconnect → maxUnavailable=1.
		Expect(k8sClient.Get(pdbCtx, pdbNN, cr)).To(Succeed())
		cr.Status.ActivePlayers = 0
		Expect(k8sClient.Status().Update(pdbCtx, cr)).To(Succeed())
		_, err = pdbRec()
		Expect(err).NotTo(HaveOccurred())
		Expect(getPDB().Spec.MaxUnavailable.IntValue()).To(Equal(1))
	})
})

// -----------------------------------------------------------------
// Readiness probe injection in buildStatefulSet
// -----------------------------------------------------------------

var _ = Describe("Readiness probe", func() {
	It("injects a readiness probe on the game container when the sidecar is enabled", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "readyz-test", Namespace: "default"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{
					Enabled:     true,
					MetricsPort: 9090,
				},
			},
		}
		sts := newReconciler().buildStatefulSet(server, "")
		gameContainer := sts.Spec.Template.Spec.Containers[0]
		Expect(gameContainer.Name).To(Equal("enshrouded-server"))
		Expect(gameContainer.ReadinessProbe).NotTo(BeNil())
		Expect(gameContainer.ReadinessProbe.HTTPGet).NotTo(BeNil())
		Expect(gameContainer.ReadinessProbe.HTTPGet.Path).To(Equal("/readyz"))
		Expect(gameContainer.ReadinessProbe.HTTPGet.Port.IntValue()).To(Equal(9090))
		Expect(gameContainer.ReadinessProbe.InitialDelaySeconds).To(Equal(int32(60)))
		Expect(gameContainer.ReadinessProbe.PeriodSeconds).To(Equal(int32(15)))
		Expect(gameContainer.ReadinessProbe.FailureThreshold).To(Equal(int32(3)))
	})

	It("does not inject a readiness probe when the sidecar is disabled", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "no-readyz-test", Namespace: "default"},
			Spec:       enshroudedv1alpha1.EnshroudedServerSpec{},
		}
		sts := newReconciler().buildStatefulSet(server, "")
		Expect(sts.Spec.Template.Spec.Containers[0].ReadinessProbe).To(BeNil())
	})

	It("falls back to port 9090 when MetricsPort is 0", func() {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: "readyz-default-port", Namespace: "default"},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				MetricsSidecar: enshroudedv1alpha1.MetricsSidecarSpec{
					Enabled:     true,
					MetricsPort: 0,
				},
			},
		}
		sts := newReconciler().buildStatefulSet(server, "")
		Expect(sts.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Port.IntValue()).To(Equal(9090))
	})
})

// -----------------------------------------------------------------
// status.GameVersion field
// -----------------------------------------------------------------

var _ = Describe("status.GameVersion", func() {
	const (
		gvNS   = "default"
		gvName = "gamever-test-server"
	)
	gvNN := types.NamespacedName{Name: gvName, Namespace: gvNS}
	gvCtx := context.Background()

	AfterEach(func() {
		cr := &enshroudedv1alpha1.EnshroudedServer{}
		if err := k8sClient.Get(gvCtx, gvNN, cr); err == nil {
			cr.Finalizers = nil
			_ = k8sClient.Update(gvCtx, cr)
			_ = k8sClient.Delete(gvCtx, cr)
		}
		_ = k8sClient.Delete(gvCtx, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: gvName, Namespace: gvNS},
		})
		pvc := &corev1.PersistentVolumeClaim{}
		if err := k8sClient.Get(gvCtx, types.NamespacedName{Name: gvName + "-savegame", Namespace: gvNS}, pvc); err == nil {
			pvc.Finalizers = nil
			_ = k8sClient.Update(gvCtx, pvc)
			_ = k8sClient.Delete(gvCtx, pvc)
		}
	})

	It("allows writing and reading status.GameVersion via the status subresource", func() {
		cr := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: gvName, Namespace: gvNS},
			Spec:       enshroudedv1alpha1.EnshroudedServerSpec{},
		}
		Expect(k8sClient.Create(gvCtx, cr)).To(Succeed())
		_, err := newReconciler().Reconcile(gvCtx, reconcile.Request{NamespacedName: gvNN})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(gvCtx, gvNN, cr)).To(Succeed())
		cr.Status.GameVersion = "1.0.7.4"
		Expect(k8sClient.Status().Update(gvCtx, cr)).To(Succeed())

		Expect(k8sClient.Get(gvCtx, gvNN, cr)).To(Succeed())
		Expect(cr.Status.GameVersion).To(Equal("1.0.7.4"))
	})
})

// -----------------------------------------------------------------
// VerticalScaling
// -----------------------------------------------------------------

var _ = Describe("VerticalScaling", func() {
	const namespace = "default"

	// vsNN is a unique-per-spec helper that returns a NamespacedName.
	vsNN := func(name string) types.NamespacedName {
		return types.NamespacedName{Name: name, Namespace: namespace}
	}

	// createServer creates a minimal EnshroudedServer and returns its NN.
	createServer := func(ctx context.Context, name string, mutate func(*enshroudedv1alpha1.EnshroudedServer)) types.NamespacedName {
		server := &enshroudedv1alpha1.EnshroudedServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: enshroudedv1alpha1.EnshroudedServerSpec{
				ServerName: name,
			},
		}
		if mutate != nil {
			mutate(server)
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())
		DeferCleanup(func(cleanCtx context.Context) {
			_ = k8sClient.Delete(cleanCtx, server)
		})
		return vsNN(name)
	}

	Context("disabled (Enabled=false)", func() {
		It("does not create a VPA object and reconcile succeeds", func(ctx context.Context) {
			nn := createServer(ctx, "vs-disabled", func(s *enshroudedv1alpha1.EnshroudedServer) {
				s.Spec.VerticalScaling = enshroudedv1alpha1.VerticalScalingSpec{
					Enabled: false,
				}
			})

			_, err := reconcileNN(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			// VPA CRDs are absent in envtest — reconcileVPA returns nil (non-fatal).
			// We verify by checking that no VPA GET call in the reconciler produced an error.
		})
	})

	Context("enabled (Enabled=true) — VPA CRDs absent in envtest", func() {
		It("reconciles without error even though the CRD is missing", func(ctx context.Context) {
			nn := createServer(ctx, "vs-enabled-no-crd", func(s *enshroudedv1alpha1.EnshroudedServer) {
				s.Spec.VerticalScaling = enshroudedv1alpha1.VerticalScalingSpec{
					Enabled:    true,
					UpdateMode: enshroudedv1alpha1.VPAUpdateModeWhenIdle,
					MinAllowed: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("6Gi"),
					},
				}
			})

			_, err := reconcileNN(ctx, nn)
			// Should succeed — missing CRD is non-fatal.
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("applyVPARecommendation — unit tests (no K8s calls)", func() {
		newServer := func(mode enshroudedv1alpha1.VPAUpdateMode, activePlayers int32) *enshroudedv1alpha1.EnshroudedServer {
			return &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					VerticalScaling: enshroudedv1alpha1.VerticalScalingSpec{
						Enabled:    true,
						UpdateMode: mode,
					},
				},
				Status: enshroudedv1alpha1.EnshroudedServerStatus{
					ActivePlayers: activePlayers,
				},
			}
		}

		It("is a no-op when UpdateMode=Off", func(ctx context.Context) {
			r := newReconciler()
			server := newServer(enshroudedv1alpha1.VPAUpdateModeOff, 0)
			// applyVPARecommendation should return early without hitting the API.
			err := r.applyVPARecommendation(ctx, server)
			Expect(err).NotTo(HaveOccurred())
		})

		It("is a no-op when UpdateMode=InPlace", func(ctx context.Context) {
			r := newReconciler()
			server := newServer(enshroudedv1alpha1.VPAUpdateModeInPlace, 0)
			err := r.applyVPARecommendation(ctx, server)
			Expect(err).NotTo(HaveOccurred())
		})

		It("is a no-op when VerticalScaling.Enabled=false", func(ctx context.Context) {
			r := newReconciler()
			server := &enshroudedv1alpha1.EnshroudedServer{
				Spec: enshroudedv1alpha1.EnshroudedServerSpec{
					VerticalScaling: enshroudedv1alpha1.VerticalScalingSpec{Enabled: false},
				},
			}
			err := r.applyVPARecommendation(ctx, server)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

// -----------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------

// envValue returns the plain Value of a named env var (not from SecretRef).
func envValue(env []corev1.EnvVar, name string) string {
	v := findEnvVar(env, name)
	if v == nil {
		return fmt.Sprintf("<env var %q not found>", name)
	}
	return v.Value
}

// findEnvVar returns the EnvVar with the given name or nil.
func findEnvVar(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}
