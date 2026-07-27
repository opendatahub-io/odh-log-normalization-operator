package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DrainConfig configures the Drain log template mining algorithm.
type DrainConfig struct {
	// +kubebuilder:default=0.4
	SimilarityThreshold float64 `json:"similarityThreshold,omitempty"`
	// +kubebuilder:default=1000
	MaxClusters int `json:"maxClusters,omitempty"`
	// +kubebuilder:default=4
	TreeDepth int `json:"treeDepth,omitempty"`
}

// MultilineConfig configures multiline log aggregation.
type MultilineConfig struct {
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
	// +kubebuilder:default=100
	MaxLines int `json:"maxLines,omitempty"`
	// +kubebuilder:default=2000
	FlushTimeoutMs int `json:"flushTimeoutMs,omitempty"`
}

// LogNormalizerConfigSpec defines the desired state of LogNormalizerConfig.
type LogNormalizerConfigSpec struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`

	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// +kubebuilder:default="quay.io/opendatahub/log-normalizer:latest"
	SidecarImage string `json:"sidecarImage,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:validation:Enum=json;otel;logfmt
	// +kubebuilder:default="json"
	OutputFormat string `json:"outputFormat,omitempty"`

	Drain DrainConfig `json:"drain,omitempty"`

	Multiline MultilineConfig `json:"multiline,omitempty"`

	// +kubebuilder:validation:items:Enum=go;java;python
	// +optional
	Consolidators []string `json:"consolidators,omitempty"`

	// +optional
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
}

// LogNormalizerConfigStatus defines the observed state of LogNormalizerConfig.
type LogNormalizerConfigStatus struct {
	Phase            string             `json:"phase,omitempty"`
	InjectedPods     int32              `json:"injectedPods,omitempty"`
	TemplatesLearned int32              `json:"templatesLearned,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Injected Pods",type=integer,JSONPath=`.status.injectedPods`

// LogNormalizerConfig is the Schema for the lognormalizerconfigs API.
type LogNormalizerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LogNormalizerConfigSpec   `json:"spec,omitempty"`
	Status LogNormalizerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LogNormalizerConfigList contains a list of LogNormalizerConfig.
type LogNormalizerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LogNormalizerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LogNormalizerConfig{}, &LogNormalizerConfigList{})
}
