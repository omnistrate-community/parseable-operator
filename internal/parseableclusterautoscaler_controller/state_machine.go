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

package parseableclusterautoscalercontroller

import (
	"context"
	"fmt"
	v1 "parseablehq/parseable-operator/api/v1"
	"parseablehq/parseable-operator/pkg/scaler"
	"parseablehq/parseable-operator/pkg/utils"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Constants for event recording
const (
	pbcaStsAddFail                 = "ParseableClusterAutoscalerStatefulSetAddFail"
	pbcaStsAddSuccess              = "ParseableClusterAutoscalerStatefulSetAddSuccess"
	pbcaStsRemoveFail              = "ParseableClusterAutoscalerStatefulSetRemoveFail"
	pbcaStsRemoveSuccess           = "ParseableClusterAutoscalerStatefulSetRemoveSuccess"
	pbcaSvcUpdateFail              = "ParseableClusterAutoscalerServiceUpdateFail"
	pbcaSvcUpdateSuccess           = "ParseableClusterAutoscalerServiceUpdateSuccess"
	pbcaStabilizationWaiting       = "ParseableClusterAutoscalerStabilizationWaiting"
	pbcaStsNotAvailable            = "ParseableClusterAutoscalerStatefulSetNotAvailable"
	pbcaStabilizationWindowPassed  = "ParseableClusterAutoscalerStabilizationWindowPassed"
	pbcaStsFetchFail               = "ParseableClusterAutoscalerStatefulSetFetchFail"
	pbcaStsUnavailableAfterRetries = "ParseableClusterAutoscalerStatefulSetUnavailableAfterRetries"
)

type stateValues struct {
	replicas   int32
	scaleEvent scaler.ScaleEvent
	behavior   *v1.Behavior
	scaling    scaler.ScalingClient
	recorder   record.EventRecorder
	k8sClient  client.Client

	// Inter-state values
	addedSTS            *appsv1.StatefulSet
	pbca                *v1.ParseableClusterAutoscaler
	pbc                 *v1.ParseableCluster
	stabalisationWindow int64
}

type state[T any] func(ctx context.Context, stateValues T) (T, state[T], error)

func run[T any](ctx context.Context, args T, start state[T]) (T, error) {
	var err error
	current := start
	for {
		if ctx.Err() != nil {
			return args, ctx.Err()
		}
		args, current, err = current(ctx, args)
		if err != nil {
			return args, err
		}
		if current == nil {
			return args, nil
		}
	}
}

// state00 determines the type of scaling event and sets the initial state accordingly.
func state00(ctx context.Context, sv stateValues) (stateValues, state[stateValues], error) {
	switch sv.scaleEvent.String() {
	case scaler.ScaleUpStr:
		// Set the desired replica count based on the scale-up policy
		for _, policy := range sv.behavior.ScaleUp.Policies {
			if policy.Type == v1.Pods {
				sv.replicas = policy.Value
			}
		}
		return state01(ctx, sv)

	case scaler.ScaleDownStr:
		// Handle scale-down events (currently no specific action)
		// You can add additional cases here if needed
		for _, policy := range sv.behavior.ScaleDown.Policies {
			if policy.Type == v1.Pods {
				sv.replicas = policy.Value
			}
		}
		return state01(ctx, sv)

	default:
		// If no scale event matches, do nothing
		return doNothing(ctx, sv)
	}

}

// state01 adds a new StatefulSet based on the replica count determined in state00.
func state01(ctx context.Context, sv stateValues) (stateValues, state[stateValues], error) {

	// Check if LastAddSTSTimeStamp is more than 2 minutes ago
	if time.Since(sv.pbca.Status.LastAddSTSTimeStamp.Time) <= 2*time.Minute {
		sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsAddFail, "State 01 Cannot add StatefulSet: LastAddSTSTimeStamp is too recent")
		return sv, doNothing, nil
	}

	// Add the StatefulSet since the stabilization window has passed
	result, sts, err := sv.scaling.AddStatefulSet(ctx, &sv.replicas)
	if err != nil {
		sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsAddFail, "Failed to add StatefulSet: %v", err)
		return sv, nil, fmt.Errorf("problem adding the StatefulSet: %w", err)
	}

	// Record successful addition of StatefulSet
	sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStsAddSuccess, "StatefulSet added successfully: %s, Result : %s", sts.Name, result)
	sv.addedSTS = sts
	return sv, state02, nil
}

