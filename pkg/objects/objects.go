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

package controller

import (
	v1 "parseablehq/parseable-operator/api/v1"
	"parseablehq/parseable-operator/pkg/utils"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Environment variable constants for the Parseable server
const (
	PAddr        = "P_ADDR"
	PUsername    = "P_USERNAME"
	PPassword    = "P_PASSWORD"
	PStagingDir  = "P_STAGING_DIR"
	PFsDir       = "P_FS_DIR"
	PS3URL       = "P_S3_URL"
	PS3AccessKey = "P_S3_ACCESS_KEY"
	PS3SecretKey = "P_S3_SECRET_KEY"
	PS3Bucket    = "P_S3_BUCKET"
	PS3Region    = "P_S3_REGION"
	PMode        = "P_MODE"
)

// makeOwnerRef creates an owner reference, typically used in controllers
// to establish a relationship between the owner (controller) and the object.
func MakeOwnerRef(apiVersion, kind, name string, uid types.UID) *metav1.OwnerReference {
	controller := true

	return &metav1.OwnerReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		UID:        uid,
		Controller: &controller,
	}
}

// makeStatefulSet creates a StatefulSet object based on the provided specifications.
func MakeStatefulSet(
	pbc *v1.ParseableCluster,
	nodeName string,
	replicas int32,
	nodeSpec *v1.NodeSpec,
	k8sconfig *v1.K8sConfigSpec,
	pbcConfig *v1.ParseableConfigSpec,
	secret *corev1.Secret,
) *appsv1.StatefulSet {

	// Get the service name for the headless service
	svcName := getHeadlessSvcName(k8sconfig.Service)

	// Create environment variables for the StatefulSet
	envs := makeEnv(secret, k8sconfig.Env)

	tgs := int64(120)
	// Construct and return the StatefulSet object
	sts := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: pbc.Namespace,
			Labels:    MakeLabels(k8sconfig.Labels, pbc, nodeName, k8sconfig.Name, nodeSpec.Type),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: MakeLabels(k8sconfig.Labels, pbc, nodeName, k8sconfig.Name, nodeSpec.Type),
			},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			},
			ServiceName: svcName,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes:                       k8sconfig.Volume,
					ServiceAccountName:            k8sconfig.ServiceAccountName,
					Tolerations:                   k8sconfig.Tolerations,
					NodeSelector:                  k8sconfig.NodeSelector,
					ImagePullSecrets:              k8sconfig.ImagePullSecrets,
					Affinity:                      &k8sconfig.Affinity,
					TerminationGracePeriodSeconds: &tgs,
					InitContainers:                k8sconfig.InitContainers,
					Containers: []corev1.Container{
						{
							Name:            k8sconfig.Name,
							Image:           k8sconfig.Image,
							ImagePullPolicy: k8sconfig.ImagePullPolicy,
							Env:             *envs,
							Args:            getCliArgs(nodeSpec.Type, pbc.Namespace, svcName),
							VolumeMounts:    k8sconfig.VolumeMount,
							Resources:       k8sconfig.Resources,
							Ports: []corev1.ContainerPort{
								{
									Name:          "ingestor",
									ContainerPort: 8000,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Labels: MakeLabels(k8sconfig.Labels, pbc, nodeName, k8sconfig.Name, nodeSpec.Type),
				},
			},
			VolumeClaimTemplates: k8sconfig.VolumeClaimTemplates,
		},
	}

	// Only set ReadinessProbe if it's defined
	if k8sconfig.ReadinessProbe != (corev1.Probe{}) {
		sts.Spec.Template.Spec.Containers[0].ReadinessProbe = &k8sconfig.ReadinessProbe
	}

	// Only set StartupProbe if it's defined
	if k8sconfig.StartupProbe != (corev1.Probe{}) {
		sts.Spec.Template.Spec.Containers[0].StartupProbe = &k8sconfig.StartupProbe
	}

	if pbc.Status.EnableAutoscaling && pbc.Status.IngestorIndex != nil && nodeSpec.Type == v1.IngestorNodeType {
		// Merge labels to include the autoscaler marker
		if pbc.Status.IngestorIndex[k8sconfig.Name] != "" {
			//fmt.Println(sts.Labels)
			labels := utils.MergeLabels(sts.Labels, map[string]string{
				"parseable_autoscaler": "true",
			})

			sts.Spec.Template.Labels = labels
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
			sts.Labels = labels
			sts.Labels["sts_name"] = pbc.Status.IngestorIndex[nodeSpec.Name]
			sts.Spec.Replicas = &replicas
		}
	}
	return sts
}

