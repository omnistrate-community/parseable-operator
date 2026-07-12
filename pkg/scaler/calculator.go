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
	"errors"

	"k8s.io/klog/v2"
)

// ScaleEvent represents the possible outcomes of the scaling decision.
type ScaleEvent int

const (
	NoScaleRequired    ScaleEvent = iota // 0: No scaling needed (within threshold)
	ScaleUp                              // 1: Scaling up required
	ScaleDown                            // 2: Scaling down required
	MaxReplicasReached                   // 3: Cannot scale up, max replicas reached
	MinReplicasReached                   // 4: Cannot scale down, min replicas reached
)

const (
	NoScaleRequiredStr    = "noScaleRequired"
	ScaleUpStr            = "scaleUp"
	ScaleDownStr          = "scaleDown"
	MaxReplicasReachedStr = "maxReplicasReached"
	MinReplicasReachedStr = "minReplicasReached"
	UnknownEventStr       = "unknownEvent"
)

// String returns a string representation of the ScaleEvent.
func (e ScaleEvent) String() string {
	switch e {
	case NoScaleRequired:
		return NoScaleRequiredStr
	case ScaleUp:
		return ScaleUpStr
	case ScaleDown:
		return ScaleDownStr
	case MaxReplicasReached:
		return MaxReplicasReachedStr
	case MinReplicasReached:
		return MinReplicasReachedStr
	default:
		return UnknownEventStr
	}
}

// scalingCalculator defines an interface for calculating whether scaling is necessary.
type ScalingCalculator interface {
	// ShouldScale determines the scaling action (scale up, scale down, or no action)
	// based on the current CPU usage and requests.
	ShouldScale(
		getCPUUsageForSTS func() (int64, error),
		getCPURequestsForSTS func() (int64, error),
	) (ScaleEvent, error)
}

// scalingCalculatorHolder is a struct that holds the parameters necessary for scaling calculations.
type ScalingCalculatorHolder struct {
	MinReplicas     int32   // The minimum number of replicas allowed.
	MaxReplicas     int32   // The maximum number of replicas allowed.
	CurrentReplicas int32   // The current number of replicas.
	Threshold       float64 // The CPU usage threshold percentage for scaling.
}

// NewScalingCalculatorHolder is a constructor for the ScalingCalculatorHolder struct.
// It initializes and returns an instance of scalingCalculator, encapsulating the provided scaling parameters.
func NewScalingCalculatorHolder(
	minReplicas int32, // The minimum number of replicas.
	maxReplicas int32, // The maximum number of replicas.
	currentReplicas int32, // The current number of replicas.
	threshold float64, // The CPU usage threshold percentage for scaling.
) ScalingCalculator {
	return &ScalingCalculatorHolder{
		MinReplicas:     minReplicas,
		MaxReplicas:     maxReplicas,
		CurrentReplicas: currentReplicas,
		Threshold:       threshold,
	}
}

// ShouldScale calculates whether the number of replicas should be scaled up or down based on CPU usage.
// It returns a ScaleEvent to specify the scaling decision and an error if something goes wrong.
func (a *ScalingCalculatorHolder) ShouldScale(
	getCPUUsageForSTS func() (int64, error),
	getCPURequestsForSTS func() (int64, error),
) (ScaleEvent, error) {
	// Retrieve the current CPU usage.
	cpuUsage, err := getCPUUsageForSTS()
	if err != nil {
		klog.Errorf("Failed to retrieve CPU usage: %v", err)
		return NoScaleRequired, err
	}

	// Retrieve the current CPU requests.
	cpuRequests, err := getCPURequestsForSTS()
	if err != nil {
		klog.Errorf("Failed to retrieve CPU requests: %v", err)
		return NoScaleRequired, err
	}

	// Handle the edge case where cpuRequests is zero.
	if cpuRequests == 0 {
		klog.Errorf("CPU requests is zero, cannot calculate CPU usage percentage")
		return NoScaleRequired, errors.New("cpuRequests is zero, cannot calculate CPU usage percentage")
	}

	// Calculate the percentage of CPU usage.
	cpuUsagePercentage := float64(cpuUsage) / float64(cpuRequests) * 100
	klog.Infof("CPU usage percentage: %.2f%%", cpuUsagePercentage)

	// Determine if scaling up is needed.
	if cpuUsagePercentage > a.Threshold {
		klog.Infof("CPU usage (%.2f%%) is above the threshold (%.2f%%)", cpuUsagePercentage, a.Threshold)
		if a.CurrentReplicas < a.MaxReplicas {
			klog.Infof("Current replicas (%d) are below the maximum replicas (%d), scaling up", a.CurrentReplicas, a.MaxReplicas)
			return ScaleUp, nil // Scale up if the current replicas are below the maximum limit.
		}
		klog.Warningf("Max replicas limit (%d) reached, cannot scale up", a.MaxReplicas)
		return MaxReplicasReached, nil // Do nothing, max replicas limit has been reached.
	}

	// Determine if scaling down is needed.
	if cpuUsagePercentage < a.Threshold {
		klog.Infof("CPU usage (%.2f%%) is below the threshold (%.2f%%)", cpuUsagePercentage, a.Threshold)
		if a.CurrentReplicas > a.MinReplicas {
			klog.Infof("Current replicas (%d) are above the minimum replicas (%d), scaling down", a.CurrentReplicas, a.MinReplicas)
			return ScaleDown, nil // Scale down if the current replicas are above the minimum limit.
		}
		klog.Warningf("Min replicas limit (%d) reached, cannot scale down", a.MinReplicas)
		return MinReplicasReached, nil // Do nothing, min replicas limit has been reached.
	}

	// No scaling action is needed if the CPU usage is within the threshold range.
	klog.Infof("CPU usage is within the threshold (%.2f%%), no scaling required", cpuUsagePercentage)
	return NoScaleRequired, nil
}
