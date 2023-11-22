/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// GdrdrvSpec defines the desired state of Gdrdrv
type GdrdrvDriverSpec struct {
	//+kubebuilder:default="openshift-kmm"
	KmmNamespace string `json:"kmmNamespace,omitempty"`

	//+kubebuilder:default="2.4"
	GdrdrvVer string `json:"gdrdrvVer,omitempty"`

	//+kubebuilder:default="ocp-efa-operator-gdrdrv-device-plugin-sa"
	DevicePluginServiceAccount string `json:"devicePluginServiceAccount,omitempty"`

	//+kubebuilder:default=true
	OpenShift *bool `json:"openShift,omitempty"`

	//+kubebuilder:default={}
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	//+kubebuilder:default="ghcr.io/foundation-model-stack/ocp-gdrdrv-device-plugin:v0.0.1"
	GdrdrvDevicePluginImage string `json:"gdrdrvDevicePluginImage,omitempty"`

	//+kubebuilder:default={}
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
}

// GdrdrvStatus defines the observed state of Gdrdrv
type GdrdrvDriverStatus struct {
	Condition string `json:"condition,omitempty"`

	Description string `json:"description,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.condition`

// Gdrdrv is the Schema for the gdrdrvs API
type GdrdrvDriver struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GdrdrvDriverSpec   `json:"spec,omitempty"`
	Status GdrdrvDriverStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// GdrdrvDriverList contains a list of Gdrdrv
type GdrdrvDriverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GdrdrvDriver `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GdrdrvDriver{}, &GdrdrvDriverList{})
}
