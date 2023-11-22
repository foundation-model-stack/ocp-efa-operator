/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	securityv1 "github.com/openshift/api/security/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	efav1alpha1 "github.com/foundation-model-stack/ocp-efa-operator/api/v1alpha1"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// GdrdrvDriverReconciler reconciles a GdrdrvDriver object
type GdrdrvDriverReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	OperatorNamespace string
	backOffDuration   map[string]time.Duration
}

//+kubebuilder:rbac:groups=efa.fms.io,resources=gdrdrvdrivers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=efa.fms.io,resources=gdrdrvdrivers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=efa.fms.io,resources=gdrdrvdrivers/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=kmm.sigs.x-k8s.io,resources=modules,verbs=get;list;watch;create;delete;deletecollection
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;delete;deletecollection
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;

// SetupWithManager sets up the controller with the Manager.
func (r *GdrdrvDriverReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&efav1alpha1.GdrdrvDriver{}).
		Owns(&v1.ConfigMap{}).
		Owns(&kmmv1beta1.Module{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&securityv1.SecurityContextConstraints{}).
		Owns(&v1.Node{}).
		Complete(r)
}

func (r *GdrdrvDriverReconciler) GetRequeueAfter(ctx context.Context, req ctrl.Request) time.Duration {
	reqStr := req.String()
	if r.backOffDuration == nil {
		r.backOffDuration = make(map[string]time.Duration)
	}
	backOffDuration := r.backOffDuration[reqStr] + time.Second
	r.backOffDuration[reqStr] = backOffDuration
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler", "GetRequeueAfter")
	l.Info("RequeueAfter", "req", reqStr, "backOffDuration", backOffDuration.String())
	return backOffDuration
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GdrdrvDriver object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *GdrdrvDriverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.Reconcile", req.NamespacedName)
	efas := &efav1alpha1.GdrdrvDriverList{}
	err := r.List(ctx, efas)
	if errors.IsNotFound(err) || len(efas.Items) == 0 {
		return ctrl.Result{}, nil
	} else if err != nil {
		l.Error(err, "Failed: Reconcile, List", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}

	var efa *efav1alpha1.GdrdrvDriver = nil
	var deployed = ""
	for _, ext := range efas.Items {
		if ext.Name == req.Name {
			efa = &ext
			break
		} else if ext.Status.Condition != efaStatusIgnored {
			deployed = ext.Name
		}
	}
	if efa == nil {
		return ctrl.Result{}, nil
	}
	if efa.Status.Condition == efaStatusIgnored {
		return ctrl.Result{}, nil
	}
	if deployed != "" {
		efa.Status.Condition = efaStatusIgnored
		efa.Status.Description = fmt.Sprintf("Ignored since an active resource is already deployed (%s)", deployed)
		if err := r.Status().Update(ctx, efa); err != nil {
			l.Error(err, "Failed: Reconcile", "Update", req)
			return ctrl.Result{}, err
		}
		l.Info("Reconcile, an active resource is already deployed, ignore", "request", req, "deployed", deployed)
		return ctrl.Result{}, nil
	}

	// Update Status and add finalizer to instance
	if !controllerutil.ContainsFinalizer(efa, efaFinalizer) {
		controllerutil.AddFinalizer(efa, efaFinalizer)
		if err = r.Update(ctx, efa); err != nil {
			l.Error(err, "Failed: Reconcile", "Update", req)
			return ctrl.Result{}, err
		}
		if efa.GetDeletionTimestamp() == nil && (efa.Status.Condition == "") {
			efa.Status.Condition = efaStatusCreating
			if err = r.Status().Update(ctx, efa); err != nil {
				l.Error(err, "Failed: Reconcile", "Update", req)
				return ctrl.Result{}, err
			}
		}
	}

	var updateStatus = false
	var requeue = false
	if efa.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(efa, efaFinalizer) {
			if efa.Status.Condition != efaStatusDeleting {
				efa.Status.Condition = efaStatusDeleting
				efa.Status.Description = efaStatusDeleting
				if err = r.Status().Update(ctx, efa); err != nil {
					l.Error(err, "Failed: Reconcile", "Update", req)
					return ctrl.Result{}, err
				}
			}
			if err = r.DeleteCluster(ctx, efa); err != nil {
				efa.Status.Condition = efaStatusError
				efa.Status.Description = "Failed to delete resources"
				updateStatus = true
			} else {
				if controllerutil.RemoveFinalizer(efa, efaFinalizer) {
					if err = r.Update(ctx, efa); err != nil {
						l.Error(err, "Failed: Reconcile", "Update", req)
						return ctrl.Result{}, err
					}
				}
			}
		}
	} else {
		if efa.Status.Condition == efaStatusError {
			efa.Status.Condition = efaStatusCreating
			efa.Status.Description = efaStatusCreating
			if err = r.Status().Update(ctx, efa); err != nil {
				l.Error(err, "Failed: Reconcile", "Update", req)
				return ctrl.Result{}, err
			}
		}
		requeue, err = r.CreateCluster(ctx, efa)
		if err != nil {
			efa.Status.Condition = efaStatusError
			efa.Status.Description = fmt.Sprintf("Failed to create resources, err=%v", err)
			updateStatus = true
		} else if requeue && efa.Status.Condition != efaStatusDiscovery {
			efa.Status.Condition = efaStatusDiscovery
			efa.Status.Description = "Waiting for Node Disocvery to create fms.io labels at all the workers"
			updateStatus = true
		} else if !requeue && err == nil && efa.Status.Condition != efaStatusReady {
			efa.Status.Condition = efaStatusReady
			efa.Status.Description = efaStatusReady
			updateStatus = true
		}
	}
	if updateStatus {
		if err = r.Status().Update(ctx, efa); err != nil {
			l.Error(err, "Failed: Reconcile", "Update", req)
			return ctrl.Result{}, err
		}
	}
	if requeue {
		return ctrl.Result{Requeue: requeue, RequeueAfter: r.GetRequeueAfter(ctx, req)}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	delete(r.backOffDuration, req.String())
	return ctrl.Result{}, nil
}

func (r *GdrdrvDriverReconciler) CreateCluster(ctx context.Context, efa *efav1alpha1.GdrdrvDriver) (requeue bool, err error) {
	gvk, err := apiutil.GVKForObject(efa, r.Scheme)
	if err != nil {
		l := log.FromContext(ctx, "GdrdrvDriverReconciler.CreateCluster")
		l.Error(err, "GVKForObject")
		return false, err
	}
	owner := &metav1.OwnerReference{
		APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: efa.Name, UID: efa.GetUID(),
		BlockOwnerDeletion: pointer.Bool(true), Controller: pointer.Bool(true),
	}

	var cudaDrvVers map[string]map[string]bool //key: cudaVer, value: kernelVers
	requeue, cudaDrvVers, err = r.GetNodeStatus(ctx, efa)
	if requeue || err != nil {
		return
	}
	for cudaDrvVer, kernelVers := range cudaDrvVers {
		if requeue, err = r.CreateKmmDockerfile(ctx, efa, cudaDrvVer, owner); requeue || err != nil {
			return
		}
		if requeue, err = r.CreateKmm(ctx, efa, cudaDrvVer, kernelVers, owner); requeue || err != nil {
			return
		}
	}
	if requeue, err = r.CraeteDevicePluginScc(ctx, efa, owner); requeue || err != nil {
		return
	}
	if requeue, err = r.CreateDevicePlugin(ctx, efa, owner); requeue || err != nil {
		return
	}
	return
}

func (r *GdrdrvDriverReconciler) GetNodeStatus(ctx context.Context, efa *efav1alpha1.GdrdrvDriver) (requeue bool, cudaDrvVers map[string]map[string]bool, err error) {
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.GetNodeStatus", "Nodes")
	var nodes v1.NodeList
	err = r.List(ctx, &nodes)
	if err != nil {
		l.Error(err, "List", "Nodes")
		return false, nil, err
	}

	cudaDrvVers = make(map[string]map[string]bool) // key=cudaVer, value = kernelVer
	var waitingCount, completeCount int
	for _, node := range nodes.Items {
		labels := node.GetLabels()
		majorVer, majorOk := labels["feature.node.kubernetes.io/kernel-version.major"]
		minorVer, minorOk := labels["feature.node.kubernetes.io/kernel-version.minor"]
		if !majorOk || !minorOk {
			// node feature discovery does not work (often on master nodes and thus we skip).
			continue
		}
		completeCount += 1
		var kernelVer = fmt.Sprintf("%s.%s", majorVer, minorVer)
		majorVer, majorOk = labels["nvidia.com/cuda.driver.major"]
		minorVer, minorOk = labels["nvidia.com/cuda.driver.minor"]
		revVer, revOk := labels["nvidia.com/cuda.driver.rev"]
		if majorOk && minorOk && revOk {
			cudaDrvVer := fmt.Sprintf("%s.%s.%s", majorVer, minorVer, revVer)
			if _, ok := cudaDrvVers[cudaDrvVer]; !ok {
				cudaDrvVers[cudaDrvVer] = make(map[string]bool)
			}
			cudaDrvVers[cudaDrvVer][kernelVer] = true
		}
	}
	if waitingCount > 0 {
		l.Info("GetNodeStatus, waiting for node discovery to create labels", "waitingCount", strconv.Itoa(waitingCount), "completeCount", strconv.Itoa(completeCount+waitingCount))
		return true, nil, nil
	}
	return false, cudaDrvVers, nil
}

func (r *GdrdrvDriverReconciler) CraeteDevicePluginScc(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, owner *metav1.OwnerReference) (requeue bool, err error) {
	if efa.Spec.OpenShift == nil || !*efa.Spec.OpenShift {
		return false, nil
	}
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s", efa.Name, efa.Spec.DevicePluginServiceAccount)}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.CraeteDevicePluginScc", obj)
	var orig securityv1.SecurityContextConstraints
	err = r.Get(ctx, obj, &orig)
	if err == nil {
		return false, nil
	} else if !errors.IsNotFound(err) {
		l.Error(err, "Get", "ConfigMap", obj)
		return false, err
	}
	scc := &securityv1.SecurityContextConstraints{
		AllowHostDirVolumePlugin: true, AllowPrivilegeEscalation: pointer.Bool(true), AllowPrivilegedContainer: true,
		AllowHostIPC: false, AllowHostNetwork: false, AllowHostPID: false, AllowHostPorts: false, Priority: pointer.Int32(10),
		AllowedCapabilities: []v1.Capability{"MKNOD"}, ForbiddenSysctls: []string{"*"}, ReadOnlyRootFilesystem: false,
		FSGroup:            securityv1.FSGroupStrategyOptions{Type: securityv1.FSGroupStrategyRunAsAny},
		RunAsUser:          securityv1.RunAsUserStrategyOptions{Type: securityv1.RunAsUserStrategyRunAsAny},
		SELinuxContext:     securityv1.SELinuxContextStrategyOptions{Type: securityv1.SELinuxStrategyRunAsAny},
		SupplementalGroups: securityv1.SupplementalGroupsStrategyOptions{Type: securityv1.SupplementalGroupsStrategyRunAsAny},
		Volumes:            []securityv1.FSType{securityv1.FSTypeSecret, securityv1.FSTypePersistentVolumeClaim},
		Users:              []string{fmt.Sprintf("system:serviceaccount:%s:%s", r.OperatorNamespace, efa.Spec.DevicePluginServiceAccount)},
		ObjectMeta: metav1.ObjectMeta{
			Name: obj.Name, OwnerReferences: []metav1.OwnerReference{*owner}, Labels: map[string]string{labelKey: efa.Name},
		},
	}
	if err := r.Create(ctx, scc, &client.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		l.Error(err, "Create", "SecurityContextConstraints", obj)
		return false, err
	}
	l.Info("Create", "SecurityContextConstraints", obj)
	return false, nil
}

