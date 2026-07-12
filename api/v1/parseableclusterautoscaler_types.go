/*
Copyright (c) 2024-2026 Parseable, Inc.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ParseableClusterAutoscalerSpec defines the desired state of the ParseableClusterAutoscaler.
type ParseableClusterAutoscalerSpec struct {
	// stop reconcilation
	Stop bool `json:"stop,omitempty"`
	// ScaleTargetRef specifies the target resource that the autoscaler should manage.
	ScaleTargetRef ScaleTargetRef `json:"scaleTargetRef"`

	// ScalingConfig contains the configuration for scaling behavior.
	ScalingConfig []ScalingConfig `json:"scalingConfig"`
}

// ScaleTargetRef references the target Kubernetes resource that the autoscaler will scale.
type ScaleTargetRef struct {
	// ApiVersion is the version of the API to use with the target resource.
	ApiVersion string `json:"apiVersion"`

	// Kind is the type of resource being referenced (e.g., Deployment, StatefulSet).
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	Name string `json:"name"`
}

// ScalingConfig defines the scaling behavior for a specific node type or set of nodes.
type ScalingConfig struct {
	// NodeType is the type of node to be scaled (e.g., worker, master).
	NodeType NodeType `json:"nodeType"`

	// SelectorLabels are the labels used to identify the nodes to scale.
	SelectorLabels map[string]string `json:"selectorLabels"`

	// MinReplicas is the minimum number of replicas the autoscaler should maintain.
	MinReplicas int32 `json:"minReplicas"`

	// MaxReplicas is the maximum number of replicas the autoscaler can scale to.
	MaxReplicas int32 `json:"maxReplicas"`

	// Threshold is the metric threshold that triggers scaling.
	Threshold float64 `json:"threshold"`

	// Behavior defines the specific behavior for scaling up and down.
	Behavior Behavior `json:"behavior"`

	// StabilizationWindowSeconds is the time window for stabilizing scaling up/down actions.
	StabilizationWindowSeconds int64 `json:"stabilizationWindowSeconds"`
}

// Behavior defines the scaling behavior for both scaling up and down.
type Behavior struct {
	// ScaleDown defines the behavior for scaling down (reducing replicas).
	ScaleDown ScaleDown `json:"scaleDown"`

	// ScaleUp defines the behavior for scaling up (increasing replicas).
	ScaleUp ScaleUp `json:"scaleUp"`
}

// ScaleDown defines the configuration for scaling down (reducing replicas).
type ScaleDown struct {
	// Policies is an optional set of policies that further define scale down behavior.
	Policies []Policies `json:"policies,omitempty"`
}

// ScaleUp defines the configuration for scaling up (increasing replicas).
type ScaleUp struct {
	// Policies is an optional set of policies that further define scale up behavior.
	Policies []Policies `json:"policies,omitempty"`
}

// Policies define specific rules for scaling actions, such as scaling by percentage or absolute number.
type Policies struct {
	// Type specifies the type of scaling (e.g., Percentage, Absolute).
	Type K8sObjectType `json:"type"`

	// Value is the value associated with the scaling type (e.g., 10% or 5 replicas).
	Value int32 `json:"value"`
}

// ParseableClusterAutoscalerStatus defines the observed state of the ParseableClusterAutoscaler.
type ParseableClusterAutoscalerStatus struct {
	IngestorIndex          map[string]string `json:"ingestorIndex,omitempty"`
	LastAddSTSTimeStamp    metav1.Time       `json:"lastAddStsTimeStamp,omitempty"`
	LastRemoveSTSTimeStamp metav1.Time       `json:"lastRemoveStsTimeStamp,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ParseableClusterAutoscaler is the Schema for the parseableclusterautoscalers API.
// It represents a custom resource for managing autoscaling behavior in a Kubernetes cluster.
type ParseableClusterAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired behavior and configuration of the autoscaler.
	Spec ParseableClusterAutoscalerSpec `json:"spec,omitempty"`

	// Status reflects the current observed state of the autoscaler.
	Status ParseableClusterAutoscalerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// ParseableClusterAutoscalerList contains a list of ParseableClusterAutoscaler resources.
type ParseableClusterAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ParseableClusterAutoscaler resources.
	Items []ParseableClusterAutoscaler `json:"items"`
}

func init() {
	// Register the ParseableClusterAutoscaler and ParseableClusterAutoscalerList with the scheme.
	SchemeBuilder.Register(&ParseableClusterAutoscaler{}, &ParseableClusterAutoscalerList{})
}
