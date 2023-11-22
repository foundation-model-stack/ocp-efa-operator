package controllers

import (
	"context"
	"fmt"
	"time"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	securityv1 "github.com/openshift/api/security/v1"
	chartsv1alpha1 "github.com/foundation-model-stack/ocp-efa-operator/api/v1alpha1"
	"golang.org/x/sys/unix"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func createGdrdrvDriver(ctx context.Context, objName string, namespaceName string, openShift bool) error {
	typedNamespaceName := types.NamespacedName{Name: objName, Namespace: namespaceName}
	err := k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.GdrdrvDriver{})
	if err != nil && errors.IsNotFound(err) {
		// Let's mock our custom resource at the same way that we would
		// apply on the cluster the manifest under config/samples
		obj := &chartsv1alpha1.GdrdrvDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typedNamespaceName.Name,
				Namespace: typedNamespaceName.Namespace,
			},
			Spec: chartsv1alpha1.GdrdrvDriverSpec{
				OpenShift:                  pointer.Bool(openShift),
				ImagePullSecrets:           []string{"secret"},
				DevicePluginServiceAccount: "sa",
				NodeSelector:               map[string]string{"node": "selector"},
			},
		}

		err = k8sClient.Create(ctx, obj)
	}
	return err
}

func deleteGdrdrvDriver(ctx context.Context, objName string) error {
	typedNamespaceName := types.NamespacedName{Name: objName}
	err := k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.GdrdrvDriver{})
	if err == nil {
		obj := &chartsv1alpha1.GdrdrvDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typedNamespaceName.Name,
				Namespace: typedNamespaceName.Namespace,
			},
		}
		err = k8sClient.Delete(ctx, obj)
	}
	return err
}

func testCreateDeleteGdrdrvDriver(objName string, namespaceName string, openShift bool) {
	It("should successfully reconcile creating and deleting a custom resource for GdrdrvDriver", func() {
		By("Creating the custom resource for the Kind GdrdrvDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName, Namespace: namespaceName}
		err := createGdrdrvDriver(ctx, objName, namespaceName, openShift)
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if the custom resource was successfully created")
		err = k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.GdrdrvDriver{})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if openShift {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", objName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
					return err
				}
			}
			for _, cudaDrvVer := range []string{"525.105.17", "535.104.05"} {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-kmm-dockerfile-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err != nil {
					return err
				}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err != nil {
					return err
				}
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully deleted in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", objName, "sa")}, &securityv1.SecurityContextConstraints{}); err == nil {
				return nil
			}
			for _, cudaDrvVer := range []string{"525.105.17", "535.104.05"} {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-kmm-dockerfile-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err == nil {
					return nil
				}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err == nil {
					return nil
				}
			}
			return unix.ENOENT
		}, time.Second*10, time.Millisecond*100).ShouldNot(Succeed())
	})
}

func testDeleteAfterGdrdrvOperatorRestart(objName string, namespaceName string) {
	It("should successfully reconcile deleting a custom resource for GdrdrvDriver at operator restart", func() {
		By("Creating the custom resource for the Kind GdrdrvDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName}
		err := createGdrdrvDriver(ctx, objName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created second time")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the Daemonset manually")
		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the SCC manually")
		err = k8sClient.Delete(ctx, &securityv1.SecurityContextConstraints{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", objName, "sa")}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the configmap for gdrdrv-kmm manually")
		err = k8sClient.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-kmm-dockerfile-cudadrv-525.105.17", objName), Namespace: kmmNamespace}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the Module for gdrdrv-kmm manually")
		err = k8sClient.Delete(ctx, &kmmv1beta1.Module{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-cudadrv-525.105.17", objName), Namespace: kmmNamespace}})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully deleted in the reconciliation")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err == nil {
				return nil
			}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", objName, "sa")}, &securityv1.SecurityContextConstraints{}); err == nil {
				return nil
			}
			for _, cudaDrvVer := range []string{"525.105.17", "535.104.05"} {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-kmm-dockerfile-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &corev1.ConfigMap{}); err == nil {
					return nil
				}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-cudadrv-%s", objName, cudaDrvVer), Namespace: kmmNamespace}, &kmmv1beta1.Module{}); err == nil {
					return nil
				}
			}
			return unix.ENOENT
		}, time.Second*10, time.Millisecond*100).ShouldNot(Succeed())
	})
}