func (r *GdrdrvDriverReconciler) CreateKmmDockerfile(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, cudaDrvVer string, owner *metav1.OwnerReference) (requeue bool, err error) {
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-gdrdrv-kmm-dockerfile-cudadrv-%s", efa.Name, cudaDrvVer), Namespace: efa.Spec.KmmNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.CreateGdrdrvKmmDockerfile", obj)
	err = r.Get(ctx, obj, &v1.ConfigMap{})
	if err == nil {
		return false, nil
	} else if !errors.IsNotFound(err) {
		l.Error(err, "Get", "ConfigMap", obj)
		return false, err
	}
	dockerStr := fmt.Sprintf(`ARG DTK_AUTO
 FROM ${DTK_AUTO} as builder
 ARG KERNEL_VERSION
 RUN dnf install -y gcc && wget -q https://github.com/NVIDIA/open-gpu-kernel-modules/archive/refs/tags/%s.tar.gz -O /tmp/nvidia-driver.tar.gz && tar -xf /tmp/nvidia-driver.tar.gz -C /tmp && \
 wget -q https://github.com/NVIDIA/gdrcopy/archive/refs/tags/v%s.tar.gz -O /tmp/gdrcopy.tar.gz && tar -xf /tmp/gdrcopy.tar.gz -C /tmp && \
 cd /tmp/gdrcopy-%s && make NVIDIA_SRC_DIR=/tmp/open-gpu-kernel-modules-%s/kernel-open/nvidia driver && cp /tmp/gdrcopy-%s/src/gdrdrv/gdrdrv.ko / && rm -rf /tmp/gdrcopy-%s /tmp/open-gpu-kernel-modules-%s
 
 FROM registry.redhat.io/ubi8/ubi-minimal
 ARG KERNEL_VERSION
 RUN microdnf install kmod
 RUN mkdir -p /opt/lib/modules/${KERNEL_VERSION}
 COPY --from=builder /gdrdrv.ko /opt/lib/modules/${KERNEL_VERSION}
 RUN echo -e '#!/bin/sh\nif [ $# -gt 1 -a "$1" = "-r" ]; then\nif ! /sbin/modprobe $@; then\n/sbin/modprobe -d /opt $@\nfi\nelse\n/sbin/modprobe $@\nfi' > /usr/local/sbin/modprobe  && chmod 755 /usr/local/sbin/modprobe
 RUN depmod -b /opt ${KERNEL_VERSION}`,
		cudaDrvVer, efa.Spec.GdrdrvVer, efa.Spec.GdrdrvVer, cudaDrvVer, efa.Spec.GdrdrvVer, efa.Spec.GdrdrvVer, cudaDrvVer)

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: obj.Name, Namespace: obj.Namespace, OwnerReferences: []metav1.OwnerReference{*owner}, Labels: map[string]string{labelKey: efa.Name, "modName": "gdrdrv"}},
		Data:       map[string]string{"dockerfile": dockerStr},
	}
	if err := r.Create(ctx, cm, &client.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		l.Error(err, "Create", "ConfigMap", obj)
		return false, err
	}
	l.Info("Create", "ConfigMap", obj)
	return false, nil
}

