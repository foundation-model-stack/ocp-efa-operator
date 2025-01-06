package controllers

import (
	"context"
	"fmt"
	"time"

	chartsv1alpha1 "github.com/foundation-model-stack/ocp-efa-operator/api/v1alpha1"
	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	securityv1 "github.com/openshift/api/security/v1"
	nfdv1alpha1 "github.com/openshift/node-feature-discovery/pkg/apis/nfd/v1alpha1"
	"golang.org/x/sys/unix"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func createEfaDriver(ctx context.Context, efaName string, namespaceName string, openShift bool) error {
	typedNamespaceName := types.NamespacedName{Name: efaName, Namespace: namespaceName}
	err := k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.EfaDriver{})
	if err != nil && errors.IsNotFound(err) {
		// Let's mock our custom resource at the same way that we would
		// apply on the cluster the manifest under config/samples
		efa := &chartsv1alpha1.EfaDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typedNamespaceName.Name,
				Namespace: typedNamespaceName.Namespace,
			},
			Spec: chartsv1alpha1.EfaDriverSpec{
				OpenShift:                  pointer.Bool(openShift),
				ImagePullSecrets:           []string{"secret"},
				DevicePluginServiceAccount: "sa",
				NodeSelector:               map[string]string{"node": "selector"},
			},
		}

		err = k8sClient.Create(ctx, efa)
	}
	return err
}

func deleteEfaDriver(ctx context.Context, efaName string) error {
	typedNamespaceName := types.NamespacedName{Name: efaName}
	err := k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.EfaDriver{})
	if err == nil {
		efa := &chartsv1alpha1.EfaDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typedNamespaceName.Name,
				Namespace: typedNamespaceName.Namespace,
			},
		}
		err = k8sClient.Delete(ctx, efa)
	}
	return err
}

func testCreateDeleteEfaDriver(efaName string, namespaceName string, openShift bool) {
	It("should successfully reconcile creating and deleting a custom resource for EfaDriver", func() {
		By("Creating the custom resource for the Kind EfaDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName, Namespace: namespaceName}
		err := createEfaDriver(ctx, efaName, namespaceName, openShift)
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if the custom resource was successfully created")
		err = k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.EfaDriver{})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if openShift {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", efaName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
					return err
				}
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-kmm-dockerfile", efaName), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err != nil {
				return err
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa", efaName), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err != nil {
				return err
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully deleted in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", efaName, "sa")}, &securityv1.SecurityContextConstraints{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-kmm-dockerfile", efaName), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa", efaName), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err == nil {
				return nil
			}
			return unix.ENOENT
		}, time.Second*10, time.Millisecond*100).ShouldNot(Succeed())
	})
}

