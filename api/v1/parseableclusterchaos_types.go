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

type ParseableClusterChaosSpec struct {
	NodeSelectorLabels map[string]string `json:"nodeSelectorLabels"`
	CPUUsage           float64           `json:"cpuUsage"`
	Age                int64             `json:"age"`
}

type ParseableClusterChaosStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type ParseableClusterChaos struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired behavior and configuration of the autoscaler.
	Spec ParseableClusterChaosSpec `json:"spec,omitempty"`

	// Status reflects the current observed state of the autoscaler.
	Status ParseableClusterChaosStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ParseableClusterChaosList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ParseableClusterAutoscaler resources.
	Items []ParseableClusterChaos `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ParseableClusterChaos{}, &ParseableClusterChaosList{})
}