func (r *GdrdrvDriverReconciler) CreateKmm(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, cudaDrvVer string, kernelVers map[string]bool, owner *metav1.OwnerReference) (requeue bool, err error) {
	modName := "gdrdrv"
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s-cudadrv-%s", efa.Name, modName, cudaDrvVer), Namespace: efa.Spec.KmmNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.CreateKmm", obj)
	err = r.Get(ctx, obj, &kmmv1beta1.Module{})
	if err == nil {
		return false, nil
	} else if !errors.IsNotFound(err) {
		l.Error(err, "Get", "KMM", obj)
		return false, err
	}
	nodeSelector := make(map[string]string)
	for key, value := range efa.Spec.NodeSelector {
		nodeSelector[key] = value
	}
	nodeSelector["nvidia.com/gpu.present"] = "true"
	drvSplit := strings.Split(cudaDrvVer, ".")
	if len(drvSplit) > 0 {
		nodeSelector["nvidia.com/cuda.driver.major"] = drvSplit[0]
	}
	if len(drvSplit) > 1 {
		nodeSelector["nvidia.com/cuda.driver.minor"] = drvSplit[1]
	}
	if len(drvSplit) > 2 {
		nodeSelector["nvidia.com/cuda.driver.rev"] = drvSplit[2]
	}
	kernelMappings := make([]kmmv1beta1.KernelMapping, 0)
	for kernelVer := range kernelVers {
		kernelVerSplit := strings.Split(kernelVer, ".")
		kernelRegexp := "^"
		if len(kernelVerSplit) > 0 {
			kernelRegexp += kernelVerSplit[0]
		}
		if len(kernelVerSplit) > 1 {
			kernelRegexp += "\\." + kernelVerSplit[1]
		}
		kernelRegexp += ".*\\.x86_64$"
		kernelMappings = append(kernelMappings, kmmv1beta1.KernelMapping{
			Regexp:         kernelRegexp,
			ContainerImage: fmt.Sprintf("image-registry.openshift-image-registry.svc:5000/openshift-kmm/%s-%s-kmm:linux-%s-cudadrv-%s", efa.Name, modName, kernelVer, cudaDrvVer),
		})
	}
	kmm := &kmmv1beta1.Module{
		Spec: kmmv1beta1.ModuleSpec{
			ModuleLoader: kmmv1beta1.ModuleLoaderSpec{
				Container: kmmv1beta1.ModuleLoaderContainerSpec{
					InTreeModuleToRemove: modName,
					Modprobe:             kmmv1beta1.ModprobeSpec{ModuleName: modName},
					KernelMappings:       kernelMappings,
					Build: &kmmv1beta1.Build{
						DockerfileConfigMap: &v1.LocalObjectReference{
							Name: fmt.Sprintf("%s-%s-kmm-dockerfile-cudadrv-%s", efa.Name, modName, cudaDrvVer),
						},
					},
				},
			},
			Selector: nodeSelector,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: obj.Name, Namespace: obj.Namespace, OwnerReferences: []metav1.OwnerReference{*owner}, Labels: map[string]string{labelKey: efa.Name, "modName": modName},
		},
	}
	if err := r.Create(ctx, kmm, &client.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		l.Error(err, "Create", "KMM", obj)
		return false, err
	}
	l.Info("Create", "KMM", obj)
	return false, nil
}

