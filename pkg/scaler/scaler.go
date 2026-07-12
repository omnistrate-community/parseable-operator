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

package scaler

import (
	"context"
	"fmt"
	v1 "parseablehq/parseable-operator/api/v1"
	"parseablehq/parseable-operator/pkg/utils"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pbcAsSTSAddFail                  = "ParseableClusterAutoscalerSTSAddFail"
	pbcAsSTSAddSuccess               = "ParseableClusterAutoscalerSTSAddSuccess"
	pbcAsSTSDeleteFail               = "ParseableClusterAutoscalerDeleteFail"
	pbcAsStatusPatchFail             = "ParseableClusterAutoscalerPatchFail"
	pbcAsSTSDeleteSuccess            = "ParseableClusterAutoscalerDeleteSuccess"
	pbcAsSTSRemoveServiceSuccess     = "ParseableClusterAutoscalerRemoveServiceSuccess"
	pbcAsWaitForStabalisationWindown = "ParseableClusterAutoscalerWaitStabalisationWindow"
	pbcAsSTSRemoveFail               = "ParseableClusterAutoscalerRemoveFail"
	pvcAsServicePatchFail            = "ParseableClusterAutoscalerServicePatchFail"
	pvcAsServicePatchSuccess         = "ParseableClusterAutoscalerServicePatchSuccess"
	pbcAsPodTrafficStop              = "ParseableClusterAutoscalerStopPodTraffic"
	pbcAsServiceUpdateFail           = "ParseableClusterAutoscalerServiceUpdateFail"
	pbcAsServiceUpdateSuccess        = "ParseableClusterAutoscalerUpdateSuccess"
)

type ScalingClient interface {
	AddStatefulSet(ctx context.Context, replicas *int32) (string, *appsv1.StatefulSet, error)
	UpdateServiceSelector(ctx context.Context, sts *appsv1.StatefulSet) (string, error)
	RemoveStatefulSet(ctx context.Context) (string, error)
	GetLatestSTS(ctx context.Context) (*appsv1.StatefulSet, error)
}

type ScalingInputs struct {
	K8sClient     client.Client
	Pbcas         *v1.ParseableClusterAutoscaler
	ScalingConfig *v1.ScalingConfig
	ScaleEvent    ScaleEvent
}

func NewScalingInputs(
	k8sClient client.Client,
	pbcas *v1.ParseableClusterAutoscaler,
	scalingConfig *v1.ScalingConfig,
	scaleEvent ScaleEvent,
) ScalingClient {
	return &ScalingInputs{
		K8sClient:     k8sClient,
		Pbcas:         pbcas,
		ScalingConfig: scalingConfig,
		ScaleEvent:    scaleEvent,
	}
}

// AddStatefulSet adds a new StatefulSet based on the latest StatefulSet found.
// It checks if the StatefulSet already exists before creating it.
func (s *ScalingInputs) AddStatefulSet(ctx context.Context, replicas *int32) (string, *appsv1.StatefulSet, error) {
	// Retrieve the latest StatefulSet
	latestSTS, err := s.GetLatestSTS(ctx)
	if err != nil {
		return pbcAsSTSAddFail, nil, err
	}

	// Mutate the latest StatefulSet with the new configuration
	mutateSTS, err := s.mutateSTS(latestSTS, replicas)
	if err != nil {
		return pbcAsSTSAddFail, nil, err
	}

	// Attempt to fetch the StatefulSet to see if it already exists
	err = s.K8sClient.Get(ctx, types.NamespacedName{Name: mutateSTS.Name, Namespace: s.Pbcas.Namespace}, mutateSTS)
	if err != nil && apierrors.IsNotFound(err) {

		// Create the new StatefulSet if it doesn't exist
		err = s.K8sClient.Create(ctx, mutateSTS)
		if err != nil {
			return pbcAsSTSAddFail, nil, err
		}

		return pbcAsSTSAddSuccess, mutateSTS, nil
	} else if err != nil {
		return pbcAsSTSAddFail, nil, err
	}

	// If the StatefulSet already exists or another error occurred, do nothing
	return pbcAsSTSAddFail, nil, nil
}

// RemoveStatefulSet deletes the oldest StatefulSet found in the cluster.
// It also records appropriate events for success and failure cases.
func (s *ScalingInputs) RemoveStatefulSet(ctx context.Context) (string, error) {
	// Retrieve the oldest StatefulSet
	oldSTS, err := s.getOldestSTS(ctx)
	if err != nil {
		return pbcAsSTSRemoveFail, err
	}

	// Delete the oldest StatefulSet with the 'Orphan' propagation policy
	err = s.K8sClient.Delete(ctx, oldSTS, client.PropagationPolicy(metav1.DeletePropagationOrphan))
	if err != nil {
		// Record a warning event if deletion fails
		return pbcAsSTSDeleteFail, err
	}

	// Record a success event for the StatefulSet deletion

	// Delete the pods associated with the StatefulSet every 10 seconds
	podList := &corev1.PodList{}
	podSelector := client.MatchingLabels(oldSTS.Spec.Selector.MatchLabels)

	for {
		// List the pods associated with the StatefulSet
		err = s.K8sClient.List(ctx, podList, podSelector)
		if err != nil {
			// Log an error if listing the pods fails
			return pbcAsSTSDeleteFail, err
		}

		if len(podList.Items) == 0 {
			// Break the loop if no more pods are found
			break
		}

		for _, pod := range podList.Items {
			// Delete each pod
			err = s.K8sClient.Delete(ctx, &pod)
			if err != nil {
				// Log an error if pod deletion fails
				return pbcAsSTSDeleteFail, err
			}

			// Record a success event for the pod deletion
			// Wait for 10 seconds before deleting the next pod
			time.Sleep(30 * time.Second)
		}
	}

	return pbcAsSTSDeleteSuccess, nil
}