func state02(ctx context.Context, sv stateValues) (stateValues, state[stateValues], error) {
	// Ensure 2 minutes have passed since the last addition
	if time.Since(sv.pbca.Status.LastAddSTSTimeStamp.Time) <= 2*time.Minute {
		sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsNotAvailable, "Cannot proceed: LastAddSTSTimeStamp is less than 2 minutes ago")
		return sv, doNothing, fmt.Errorf("skipping: LastAddSTSTimeStamp is less than 2 minutes ago")
	}

	// Add delay to make sure STS is up, API takes roughly 5-10 seconds
	// though you might see an immediate in kubectl watch.

	time.Sleep(30 * time.Second)

	maxRetries := 5

	latestSTS := &appsv1.StatefulSet{}
	var err error
	// Retry loop to give the StatefulSet time to stabilize
	for retries := 0; retries < maxRetries; retries++ {
		latestSTS, err = sv.scaling.GetLatestSTS(ctx)
		if err != nil {
			sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsFetchFail, "Failed to fetch the latest StatefulSet: %v", err)
			return sv, nil, fmt.Errorf("failed to fetch the latest StatefulSet: %w", err)
		}

		// Check if the StatefulSet is fully deployed
		if latestSTS.Status.AvailableReplicas == latestSTS.Status.CurrentReplicas {
			// Patch the autoscaler status with the new StatefulSet name
			_, _, err = utils.PatchStatus(ctx, sv.k8sClient, sv.pbca, func(obj client.Object) client.Object {
				in := obj.(*v1.ParseableClusterAutoscaler)
				key := latestSTS.Labels["original_sts_name"]
				// Check if the map is nil, and initialize it if necessary
				if in.Status.IngestorIndex == nil {
					in.Status.IngestorIndex = make(map[string]string)
				}
				in.Status.IngestorIndex[key] = latestSTS.Name
				return in
			})
			if err != nil {
				return sv, doNothing, fmt.Errorf("failed to patch CurrentIngestorSTS in autoscaler: %w", err)
			}

			// Patch the ParseableCluster status with the new StatefulSet name
			_, _, err = utils.PatchStatus(ctx, sv.k8sClient, sv.pbc, func(obj client.Object) client.Object {
				in := obj.(*v1.ParseableCluster)
				key := latestSTS.Labels["original_sts_name"]
				// Check if the map is nil, and initialize it if necessary
				if in.Status.IngestorIndex == nil {
					in.Status.IngestorIndex = make(map[string]string)
				}
				in.Status.IngestorIndex[key] = latestSTS.Name
				return in
			})
			if err != nil {
				return sv, doNothing, fmt.Errorf("failed to patch CurrentIngestorSTS in ParseableCluster: %w", err)
			}

			// Record successful service update
			sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStsAddSuccess, "StatefulSet fully deployed: %s", latestSTS.Name)
			return sv, state03, nil
		}

		// If not yet available, sleep and retry
		sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsNotAvailable, "StatefulSet not fully available, sleeping 30 seconds")
		time.Sleep(60 * time.Second)
	}

	// If the StatefulSet is still not available after retries, delete it
	sv.recorder.Event(sv.pbca, corev1.EventTypeWarning, pbcaStsUnavailableAfterRetries, fmt.Sprintf("StatefulSet still unavailable, proceeding to delete: %s", latestSTS.Name))
	err = sv.k8sClient.Delete(ctx, latestSTS)
	if err != nil {
		return sv, nil, fmt.Errorf("failed to delete StatefulSet %s: %w", latestSTS.Name, err)
	}

	return sv, doNothing, nil
}

// state03 removes the old StatefulSet after the new one has been stabilized and service updated.
func state03(ctx context.Context, sv stateValues) (stateValues, state[stateValues], error) {
	// Now remove the old StatefulSet
	result, err := sv.scaling.RemoveStatefulSet(ctx)
	if err != nil {
		sv.recorder.Eventf(sv.pbca, corev1.EventTypeWarning, pbcaStsRemoveSuccess, "Failed to remove StatefulSet: %v", err)
		return sv, nil, fmt.Errorf("problem removing the StatefulSet: %w", err)
	}

	// Record successful removal of StatefulSet
	sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStsRemoveSuccess, "StatefulSet removed successfully: %s", result)
	return sv, doNothing, nil
}

// doNothing is a terminal state that does nothing and returns nil, signaling the end of the state machine.
func doNothing(ctx context.Context, sv stateValues) (stateValues, state[stateValues], error) {
	return stateValues{}, nil, nil
}

// // handleStabilization manages the stabilization window by waiting if needed.
// func handleStabilization(ctx context.Context, sv *stateValues, lastAddTime time.Time) (bool, error) {
// 	now := time.Now().UTC()
// 	timeDifference := now.Sub(lastAddTime)

// 	// Check if the stabilization window has passed
// 	if timeDifference < time.Duration(sv.stabalisationWindow) {
// 		waitTime := time.Duration(sv.stabalisationWindow) - timeDifference
// 		sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStabilizationWaiting, "Waiting for stabilization window to pass: %v", waitTime)

// 		select {
// 		case <-time.After(waitTime):
// 			sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStabilizationWindowPassed, "Stabilization window passed, proceeding")
// 			return true, nil
// 		case <-ctx.Done():
// 			return false, ctx.Err()
// 		}
// 	}

// 	sv.recorder.Eventf(sv.pbca, corev1.EventTypeNormal, pbcaStabilizationWindowPassed, "Stabilization window has already passed")
// 	return true, nil
// }