func (r *GdrdrvDriverReconciler) CreateDevicePlugin(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, owner *metav1.OwnerReference) (requeue bool, err error) {
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-gdrdrv-device-plugin", efa.Name), Namespace: r.OperatorNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.CreateDevicePlugin", obj)
	err = r.Get(ctx, obj, &appsv1.DaemonSet{})
	if err == nil {
		return false, nil
	} else if !errors.IsNotFound(err) {
		l.Error(err, "Get", "DaemonSet", obj)
		return false, err
	}
	labels := map[string]string{labelKey: obj.Name}
	secrets := make([]v1.LocalObjectReference, 0)
	for _, secret := range efa.Spec.ImagePullSecrets {
		secrets = append(secrets, v1.LocalObjectReference{Name: secret})
	}
	dir := v1.HostPathDirectory
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: obj.Name, Namespace: obj.Namespace, OwnerReferences: []metav1.OwnerReference{*owner}, Labels: map[string]string{labelKey: efa.Name}},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ImagePullSecrets:   secrets,
					ServiceAccountName: efa.Spec.DevicePluginServiceAccount,
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{MatchExpressions: []v1.NodeSelectorRequirement{{Key: "nvidia.com/gpu.present", Operator: "In", Values: []string{"true"}}}},
								},
							},
						},
					},
					NodeSelector: efa.Spec.NodeSelector,
					Tolerations: []v1.Toleration{{
						Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule",
					}},
					Containers: []v1.Container{{
						Name:            "device-plugin",
						Image:           efa.Spec.GdrdrvDevicePluginImage,
						ImagePullPolicy: v1.PullAlways,
						Command:         []string{"/usr/local/bin/ocp-gdrdrv-device-plugin"},
						SecurityContext: &v1.SecurityContext{
							Privileged:   pointer.Bool(true),
							Capabilities: &v1.Capabilities{Add: []v1.Capability{"MKNOD"}},
							RunAsUser:    pointer.Int64(0),
						},
						VolumeMounts: []v1.VolumeMount{
							{Name: "dev", MountPath: "/dev"},
							{Name: "plugins-dir", MountPath: pluginapi.DevicePluginPath},
						},
					}},
					Volumes: []v1.Volume{
						{Name: "dev", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/dev", Type: &dir}}},
						{Name: "plugins-dir", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: pluginapi.DevicePluginPath, Type: &dir}}},
					},
				},
			},
		},
	}
	if err := r.Create(ctx, ds); err != nil && errors.IsAlreadyExists(err) {
		l.Error(err, "Create", "DaemonSet", obj)
		return false, err
	}
	l.Info("Create", "DaemonSet", obj)
	return false, nil
}

