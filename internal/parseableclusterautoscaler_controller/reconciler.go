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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Constants for event recording
const (
	pbcaScalingInit        = "ParseableClusterAutoscalerScalingInitialization"
	pbcaScalingMachineDone = "ParseableClusterAutoscalerScalingDone"
	pbcaScalingDecision    = "ParseableClusterAutoscalerScalingDecision"
	pbcaScalingError       = "ParseableClusterAutoscalerScalingError"
	pbcaStateMachineError  = "ParseableClusterAutoscalerStateMachineError"
)

func (r *ParseableClusterAutoscalerReconciler) do(ctx context.Context, pbca *v1.ParseableClusterAutoscaler, pbc *v1.ParseableCluster) error {
	// Iterate over each scaling configuration defined in the autoscaler spec

	//var stabalisationWindow int64
	for _, scalingConfig := range pbca.Spec.ScalingConfig {
		//stabalisationWindow := scalingConfig.StabilizationWindowSeconds
		// Initialize metrics holder to gather Kubernetes metrics (e.g., CPU usage)
		metricsK8s := newMetricsK8sHolder(
			ctx,
			pbca,
			r.metricsClientset,
			r.Client,
			scalingConfig.SelectorLabels,
		)

		// Get the current number of replicas in the StatefulSet
		currentReplicas, err := metricsK8s.getCurrentReplicasForSTS()
		if err != nil {
			// Record an event if there's an error retrieving replicas and return the error
			r.Recorder.Eventf(pbca, corev1.EventTypeWarning, pbcaScalingError, "Error retrieving current replicas for StatefulSet: %v", err)
			return err
		}

		// Initialize the scaling calculator to determine if scaling is required
		scalingCalculator := scaler.NewScalingCalculatorHolder(
			scalingConfig.MinReplicas,
			scalingConfig.MaxReplicas,
			currentReplicas,
			scalingConfig.Threshold,
		)

		// Check if scaling is necessary by evaluating current CPU usage against the threshold
		scaleEvent, err := scalingCalculator.ShouldScale(metricsK8s.getCPUUsageForSTS, metricsK8s.getCPURequestsForSTS)
		if err != nil {
			// Record an event if there's an error in the scaling decision process and return the error
			r.Recorder.Eventf(pbca, corev1.EventTypeWarning, pbcaScalingError, "Error determining scaling: %v", err)
			return err
		}

		// If the decision indicates that min or max replicas have been reached, record the decision and skip scaling
		if scaleEvent.String() == scaler.MinReplicasReachedStr || scaleEvent.String() == scaler.MaxReplicasReachedStr {
			r.Recorder.Eventf(pbca, corev1.EventTypeNormal, pbcaScalingDecision, "Scaling decision made: %s, no event will be passed to state machine.", scaleEvent.String())
			// patch the autoscaler status with new sts name
			_, _, err = utils.PatchStatus(ctx, r.Client, pbc, func(obj client.Object) client.Object {
				in := obj.(*v1.ParseableCluster)
				in.Status.IsScaling = false
				return in
			})
			return nil
		}

		// Record the scaling decision event
		r.Recorder.Eventf(pbca, corev1.EventTypeNormal, pbcaScalingDecision, "Scaling decision made: %s", scaleEvent.String())

		_, _, err = utils.PatchStatus(ctx, r.Client, pbc, func(obj client.Object) client.Object {
			in := obj.(*v1.ParseableCluster)
			in.Status.IsScaling = true
			return in
		})

		// Prepare values for the state machine, including the scaling event and configuration
		sv := stateValues{
			behavior:   &scalingConfig.Behavior,
			scaleEvent: scaleEvent,
			k8sClient:  r.Client,
			pbca:       pbca,
			recorder:   r.Recorder,
			pbc:        pbc,
			scaling: scaler.NewScalingInputs(
				r.Client,
				pbca,
				&scalingConfig,
				scaleEvent,
			),
		}

		// Determine the starting state of the state machine based on the scaling event
		var startState state[stateValues]
		startState = state00

		// Run the state machine with the initialized values and chosen start state
		_, err = run(ctx, sv, startState)
		if err != nil {
			// Record an event if an error occurs during state machine execution and return the error
			r.Recorder.Eventf(pbca, corev1.EventTypeWarning, pbcaStateMachineError, "Error during state machine execution: %v", err)
			return fmt.Errorf("Error during state machine execution: %v", err)
		}
	}

	// Record a successful completion of the scaling process
	r.Recorder.Event(pbca, corev1.EventTypeNormal, pbcaScalingMachineDone, "Scaling process completed successfully")

	// wait for 60 seconds before proceeding with next event
	time.Sleep(60 * time.Second)

	return nil
}
