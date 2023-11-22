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

// EfaDriverSpec defines the desired state of EfaDriver
type EfaDriverSpec struct {
	//+kubebuilder:default="openshift-kmm"
	KmmNamespace string `json:"kmmNamespace,omitempty"`

	//+kubebuilder:default="1.26.1"
	AwsEfaInstallerVer string `json:"awsEfaInstallerVer,omitempty"`

	//+kubebuilder:default="ocp-efa-operator-efa-device-plugin-sa"
	DevicePluginServiceAccount string `json:"devicePluginServiceAccount,omitempty"`

	//+kubebuilder:default=true
	OpenShift *bool `json:"openShift,omitempty"`

	//+kubebuilder:default={}
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	//+kubebuilder:default="ghcr.io/foundation-model-stack/ocp-efa-device-plugin:v0.0.1"
	EfaDevicePluginImage string `json:"efaDevicePluginImage,omitempty"`
	//+kubebuilder:default={}
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
}

// EfaDriverStatus defines the observed state of EfaDriver
type EfaDriverStatus struct {
	Condition string `json:"condition,omitempty"`

	Description string `json:"description,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.condition`

// EfaDriver is the Schema for the efadrivers API
type EfaDriver struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EfaDriverSpec   `json:"spec,omitempty"`
	Status EfaDriverStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EfaDriverList contains a list of EfaDriver
type EfaDriverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EfaDriver `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EfaDriver{}, &EfaDriverList{})
}