func (r *GdrdrvDriverReconciler) DeleteCluster(ctx context.Context, efa *efav1alpha1.GdrdrvDriver) (err error) {
	if err = r.DeleteDevicePlugin(ctx, efa, "gdrdrv"); err != nil {
		return
	}
	if err = r.DeleteDevicePluginScc(ctx, efa); err != nil {
		return
	}
	if err = r.DeleteKmm(ctx, efa, "gdrdrv"); err != nil {
		return
	}
	if err = r.DeleteConfigMap(ctx, efa, "gdrdrv"); err != nil {
		return
	}
	return
}

func (r *GdrdrvDriverReconciler) DeleteDevicePlugin(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, modName string) (err error) {
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s-device-plugin", efa.Name, modName), Namespace: r.OperatorNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.DeleteDevicePlugin", obj)
	orig := &appsv1.DaemonSet{}
	err = r.Get(ctx, obj, orig)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		l.Error(err, "Get", "DaemonSet", obj)
		return err
	}
	err = r.Delete(ctx, orig, &client.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		l.Error(err, "Delete", "DaemonSet", obj)
		return err
	}
	l.Info("Delete", "DaemonSet", obj)
	return nil
}

func (r *GdrdrvDriverReconciler) DeleteKmm(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, modName string) (err error) {
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s", efa.Name, modName), Namespace: efa.Spec.KmmNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.DeleteKmm", obj)
	err = r.DeleteAllOf(ctx, &kmmv1beta1.Module{}, client.InNamespace(efa.Spec.KmmNamespace), client.MatchingLabels{labelKey: efa.Name, "modName": modName})
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		l.Error(err, "DeleteAllOf", "KMM", obj)
		return err
	}
	l.Info("Delete", "KMM", obj)
	return nil
}

