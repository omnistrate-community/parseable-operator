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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ParseableClusterSpec defines the desired state of ParseableCluster
type ParseableClusterSpec struct {
	DeploymentOrder []string              `json:"deploymentOrder"`
	K8sConfig       []K8sConfigSpec       `json:"k8sConfig"`
	ParseableConfig []ParseableConfigSpec `json:"parseableConfig"`
	Nodes           []NodeSpec            `json:"nodes"`
}

// K8sConfigSpec defines the configuration for Kubernetes resources
type K8sConfigSpec struct {
	Name                 string                     `json:"name"`
	Labels               map[string]string          `json:"labels"`
	Volume               []v1.Volume                `json:"volume,omitempty"`
	VolumeMount          []v1.VolumeMount           `json:"volumeMount,omitempty"`
	VolumeClaimTemplates []v1.PersistentVolumeClaim `json:"volumeClaimTemplate,omitempty"`
	Image                string                     `json:"image"`
	ImagePullSecrets     []v1.LocalObjectReference  `json:"imagePullSecrets,omitempty"`
	ImagePullPolicy      v1.PullPolicy              `json:"imagePullPolicy,omitempty"`
	ServiceAccountName   string                     `json:"serviceAccountName,omitempty"`
	Env                  []v1.EnvVar                `json:"env,omitempty"`
	Tolerations          []v1.Toleration            `json:"tolerations,omitempty"`
	NodeSelector         map[string]string          `json:"nodeSelector,omitempty"`
	Affinity             v1.Affinity                `json:"affinity,omitempty"`
	Service              []v1.Service               `json:"service,omitempty"`
	Resources            v1.ResourceRequirements    `json:"resources,omitempty"`
	ReadinessProbe       v1.Probe                   `json:"readinessProbe,omitempty"`
	StartupProbe         v1.Probe                   `json:"startupProbe,omitempty"`
	InitContainers       []v1.Container             `json:"initContainers,omitempty"`
}

// Metadata holds additional metadata for Kubernetes resources
type Metadata struct {
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// StorageConfig defines the storage configuration for Kubernetes resources
type StorageConfig struct {
	Name      string                       `json:"name"`
	MountPath string                       `json:"mountPath"`
	PvcSpec   v1.PersistentVolumeClaimSpec `json:"spec"`
}

// ParseableConfigSpec defines the configuration for Parseable components
type ParseableConfigSpec struct {
	Name       string            `json:"name"`
	EnvVars    map[string]string `json:"env,omitempty"`
	SecretName string            `json:"secretName,omitempty"`
	CliArgs    []string          `json:"cliArgs"`
}

// NodeSpec defines the configuration for a node in the Parseable cluster
type NodeSpec struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Type            NodeType `json:"type"`
	Replicas        *int32   `json:"replicas"`
	K8sConfig       string   `json:"k8sConfig"`
	ParseableConfig string   `json:"parseableConfig"`
}

// ParseableClusterStatus defines the observed state of ParseableCluster
type ParseableClusterStatus struct {
	EnableAutoscaling bool              `json:"enableAutoscaling,omitempty"`
	IngestorIndex     map[string]string `json:"ingestorIndex,omitempty"`
	IsScaling         bool              `json:"isScaling,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ParseableCluster is the Schema for the parseableclusters API
type ParseableCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ParseableClusterSpec   `json:"spec,omitempty"`
	Status ParseableClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ParseableClusterList contains a list of ParseableCluster
type ParseableClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ParseableCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ParseableCluster{}, &ParseableClusterList{})
}
