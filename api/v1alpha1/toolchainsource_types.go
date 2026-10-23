// Package v1alpha1 is the toolchain-manager's API: a ToolchainSource is a
// repository a person added as a source of toolchains, waiting for or holding
// the installation owner's approval; a ToolchainDeprecation takes a toolchain,
// or one of its versions, out of the offered list.
//
// +kubebuilder:object:generate=true
// +groupName=toolchain-manager.giantswarm.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API's group and version.
var GroupVersion = schema.GroupVersion{Group: "toolchain-manager.giantswarm.io", Version: "v1alpha1"}

// AddToScheme registers the API's types.
func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ToolchainSource{}, &ToolchainSourceList{}, &ToolchainDeprecation{}, &ToolchainDeprecationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

// Phase is where a source stands in its review.
type Phase string

const (
	// PhasePending is a source waiting for the installation owner's approval:
	// its toolchains are not offered.
	PhasePending Phase = "Pending"
	// PhaseApproved is a source the installation owner approved: its
	// contract-checked toolchains are offered.
	PhaseApproved Phase = "Approved"
)

// ToolchainSourceSpec is what the person who added the source named.
type ToolchainSourceSpec struct {
	// Repository is the source's GitHub repository, owner/name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="repository is immutable"
	Repository string `json:"repository"`
	// URL is the repository's web address.
	URL string `json:"url,omitempty"`
	// Registry is the image repository prefix the source's toolchains
	// publish under, <registry>/<name>.
	Registry string `json:"registry"`
	// SlackChannel is the owning team's channel that approves the source's
	// updates: its ID (C0123ABCDE), which the asks are posted to, or its
	// name.
	SlackChannel string `json:"slackChannel"`
	// Team is the owning GitHub team's slug, whose members approve a major
	// update in SlackChannel.
	// +optional
	Team string `json:"team,omitempty"`
	// AddedBy is the person who added the source: their verified email,
	// else their subject.
	AddedBy string `json:"addedBy"`
	// AddedBySubject is the identity provider's subject of the person who
	// added the source, which their removal rights rest on.
	AddedBySubject string `json:"addedBySubject"`
}

// ToolchainSourceStatus is the installation owner's review.
type ToolchainSourceStatus struct {
	// Phase is Pending until the installation owner approves the source.
	// +kubebuilder:validation:Enum=Pending;Approved
	Phase Phase `json:"phase,omitempty"`
	// ApprovedBy is the owner who approved the source.
	ApprovedBy string `json:"approvedBy,omitempty"`
	// ApprovedAt is when.
	ApprovedAt *metav1.Time `json:"approvedAt,omitempty"`
}

// ToolchainSource is a repository added as a source of toolchains.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tcs
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Added by",type=string,JSONPath=`.spec.addedBy`
// +kubebuilder:printcolumn:name="Approved by",type=string,JSONPath=`.status.approvedBy`
type ToolchainSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ToolchainSourceSpec   `json:"spec"`
	Status ToolchainSourceStatus `json:"status,omitempty"`
}

// ToolchainSourceList is a list of ToolchainSources.
//
// +kubebuilder:object:root=true
type ToolchainSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ToolchainSource `json:"items"`
}