func (r *GdrdrvDriverReconciler) DeleteConfigMap(ctx context.Context, efa *efav1alpha1.GdrdrvDriver, modName string) (err error) {
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s-kmm-dockerfile", efa.Name, modName), Namespace: efa.Spec.KmmNamespace}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.DeleteConfigMap", obj)
	err = r.DeleteAllOf(ctx, &v1.ConfigMap{}, client.InNamespace(efa.Spec.KmmNamespace), client.MatchingLabels{labelKey: efa.Name, "modName": modName})
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		l.Error(err, "Get", "ConfigMap", obj)
		return err
	}
	l.Info("Delete", "ConfigMap", obj)
	return nil
}

func (r *GdrdrvDriverReconciler) DeleteDevicePluginScc(ctx context.Context, efa *efav1alpha1.GdrdrvDriver) (err error) {
	if efa.Spec.OpenShift == nil || !*efa.Spec.OpenShift {
		return nil
	}
	obj := client.ObjectKey{Name: fmt.Sprintf("%s-%s", efa.Name, efa.Spec.DevicePluginServiceAccount)}
	l := log.FromContext(ctx).WithValues("GdrdrvDriverReconciler.DeleteDevicePluginScc", obj)
	orig := &securityv1.SecurityContextConstraints{}
	err = r.Get(ctx, obj, orig)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		l.Error(err, "Get", "SecurityContextConstraints", obj)
		return err
	}
	err = r.Delete(ctx, orig, &client.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		l.Error(err, "Delete", "SecurityContextConstraints", obj)
		return err
	}
	l.Info("Delete", "SecurityContextConstraints", obj)
	return nil
}
