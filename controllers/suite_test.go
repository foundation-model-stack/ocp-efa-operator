/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package controllers

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	efav1alpha1 "github.com/foundation-model-stack/ocp-efa-operator/api/v1alpha1"
	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	securityv1 "github.com/openshift/api/security/v1"
	nfdv1alpha1 "github.com/openshift/node-feature-discovery/pkg/apis/nfd/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment

const namespaceName = "ocp-efa-operator"
const kmmNamespace = "openshift-kmm"
const clusterName = "test"

func TestAPIs(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Setenv("KUBEBUILDER_ASSETS", filepath.Join(build.Default.GOPATH, "src", "github.com", "foundation-model-stack", "ocp-efa-operator", "bin", "k8s", "1.26.0-linux-amd64"))
	}
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "crd", "bases"),
			filepath.Join("..", "hack", "0000_03_security-openshift_01_scc.crd.yaml"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "github.com", "kubernetes-sigs", "kernel-module-management@v1.1.0", "config", "crd", "bases", "kmm.sigs.x-k8s.io_modules.yaml"),
			filepath.Join("..", "hack", "nfd.openshift.io_v1alpha1_nodefeaturerules.yaml"),
			filepath.Join("..", "hack", "nfd.k8s-sigs.io_v1alpha1_nodefeaturerules.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = efav1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = securityv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = kmmv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = nfdv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("Creating the Namespace to perform the tests")
	err = k8sClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
	Expect(err).To(Not(HaveOccurred()))
	err = k8sClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openshift-kmm"}})
	Expect(err).To(Not(HaveOccurred()))

	err = k8sClient.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node0", Labels: map[string]string{
		"feature.node.kubernetes.io/kernel-version.major": "4",
		"feature.node.kubernetes.io/kernel-version.minor": "18",
		"nvidia.com/cuda.driver.major":                    "525",
		"nvidia.com/cuda.driver.minor":                    "105",
		"nvidia.com/cuda.driver.rev":                      "17",
		"nvidia.com/gpu.present":                          "true",
		"feature.node.kubernetes.io/fms.io.efa.present":   "true",
		"feature.node.kubernetes.io/fms.io.dev.count":     "8",
	}}})
	Expect(err).To(Not(HaveOccurred()))
	err = k8sClient.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{
		"feature.node.kubernetes.io/kernel-version.major": "6",
		"feature.node.kubernetes.io/kernel-version.minor": "0",
		"feature.node.kubernetes.io/fms.io.efa.present":   "true",
		"feature.node.kubernetes.io/fms.io.dev.count":     "8",
	}}})
	Expect(err).To(Not(HaveOccurred()))
	err = k8sClient.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2", Labels: map[string]string{
		"feature.node.kubernetes.io/kernel-version.major": "5",
		"feature.node.kubernetes.io/kernel-version.minor": "0",
		"nvidia.com/cuda.driver.major":                    "535",
		"nvidia.com/cuda.driver.minor":                    "104",
		"nvidia.com/cuda.driver.rev":                      "05",
		"nvidia.com/gpu.present":                          "true",
		"feature.node.kubernetes.io/fms.io.dev.count":     "8",
	}}})
	Expect(err).To(Not(HaveOccurred()))
})

var _ = AfterSuite(func() {
	By("Deleting the Namespace to perform the tests")
	err := k8sClient.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
	Expect(err).To(Not(HaveOccurred()))
	By("Deleting the Namespace to perform the tests")
	err = k8sClient.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openshift-kmm"}})
	Expect(err).To(Not(HaveOccurred()))

	By("tearing down the test environment")
	err = testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
