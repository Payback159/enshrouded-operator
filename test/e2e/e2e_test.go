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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/payback159/enshrouded-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "enshrouded-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "enshrouded-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "enshrouded-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "enshrouded-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("cleaning up the metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("ensuring the metrics ClusterRoleBinding is clean before creating")
			cleanCmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
			_, _ = utils.Run(cleanCmd)

			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=enshrouded-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			By("waiting for the webhook service endpoints to be ready")
			verifyWebhookEndpointsReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
					"-l", "kubernetes.io/service-name=enshrouded-operator-webhook-service",
					"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
			}
			Eventually(verifyWebhookEndpointsReady, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("getting the metrics service ClusterIP to avoid DNS resolution issues")
			metricsClusterIP, err := getServiceClusterIP(metricsServiceName, namespace)
			Expect(err).NotTo(HaveOccurred(), "Failed to get metrics service ClusterIP")
			Expect(metricsClusterIP).NotTo(BeEmpty())

			curlPodOverrides := fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsClusterIP, serviceAccountName)
			createCurlPod := func() {
				cmd := exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
					"--namespace", namespace,
					"--image=curlimages/curl:latest",
					"--overrides", curlPodOverrides)
				_, _ = utils.Run(cmd)
			}

			By("creating the curl-metrics pod to access the metrics endpoint")
			createCurlPod()

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				if output == "Failed" {
					// Transient failure (e.g. DNS not yet propagated), delete and recreate
					delCmd := exec.Command("kubectl", "delete", "pod", "curl-metrics",
						"-n", namespace, "--ignore-not-found")
					_, _ = utils.Run(delCmd)
					createCurlPod()
				}
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status: "+output)
			}
			Eventually(verifyCurlUp, 5*time.Minute, 15*time.Second).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should provisioned cert-manager", func() {
			By("validating that cert-manager has the certificate Secret")
			verifyCertManager := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCertManager).Should(Succeed())
		})

		It("should have CA injection for mutating webhooks", func() {
			By("checking CA injection for mutating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"enshrouded-operator-mutating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				mwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		It("should have CA injection for validating webhooks", func() {
			By("checking CA injection for validating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"validatingwebhookconfigurations.admissionregistration.k8s.io",
					"enshrouded-operator-validating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				vwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		It("should create an EnshroudedServer and reconcile child resources", func() {
			const (
				crName      = "test-enshrouded-server"
				crNamespace = "default"
			)

			By("applying the EnshroudedServer sample CR")
			sampleCR := fmt.Sprintf(`
apiVersion: enshrouded.enshrouded.io/v1alpha1
kind: EnshroudedServer
metadata:
  name: %s
  namespace: %s
spec:
  serverName: "E2E Test Server"
  port: 15637
  steamPort: 27015
  serverSlots: 4
  storage:
    size: 1Gi
`, crName, crNamespace)

			tmpFile, err := os.CreateTemp("", "enshrouded-cr-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile.Name())
			_, err = tmpFile.WriteString(sampleCR)
			Expect(err).NotTo(HaveOccurred())
			Expect(tmpFile.Close()).To(Succeed())

			cmd := exec.Command("kubectl", "apply", "-f", tmpFile.Name())
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply EnshroudedServer CR")

			defer func() {
				cmd := exec.Command("kubectl", "delete", "-f", tmpFile.Name(), "--ignore-not-found")
				_, _ = utils.Run(cmd)
			}()

			By("waiting for the PVC to be created")
			verifyPVC := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pvc",
					fmt.Sprintf("%s-savegame", crName), "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(fmt.Sprintf("%s-savegame", crName)))
			}
			Eventually(verifyPVC).Should(Succeed())

			By("waiting for the StatefulSet to be created")
			verifyStatefulSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					crName, "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crName))
			}
			Eventually(verifyStatefulSet).Should(Succeed())

			By("waiting for the Service to be created")
			verifyService := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "service",
					crName, "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crName))
			}
			Eventually(verifyService).Should(Succeed())

			By("verifying the StatefulSet uses the correct image")
			cmd = exec.Command("kubectl", "get", "statefulset", crName, "-n", crNamespace,
				"-o", "jsonpath={.spec.template.spec.containers[0].image}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal("sknnr/enshrouded-dedicated-server:latest"))

			By("verifying the StatefulSet has the correct server name env var")
			cmd = exec.Command("kubectl", "get", "statefulset", crName, "-n", crNamespace,
				"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="SERVER_NAME")].value}`)
			out, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal("E2E Test Server"))

			By("verifying the EnshroudedServer status phase is set")
			verifyStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "enshroudedserver",
					crName, "-n", crNamespace,
					"-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty(), "status.phase should be set after reconciliation")
			}
			Eventually(verifyStatus).Should(Succeed())

			By("verifying reconcile success metrics are recorded")
			metricsOutput, err := getMetricsOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(metricsOutput).To(ContainSubstring(
				`controller_runtime_reconcile_total{controller="enshroudedserver",result="success"}`,
			))
		})

		It("should create RBAC and inject the metrics sidecar when enabled", func() {
			const (
				crName      = "test-sidecar-server"
				crNamespace = "default"
			)

			By("applying an EnshroudedServer CR with metricsSidecar enabled")
			sampleCR := fmt.Sprintf(`
apiVersion: enshrouded.enshrouded.io/v1alpha1
kind: EnshroudedServer
metadata:
  name: %s
  namespace: %s
spec:
  serverName: "E2E Sidecar Test Server"
  port: 15637
  steamPort: 27015
  serverSlots: 4
  storage:
    size: 1Gi
  metricsSidecar:
    enabled: true
    image: %s
    metricsPort: 9090
    scrapeIntervalSeconds: 15
`, crName, crNamespace, sidecarImage)

			tmpFile, err := os.CreateTemp("", "enshrouded-sidecar-cr-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile.Name())
			_, err = tmpFile.WriteString(sampleCR)
			Expect(err).NotTo(HaveOccurred())
			Expect(tmpFile.Close()).To(Succeed())

			cmd := exec.Command("kubectl", "apply", "-f", tmpFile.Name())
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply EnshroudedServer CR with sidecar")

			defer func() {
				cmd := exec.Command("kubectl", "delete", "-f", tmpFile.Name(), "--ignore-not-found")
				_, _ = utils.Run(cmd)
				// Clean up RBAC objects created for the sidecar.
				cmd = exec.Command("kubectl", "delete", "serviceaccount",
					crName+"-metrics", "-n", crNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "role",
					crName+"-metrics", "-n", crNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "rolebinding",
					crName+"-metrics", "-n", crNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			}()

			By("waiting for the ServiceAccount to be created")
			verifyServiceAccount := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "serviceaccount",
					crName+"-metrics", "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crName + "-metrics"))
			}
			Eventually(verifyServiceAccount).Should(Succeed())

			By("waiting for the Role to be created")
			verifyRole := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "role",
					crName+"-metrics", "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crName + "-metrics"))
			}
			Eventually(verifyRole).Should(Succeed())

			By("waiting for the RoleBinding to be created")
			verifyRoleBinding := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "rolebinding",
					crName+"-metrics", "-n", crNamespace,
					"-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crName + "-metrics"))
			}
			Eventually(verifyRoleBinding).Should(Succeed())

			By("verifying the StatefulSet contains the metrics-sidecar container")
			verifySidecarContainer := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset", crName, "-n", crNamespace,
					"-o", `jsonpath={.spec.template.spec.containers[*].name}`)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("metrics-sidecar"))
			}
			Eventually(verifySidecarContainer).Should(Succeed())

			By("verifying the StatefulSet uses the metrics sidecar image")
			cmd = exec.Command("kubectl", "get", "statefulset", crName, "-n", crNamespace,
				"-o", `jsonpath={.spec.template.spec.containers[?(@.name=="metrics-sidecar")].image}`)
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(sidecarImage))

			By("verifying the StatefulSet serviceAccountName is set to the sidecar SA")
			cmd = exec.Command("kubectl", "get", "statefulset", crName, "-n", crNamespace,
				"-o", "jsonpath={.spec.template.spec.serviceAccountName}")
			out, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(crName + "-metrics"))

			By("verifying the NetworkPolicy allows TCP ingress on the metrics port")
			cmd = exec.Command("kubectl", "get", "networkpolicy",
				crName+"-netpol", "-n", crNamespace,
				"-o", "jsonpath={.spec.ingress[*].ports[*].port}")
			out, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("9090"))

			By("waiting for the metrics-sidecar container to be running")
			// The sidecar starts independently of the game-server container so it
			// will enter Running state even before the game server is available.
			verifySidecarRunning := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", crName+"-0", "-n", crNamespace,
					"-o", `jsonpath={.status.containerStatuses[?(@.name=="metrics-sidecar")].state.running}`)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty(), "metrics-sidecar container is not yet running")
			}
			Eventually(verifySidecarRunning, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("port-forwarding the sidecar /metrics endpoint and verifying its response")
			// Start kubectl port-forward in the background and clean up when the
			// test finishes.
			pfCmd := exec.Command("kubectl", "port-forward",
				"pod/"+crName+"-0", "19090:9090", "-n", crNamespace)
			Expect(pfCmd.Start()).To(Succeed())
			defer func() { _ = pfCmd.Process.Kill() }()

			// Give port-forward a moment to establish before the first curl.
			verifySidecarMetrics := func(g Gomega) {
				curlCmd := exec.Command("curl", "-sf", "http://127.0.0.1:19090/metrics")
				metricsOut, err := curlCmd.Output()
				g.Expect(err).NotTo(HaveOccurred(), "curl to sidecar /metrics failed")
				g.Expect(string(metricsOut)).To(ContainSubstring("enshrouded_server_up"),
					"expected enshrouded_server_up metric in sidecar /metrics output")
			}
			Eventually(verifySidecarMetrics, 30*time.Second, 2*time.Second).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// getServiceClusterIP returns the ClusterIP of a service in the given namespace.
func getServiceClusterIP(serviceName, ns string) (string, error) {
	cmd := exec.Command("kubectl", "get", "service", serviceName, "-n", ns,
		"-o", "jsonpath={.spec.clusterIP}")
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
