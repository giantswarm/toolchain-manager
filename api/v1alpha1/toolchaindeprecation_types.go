package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ToolchainDeprecationSpec names a deprecated toolchain of a source, or one
// of its versions. A deprecated version leaves the offered list; its release
// and image stay, since a suspended Session resumes only on the exact digest
// it started with.
type ToolchainDeprecationSpec struct {
	// Repository is the source's GitHub repository, owner/name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="repository is immutable"
	Repository string `json:"repository"`
	// Toolchain is the toolchain's name.
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]*$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="toolchain is immutable"
	Toolchain string `json:"toolchain"`
	// Version is the deprecated version, vX.Y.Z; empty deprecates the
	// toolchain with every version.
	// +kubebuilder:validation:Pattern=`^(v[0-9]+\.[0-9]+\.[0-9]+)?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="version is immutable"
	Version string `json:"version,omitempty"`
	// Reason says why, for the people whose agents name it.
	Reason string `json:"reason,omitempty"`
	// DeprecatedBy is the person who deprecated it: their verified email,
	// else their subject.
	DeprecatedBy string `json:"deprecatedBy"`
}

// ToolchainDeprecation is a toolchain, or a version of it, taken out of the
// offered list.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=tcd
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="Toolchain",type=string,JSONPath=`.spec.toolchain`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Deprecated by",type=string,JSONPath=`.spec.deprecatedBy`
type ToolchainDeprecation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ToolchainDeprecationSpec `json:"spec"`
}

// ToolchainDeprecationList is a list of ToolchainDeprecations.
//
// +kubebuilder:object:root=true
type ToolchainDeprecationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ToolchainDeprecation `json:"items"`
}