func testCreateOnUserDeleteGdrdrv(objName string, namespaceName string) {
	It("should successfully reconcile creating a custom resource for GdrdrvDriver at random user deletion", func() {
		By("Creating the custom resource for the Kind GdrdrvDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName}
		err := createGdrdrvDriver(ctx, objName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &securityv1.SecurityContextConstraints{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", objName, "sa")}})
		Expect(err).To(Not(HaveOccurred()))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", objName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
				return err
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testCreateOnUserModifyGdrdrv(objName string, namespaceName string) {
	It("should successfully reconcile creating a custom resource for GdrdrvDriver at random user modify", func() {
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName}

		By("Creating the Daemonset manually")
		err := k8sClient.Create(ctx, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ocp-efa-operator", "cluster": "test"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "ocp-efa-operator", "cluster": "test"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "test", Image: "image:latest"},
					}},
				},
			},
		})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the SCC manually")
		err = k8sClient.Create(ctx, &securityv1.SecurityContextConstraints{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", objName, "sa")},
		})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the custom resource for the Kind GdrdrvDriver")
		err = createGdrdrvDriver(ctx, objName, namespaceName, true)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Delete(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}})
		Expect(err).To(Not(HaveOccurred()))

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if resources were successfully created in the reconciliation")
		Eventually(func() error {
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", objName), Namespace: namespaceName}, &appsv1.DaemonSet{}); err != nil {
				return err
			}
			if err = k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", objName, "sa")}, &securityv1.SecurityContextConstraints{}); err != nil {
				return err
			}
			return nil
		}, time.Second*10, time.Millisecond*10).Should(Succeed())

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testCreateTwiceGdrdrvDriver(objName string, namespaceName string) {
	It("should fail to reconcile creating the second custom resource for GdrdrvDriver", func() {
		By("Creating the custom resource for the Kind GdrdrvDriver")
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName}
		err := createGdrdrvDriver(ctx, objName, namespaceName, false)
		Expect(err).To(Not(HaveOccurred()))

		By("Checking if the custom resource was successfully created")
		err = k8sClient.Get(ctx, typedNamespaceName, &chartsv1alpha1.GdrdrvDriver{})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource created")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Creating the second custom resource for the Kind GdrdrvDriver")
		typedNamespaceName2 := types.NamespacedName{Name: objName + "-2"}
		err = createGdrdrvDriver(ctx, objName+"-2", namespaceName, false)
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the second custom resource created (should be ignored)")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName2})
		Expect(err).To(Not(HaveOccurred()))

		Eventually(func() error {
			found := &chartsv1alpha1.GdrdrvDriverList{}
			if err := k8sClient.List(ctx, found); err != nil {
				return err
			}
			for _, obj := range found.Items {
				switch obj.Name {
				case typedNamespaceName.Name:
					if obj.Status.Condition != efaStatusReady {
						return unix.EAGAIN
					}
				case typedNamespaceName2.Name:
					if obj.Status.Condition != efaStatusIgnored {
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
		typedNamespaceName3 := types.NamespacedName{Name: objName + "-3"}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName3})
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName)
		Expect(err).To(Not(HaveOccurred()))

		By("Removing the custom ressource for the Kind GdrdrvDriver")
		err = deleteGdrdrvDriver(ctx, objName+"-2")
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))

		By("Reconciling the custom resource deleted")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName2})
		Expect(err).To(Not(HaveOccurred()))
	})
}

func testEmptyRequestGdrdrv(objName string, namespaceName string) {
	It("should successfully reconcile empty requests by ignoring them", func() {
		ctx := context.Background()
		typedNamespaceName := types.NamespacedName{Name: objName}

		By("Reconciling the custom resource for empty (race condition?)")
		reconciler := &GdrdrvDriverReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: namespaceName,
		}

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typedNamespaceName})
		Expect(err).To(Not(HaveOccurred()))
	})
}

var _ = Describe("GdrdrvDriver controller", func() {
	Context("GdrdrvDriver controller test", func() {
		testCreateDeleteGdrdrvDriver(clusterName, namespaceName, true)
		testCreateDeleteGdrdrvDriver(clusterName, namespaceName, false)
		testCreateDeleteGdrdrvDriver(clusterName, namespaceName, false)
		testCreateOnUserDeleteGdrdrv(clusterName, namespaceName)
		testDeleteAfterGdrdrvOperatorRestart(clusterName, namespaceName)
		testCreateOnUserModifyGdrdrv(clusterName, namespaceName)
		testCreateTwiceGdrdrvDriver(clusterName, namespaceName)
		testEmptyRequestGdrdrv(clusterName, namespaceName)
	})
})