func testDeleteAfterOperatorRestart(efaName string, namespaceName string) {
	It("should successfully reconcile deleting a custom resource for EfaDriver at operator restart", func() {
		By("Creating the custom resource for the Kind EfaDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName}
		err := createEfaDriver(ctx, efaName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		efaReconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created second time")
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the Daemonset manually")
		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the SCC manually")
		err = k8sClient.Delete(ctx, &securityv1.SecurityContextConstraints{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", efaName, "sa")}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the configmap for efa-kmm manually")
		err = k8sClient.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa-kmm-dockerfile", efaName), Namespace: kmmNamespace}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the Module for efa-kmm manually")
		err = k8sClient.Delete(ctx, &kmmv1beta1.Module{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa", efaName), Namespace: kmmNamespace}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the Module for ndr manually")
		err = k8sClient.Delete(ctx, &nfdv1alpha1.NodeFeatureRule{ObjectMeta: metav1.ObjectMeta{Name: efaName, Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully deleted in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", efaName, "sa")}, &securityv1.SecurityContextConstraints{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-kmm-dockerfile", efaName), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa", efaName), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err == nil {
				return nil
			}
			return unix.ENOENT
		}, time.Second*10, time.Millisecond*100).ShouldNot(Succeed())
	})
}

func testCreateOnUserDelete(efaName string, namespaceName string) {
	It("should successfully reconcile creating a custom resource for EfaDriver at random user deletion", func() {
		By("Creating the custom resource for the Kind EfaDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName}
		err := createEfaDriver(ctx, efaName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		efaReconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &securityv1.SecurityContextConstraints{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", efaName, "sa")}})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &nfdv1alpha1.NodeFeatureRule{ObjectMeta: metav1.ObjectMeta{Name: efaName, Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &kmmv1beta1.Module{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa", efaName), Namespace: kmmNamespace}})
		Expect(err).To(Not(HaveOccurred()))

		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", efaName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
				return err
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testCreateOnUserModify(efaName string, namespaceName string) {
	It("should successfully reconcile creating a custom resource for EfaDriver at random user modify", func() {
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName}

		By("Creating the Daemonset manually")
		err := k8sClient.Create(ctx, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ocp-efa-operator", "cluster": "test-efa"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "ocp-efa-operator", "cluster": "test-efa"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "test", Image: "image:latest"},
					}},
				},
			},
		})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the SCC manually")
		err = k8sClient.Create(ctx, &securityv1.SecurityContextConstraints{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", efaName, "sa")},
		})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the custom resource for the Kind EfaDriver")
		err = createEfaDriver(ctx, efaName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		efaReconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-efa-device-plugin", efaName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", efaName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
				return err
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testCreateTwiceEfaDriver(efaName string, namespaceName string) {
	It("should fail to reconcile creating the second custom resource for EfaDriver", func() {
		By("Creating the custom resource for the Kind EfaDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName}
		err := createEfaDriver(ctx, efaName, namespaceName, false)
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if the custom resource was successfully created")
		err = k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.EfaDriver{})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the second custom resource for the Kind EfaDriver")
		typedNamespaceName2 := types.NamespacedName{Name: efaName + "-2"}
		err = createEfaDriver(ctx, efaName+"-2", namespaceName, false)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the second custom resource created (should be ignored)")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName2})
		Expect(err).To(Not(HaveOccurred()))

		Eventually(func() error {
			found := &chartsv1alpha1.EfaDriverList{}
			if err := k8sClient.List(ctx, found); err != nil {
				return err
			}
			for _, efa := range found.Items {
				switch efa.Name {
				case typedNamespaceName.Name:
					if efa.Status.Condition != efaStatusReady {
						return unix.EAGAIN
					}
				case typedNamespaceName2.Name:
					if efa.Status.Condition != efaStatusIgnored {
						return unix.EAGAIN
					}
				default:
					Panic()
				}
			}
			return nil
		}, time.Second*10, time.Second).Should(Succeed())

		By("Reconciling the second custom resource created again (should be ignored)")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName2})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the third custom resource created again (should be ignored)")
		typedNamespaceName3 := types.NamespacedName{Name: efaName + "-3"}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName3})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName)
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind EfaDriver")
		err = deleteEfaDriver(ctx, efaName+"-2")
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName2})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testEmptyRequest(efaName string, namespaceName string) {
	It("should successfully reconcile empty requests by ignoring them", func() {
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: efaName}

		By("Reconciling the custom resource for empty (race condition?)")
		efaReconciler := &EfaDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}

		_, err := efaReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

var _ = Describe("EfaDriver controller", func() {
	Context("EfaDriver controller test", func() {
		testCreateDeleteEfaDriver(clusterName, namespaceName, true)
		testCreateDeleteEfaDriver(clusterName, namespaceName, false)
		testCreateDeleteEfaDriver(clusterName, namespaceName, false)
		testCreateOnUserDelete(clusterName, namespaceName)
		testDeleteAfterOperatorRestart(clusterName, namespaceName)
		testCreateOnUserModify(clusterName, namespaceName)
		testCreateTwiceEfaDriver(clusterName, namespaceName)
		testEmptyRequest(clusterName, namespaceName)
	})
})
