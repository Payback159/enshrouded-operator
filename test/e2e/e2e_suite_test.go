//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/payback159/enshrouded-operator/test/utils"
)

var (
	// managerImage is the manager image to be built and loaded for testing.
	managerImage = "example.com/enshrouded-operator:v0.0.1"
	// sidecarImage is the metrics sidecar image built and loaded for testing.
	sidecarImage = "example.com/enshrouded-metrics-sidecar:v0.0.1"
	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting enshrouded-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	// TODO(user): If you want to change the e2e test vendor from Kind,
	// ensure the image is built and available, then remove the following block.
	By("loading the manager image on Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	By("building the metrics sidecar image")
	cmd = exec.Command("docker", "build", "-t", sidecarImage, "-f", "Dockerfile.sidecar", ".")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the metrics sidecar image")

	By("loading the metrics sidecar image on Kind")
	err = utils.LoadImageToKindClusterWithName(sidecarImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the metrics sidecar image into Kind")

	By("pulling the curl image for metrics test")
	cmd = exec.Command("docker", "pull", "--platform", "linux/amd64", "curlimages/curl:latest")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to pull curlimages/curl:latest")

	By("loading the curl image on Kind")
	kindClusterName := "enshrouded-operator-test-e2e"
	if v := os.Getenv("KIND_CLUSTER"); v != "" {
		kindClusterName = v
	}
	kindNode := kindClusterName + "-control-plane"
	cmd = exec.Command("bash", "-c",
		"docker save curlimages/curl:latest | docker exec -i "+kindNode+" ctr --namespace=k8s.io images import -")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load curlimages/curl:latest into Kind")

	// Install CRDs globally so that all Describe blocks (UpgradeStrategy,
	// VerticalScaling, Manager…) can apply EnshroudedServer CRs regardless
	// of the randomised execution order chosen by Ginkgo.
	By("installing CRDs")
	cmd = exec.Command("make", "install")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install CRDs")

	// Create the operator namespace and apply the restricted security label
	// so that all Describe blocks that reference the namespace succeed
	// regardless of order.
	By("creating manager namespace")
	cmd = exec.Command("kubectl", "create", "ns", "enshrouded-operator-system")
	_, _ = utils.Run(cmd) // ignore AlreadyExists from previous runs

	By("labeling the namespace to enforce the restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", "enshrouded-operator-system",
		"pod-security.kubernetes.io/enforce=restricted")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

	// Install CertManager before deploying the controller-manager, because the
	// kustomize manifest includes cert-manager Certificate/Issuer resources.
	setupCertManager()

	// Deploy the controller-manager once so UpgradeStrategy, VerticalScaling
	// and Manager tests all run against a live controller.
	By("deploying the controller-manager")
	cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

	// Wait for the controller pod to be Running so that the webhook is up
	// before any Describe block applies an EnshroudedServer CR.
	By("waiting for the controller-manager pod to be Running")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get",
			"pods", "-l", "control-plane=controller-manager",
			"-o", "go-template={{ range .items }}"+
				"{{ if not .metadata.deletionTimestamp }}"+
				"{{ .metadata.name }}"+
				"{{ \"\\n\" }}{{ end }}{{ end }}",
			"-n", namespace,
		)
		podOutput, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		podNames := utils.GetNonEmptyLines(podOutput)
		g.Expect(podNames).To(HaveLen(1))
		cmd = exec.Command("kubectl", "get", "pods", podNames[0],
			"-o", "jsonpath={.status.phase}", "-n", namespace)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Running"))
	}, 3*time.Minute, time.Second).Should(Succeed())

	By("waiting for the webhook service endpoints to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
			"-l", "kubernetes.io/service-name=enshrouded-operator-webhook-service",
			"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
		g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
	}, 3*time.Minute, time.Second).Should(Succeed())
})

var _ = AfterSuite(func() {
	teardownCertManager()

	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	_, _ = utils.Run(cmd)

	By("uninstalling CRDs")
	cmd = exec.Command("make", "uninstall")
	_, _ = utils.Run(cmd)

	By("removing manager namespace")
	cmd = exec.Command("kubectl", "delete", "ns", "enshrouded-operator-system", "--ignore-not-found")
	_, _ = utils.Run(cmd)
})

// setupCertManager installs CertManager if it is not already present.
// When CERT_MANAGER_INSTALL_SKIP=true the installation is still performed on a
// fresh cluster, but the suite will NOT clean it up afterwards (useful when
// cert-manager is pre-installed in a long-lived cluster and should not be removed).
func setupCertManager() {
	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter,
			"Warning: CERT_MANAGER_INSTALL_SKIP=true but cert-manager is not installed — installing now (will not be removed after the suite).\n")
	} else {
		// Mark for cleanup so teardownCertManager removes what we installed.
		shouldCleanupCertManager = true
	}

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
