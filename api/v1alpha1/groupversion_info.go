// Package v1alpha1 contains API Schema definitions for the lognormalizer v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=lognormalizer.opendatahub.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{Group: "lognormalizer.opendatahub.io", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion} //nolint:staticcheck // standard kubebuilder pattern

	AddToScheme = SchemeBuilder.AddToScheme
)
