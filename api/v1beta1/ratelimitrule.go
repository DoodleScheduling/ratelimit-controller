package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type RateLimitRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RateLimitRuleSpec `json:"spec,omitempty"`
}

// RateLimitRuleList contains a list of RateLimitRule.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type RateLimitRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RateLimitRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RateLimitRule{}, &RateLimitRuleList{})
}

// RateLimitRuleSpec defines the desired state of RateLimitRule.
type RateLimitRuleSpec struct {
	Domain string `json:"domain"`
	// +kubebuilder:validation:Enum=second;minute;hour;day;month;year
	Unit            string                   `json:"unit,omitempty"`
	RequestsPerUnit uint32                   `json:"requestsPerUnit,omitempty"`
	Unlimited       bool                     `json:"unlimited,omitempty"`
	Replaces        []LocalResourceReference `json:"replaces,omitempty"`
	Descriptors     []Descriptor             `json:"descriptors"`
	ShadowMode      bool                     `json:"shadowMode,omitempty"`
	DetailedMetric  bool                     `json:"detailedMetric,omitempty"`
}

type Descriptor struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

type LocalResourceReference struct {
	Name string `json:"name"`
}