// getHeadlessSvcName returns the name of the headless service from a list of services.
func getHeadlessSvcName(services []corev1.Service) string {
	for _, svc := range services {
		if svc.Spec.ClusterIP == "None" {
			return svc.Name
		}
	}
	return ""
}

// getCliArgs generates the CLI arguments for the container based on the NodeType.
func getCliArgs(
	nodeType v1.NodeType,
	namespace,
	serviceName string,
) []string {
	switch nodeType {
	case v1.IngestorNodeType:
		return []string{
			"/usr/bin/parseable-enterprise", "s3-store", "--ingestor-endpoint=$(HOSTNAME)." + serviceName + "." + namespace + ".svc.cluster.local:8000",
		}
	case v1.QueryNodeType:
		return []string{"/usr/bin/parseable-enterprise", "s3-store", "--querier-endpoint=$(HOSTNAME)." + serviceName + "." + namespace + ".svc.cluster.local:8000"}
	case v1.PrismNodeType:
		return []string{"/usr/bin/parseable-enterprise", "s3-store"}
	default:
		return nil
	}
}

func makeEnv(secret *corev1.Secret, additionalEnvs []corev1.EnvVar) *[]corev1.EnvVar {
	// Mapping of secret keys to environment variable names
	keyToEnvVar := map[string]string{
		"addr":          PAddr,
		"username":      PUsername,
		"password":      PPassword,
		"staging.dir":   PStagingDir,
		"fs.dir":        PFsDir,
		"s3.url":        PS3URL,
		"s3.access.key": PS3AccessKey,
		"s3.secret.key": PS3SecretKey,
		"s3.bucket":     PS3Bucket,
		"s3.region":     PS3Region,
	}

	// Initialize environment variables from the secret if the key exists
	var envVars []corev1.EnvVar
	for secretKey, envVarName := range keyToEnvVar {
		// Only add env variable if the key exists in the secret
		if value, exists := secret.Data[secretKey]; exists && len(value) > 0 {
			envVars = append(envVars, corev1.EnvVar{
				Name: envVarName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: secretKey,
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secret.ObjectMeta.Name, // Use the secret's name
						},
					},
				},
			})
		}
	}

	// Append any additional environment variables provided
	envVars = append(envVars, additionalEnvs...)

	// Sort the environment variables by their names
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	return &envVars
}

func MakeService(
	svc corev1.Service,
	pbc *v1.ParseableCluster,
	stsName string,
	nodeType v1.NodeType,
) *corev1.Service {

	pbcSvc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: pbc.Namespace,
			Labels:    getNodeTypeLabel(nodeType),
		},
		Spec: svc.Spec,
	}

	if svc.Spec.ClusterIP == "None" {
		pbcSvc.Labels = utils.MergeLabels(pbcSvc.Labels, map[string]string{
			"headless": "true",
		})
	} else {
		pbcSvc.Labels = utils.MergeLabels(pbcSvc.Labels, map[string]string{
			"headless": "false",
		})
	}

	pbcSvc.Spec.Selector = getNodeTypeLabel(nodeType)

	return pbcSvc
}

// makeLabels merges the provided labels with a node type-specific label based on the node type.
// It returns the merged labels map.
func MakeLabels(labels map[string]string, pbc *v1.ParseableCluster, nodeName, nodeSpecNodeName string, nodeType v1.NodeType) map[string]string {

	// Merge nodeTypeLabel into labels
	for key, value := range getNodeTypeLabel(nodeType) {
		labels[key] = value
	}
	labels["app"] = "parseable-cluster"
	labels["parseable_cr"] = pbc.Name
	labels["sts_name"] = nodeName
	labels["original_sts_name"] = nodeSpecNodeName

	// Return the merged labels
	return labels
}

// getNodeTypeLabel returns a map containing the label for the given node type.
func getNodeTypeLabel(nodeType v1.NodeType) map[string]string {
	switch nodeType {
	case v1.IngestorNodeType:
		// Return label for Ingestor node type
		return map[string]string{
			"pbc_nodetype": string(v1.IngestorNodeType),
		}
	case v1.QueryNodeType:
		// Return label for Query node type
		return map[string]string{
			"pbc_nodetype": string(v1.QueryNodeType),
		}
	case v1.PrismNodeType:
		// Return label for Query node type
		return map[string]string{
			"pbc_nodetype": string(v1.PrismNodeType),
		}
	default:
		// Return an empty map if no specific node type matches
		return map[string]string{}
	}
}