// getLatestSTS retrieves the latest StatefulSet in the cluster based on the CreationTimestamp.
func (s *ScalingInputs) GetLatestSTS(ctx context.Context) (*appsv1.StatefulSet, error) {
	stsList := &appsv1.StatefulSetList{}
	stsListOpts := []client.ListOption{
		client.InNamespace(s.Pbcas.Namespace),
		client.MatchingLabels(s.ScalingConfig.SelectorLabels),
	}

	// List all StatefulSets in the namespace with the specified labels
	err := s.K8sClient.List(ctx, stsList, stsListOpts...)
	if err != nil {
		return nil, err
	}

	if len(stsList.Items) == 0 {
		return nil, fmt.Errorf("No StatefulSets found")
	}

	// Find the StatefulSet with the latest CreationTimestamp
	latestSTS := &stsList.Items[0]
	for _, sts := range stsList.Items {
		if sts.CreationTimestamp.Time.After(latestSTS.CreationTimestamp.Time) {
			latestSTS = &sts
		}
	}

	// Return a deep copy of the latest StatefulSet
	return latestSTS.DeepCopy(), nil
}

// mutateSTS creates a new StatefulSet configuration based on an existing StatefulSet.
func (s *ScalingInputs) mutateSTS(sts *appsv1.StatefulSet, replicas *int32) (*appsv1.StatefulSet, error) {

	// Generate a new name for the StatefulSet
	generateName, err := utils.MutateString(sts.Name)
	if err != nil {
		return nil, err
	}

	// Merge labels to include the autoscaler marker
	labels := utils.MergeLabels(sts.Labels, map[string]string{
		"parseable_autoscaler": "true",
	})

	var newReplicaCount int32

	if s.ScaleEvent == ScaleUp {
		newReplicaCount = *sts.Spec.Replicas + *replicas
	} else if s.ScaleEvent == ScaleDown {
		if *sts.Spec.Replicas > *replicas {
			newReplicaCount = *sts.Spec.Replicas - *replicas
		}
	}

	// Clear the ResourceVersion and update the StatefulSet fields
	sts.ResourceVersion = ""
	sts.Name = generateName
	sts.Spec.Replicas = &newReplicaCount
	sts.Spec.Template.Labels = labels
	sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	sts.Labels = labels
	sts.Labels["sts_name"] = generateName

	// Return the mutated StatefulSet
	return sts, nil
}

// UpdateServiceSelector updates the selector of a service with specific labels to point to the provided StatefulSet.
func (s *ScalingInputs) UpdateServiceSelector(ctx context.Context, sts *appsv1.StatefulSet) (string, error) {
	// Define the label selector for the service
	serviceList := &corev1.ServiceList{}
	serviceListOpts := []client.ListOption{
		client.InNamespace(s.Pbcas.Namespace),
		client.MatchingLabels{
			"pbc_nodetype": "ingestor",
		},
	}

	// List services with the matching labels
	err := s.K8sClient.List(ctx, serviceList, serviceListOpts...)
	if err != nil {
		return pbcAsSTSRemoveFail, err
	}

	// Loop through each service and update the selector
	for _, svc := range serviceList.Items {
		fmt.Println(svc.Name)
		updatedSvc := svc.DeepCopy()
		updatedSvc.Spec.Selector = map[string]string{
			"sts_name": sts.Name,
		}

		// Attempt to update the service selector
		err = s.K8sClient.Update(ctx, updatedSvc)
		if err != nil {
			// Record a warning event if the update fails
			return pbcAsServiceUpdateFail, err
		}

	}

	return pbcAsServiceUpdateSuccess, nil
}

// getOldestSTS retrieves the oldest StatefulSet in the cluster based on the CreationTimestamp.
func (s *ScalingInputs) getOldestSTS(ctx context.Context) (*appsv1.StatefulSet, error) {
	stsList := &appsv1.StatefulSetList{}
	stsListOpts := []client.ListOption{
		client.InNamespace(s.Pbcas.Namespace),
		client.MatchingLabels(s.ScalingConfig.SelectorLabels),
	}

	// List all StatefulSets in the namespace with the specified labels
	err := s.K8sClient.List(ctx, stsList, stsListOpts...)
	if err != nil {
		return nil, err
	}

	if len(stsList.Items) == 0 {
		return nil, fmt.Errorf("No StatefulSets found")
	}

	// Find the StatefulSet with the earliest CreationTimestamp
	oldestSTS := &stsList.Items[0]
	for _, sts := range stsList.Items {
		if sts.CreationTimestamp.Time.Before(oldestSTS.CreationTimestamp.Time) {
			oldestSTS = &sts
		}
	}

	// Return a deep copy of the oldest StatefulSet
	return oldestSTS.DeepCopy(), nil
}
