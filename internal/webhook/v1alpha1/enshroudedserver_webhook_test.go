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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
)

var _ = Describe("EnshroudedServer Webhook", func() {
	var (
		obj       *enshroudedv1alpha1.EnshroudedServer
		oldObj    *enshroudedv1alpha1.EnshroudedServer
		validator EnshroudedServerCustomValidator
		defaulter EnshroudedServerCustomDefaulter
	)

	BeforeEach(func() {
		obj = &enshroudedv1alpha1.EnshroudedServer{}
		oldObj = &enshroudedv1alpha1.EnshroudedServer{}
		validator = EnshroudedServerCustomValidator{}
		defaulter = EnshroudedServerCustomDefaulter{}
		// Defaulting normally runs before validation; pre-fill required fields.
		obj.Spec.Image.Repository = "sknnr/enshrouded-dedicated-server"
		oldObj.Spec.Image.Repository = "sknnr/enshrouded-dedicated-server"
	})

	Context("Defaulting Webhook", func() {
		It("should set all defaults on an empty spec", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.ServerName).To(Equal("Enshrouded Server"))
			Expect(obj.Spec.Port).To(Equal(int32(15637)))
			Expect(obj.Spec.SteamPort).To(Equal(int32(27015)))
			Expect(obj.Spec.ServerSlots).To(Equal(int32(16)))
			Expect(obj.Spec.ServerIP).To(Equal("0.0.0.0"))
			Expect(obj.Spec.Image.Repository).To(Equal("sknnr/enshrouded-dedicated-server"))
			Expect(obj.Spec.Image.Tag).To(Equal("latest"))
			Expect(obj.Spec.Image.PullPolicy).To(Equal(corev1.PullIfNotPresent))
			Expect(obj.Spec.Storage.Size.String()).To(Equal("10Gi"))
		})

		It("should not overwrite fields that are already set", func() {
			obj.Spec.ServerName = "My Server"
			obj.Spec.Port = 12345
			obj.Spec.SteamPort = 12346
			obj.Spec.ServerSlots = 4
			obj.Spec.ServerIP = "192.168.1.1"
			obj.Spec.Image.Repository = "custom/image"
			obj.Spec.Image.Tag = "v1.0"
			obj.Spec.Image.PullPolicy = corev1.PullAlways
			obj.Spec.Storage.Size = resource.MustParse("20Gi")

			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.ServerName).To(Equal("My Server"))
			Expect(obj.Spec.Port).To(Equal(int32(12345)))
			Expect(obj.Spec.SteamPort).To(Equal(int32(12346)))
			Expect(obj.Spec.ServerSlots).To(Equal(int32(4)))
			Expect(obj.Spec.ServerIP).To(Equal("192.168.1.1"))
			Expect(obj.Spec.Image.Repository).To(Equal("custom/image"))
			Expect(obj.Spec.Image.Tag).To(Equal("v1.0"))
			Expect(obj.Spec.Image.PullPolicy).To(Equal(corev1.PullAlways))
			Expect(obj.Spec.Storage.Size.String()).To(Equal("20Gi"))
		})
	})

	Context("Validation Webhook", func() {
		Context("ValidateCreate", func() {
			It("should accept a valid spec", func() {
				obj.Spec.Port = 15637
				obj.Spec.SteamPort = 27015
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject identical port and steamPort", func() {
				obj.Spec.Port = 15637
				obj.Spec.SteamPort = 15637
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("steamPort must differ from port"))
			})

			It("should reject an invalid serverIP", func() {
				obj.Spec.ServerIP = "not-an-ip"
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("must be a valid IP address"))
			})

			It("should accept 0.0.0.0 as serverIP", func() {
				obj.Spec.ServerIP = "0.0.0.0"
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept a valid IPv4 serverIP", func() {
				obj.Spec.ServerIP = "10.0.0.1"
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject serverPasswordSecretRef with empty name", func() {
				obj.Spec.ServerPasswordSecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ""},
					Key:                  "password",
				}
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("secret name must not be empty"))
			})

			It("should reject serverPasswordSecretRef with empty key", func() {
				obj.Spec.ServerPasswordSecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "my-secret"},
					Key:                  "",
				}
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("secret key must not be empty"))
			})

			It("should accept valid serverPasswordSecretRef", func() {
				obj.Spec.ServerPasswordSecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "my-secret"},
					Key:                  "password",
				}
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject empty image repository", func() {
				obj.Spec.Image.Repository = ""
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("image repository must not be empty"))
			})

			It("should return multiple errors at once", func() {
				obj.Spec.Port = 9999
				obj.Spec.SteamPort = 9999
				obj.Spec.ServerIP = "bad-ip"
				obj.Spec.Image.Repository = ""
				_, err := validator.ValidateCreate(ctx, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("steamPort must differ from port"))
				Expect(err.Error()).To(ContainSubstring("must be a valid IP address"))
				Expect(err.Error()).To(ContainSubstring("image repository must not be empty"))
			})
		})

		Context("ValidateUpdate", func() {
			It("should accept increasing storage size", func() {
				oldObj.Spec.Storage.Size = resource.MustParse("10Gi")
				obj.Spec.Storage.Size = resource.MustParse("20Gi")
				_, err := validator.ValidateUpdate(ctx, oldObj, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject shrinking storage size", func() {
				oldObj.Spec.Storage.Size = resource.MustParse("20Gi")
				obj.Spec.Storage.Size = resource.MustParse("10Gi")
				_, err := validator.ValidateUpdate(ctx, oldObj, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("storage size may not be reduced"))
			})

			It("should accept keeping the same storage size", func() {
				oldObj.Spec.Storage.Size = resource.MustParse("10Gi")
				obj.Spec.Storage.Size = resource.MustParse("10Gi")
				_, err := validator.ValidateUpdate(ctx, oldObj, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should still validate spec fields on update", func() {
				oldObj.Spec.Storage.Size = resource.MustParse("10Gi")
				obj.Spec.Storage.Size = resource.MustParse("10Gi")
				obj.Spec.Port = 5555
				obj.Spec.SteamPort = 5555
				_, err := validator.ValidateUpdate(ctx, oldObj, obj)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("steamPort must differ from port"))
			})
		})

		Context("ValidateDelete", func() {
			It("should always accept deletion", func() {
				_, err := validator.ValidateDelete(ctx, obj)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})
