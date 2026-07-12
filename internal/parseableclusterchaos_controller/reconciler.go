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

package parseableclusterchoascontroller

import (
	"context"
	"fmt"
	v1 "parseablehq/parseable-operator/api/v1"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ParseableClusterChaosReconciler) do(ctx context.Context, pbcc *v1.ParseableClusterChaos) error {
	klog.Info("Starting ParseableClusterChoas reconciliation...")

	pbcListOpts := []client.ListOption{
		client.MatchingLabels(pbcc.Spec.NodeSelectorLabels),
	}

	nodes := corev1.NodeList{}
	err := r.Client.List(ctx, &nodes, pbcListOpts...)
	if err != nil {
		klog.Errorf("Failed to list nodes: %v", err)
		return err
	}
	klog.Infof("Found %d ingestor nodes for reconciliation", len(nodes.Items))

	for _, node := range nodes.Items {
		// 1. Check if the node age is more than 5 hours
		nodeAge := time.Since(node.CreationTimestamp.Time)
		klog.Infof("Checking node %s, age: %v", node.Name, nodeAge)

		if nodeAge <= time.Duration(pbcc.Spec.Age)*time.Hour {
			klog.Infof("Node %s is younger than 5 hours, skipping", node.Name)
			continue
		}

		// 2. Check if CPU usage is more than 50%
		cpuUsage, err := getNodeCPUUsage(ctx, node, r.metricsClientset)
		if err != nil {
			klog.Errorf("Failed to get CPU usage for node %s: %v", node.Name, err)
			continue
		}
		klog.Infof("Node %s CPU usage: %.2f%%", node.Name, cpuUsage)

		if cpuUsage > pbcc.Spec.CPUUsage {
			klog.Infof("Node %s CPU usage is greater than %v%%, skipping", node.Name, pbcc.Spec.CPUUsage)
			continue
		}

		// 3. Mark node as unschedulable
		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Unschedulable = true
		if err := r.Client.Update(ctx, nodeCopy); err != nil {
			klog.Errorf("Failed to mark node %s as unschedulable: %v", node.Name, err)
			return err
		}
		klog.Infof("Node %s marked as unschedulable", node.Name)

		// 4. Drain the node (safely evict or delete pods on the node)
		if err := drainNode(ctx, r, nodeCopy); err != nil {
			klog.Errorf("Failed to drain node %s: %v", node.Name, err)
			return err
		}
		klog.Infof("Node %s drained successfully", node.Name)

		// 5. Once all pods are drained, delete the node
		err = r.Client.Delete(ctx, nodeCopy)
		if err != nil {
			klog.ErrorS(err, "Failed to delete node", "node", node.Name)
			return err
		}
		klog.InfoS("Node deleted successfully", "node", node.Name)
	}

	klog.Info("ParseableClusterChoas reconciliation completed.")
	return nil
}

// Helper function to get CPU usage of the node
func getNodeCPUUsage(ctx context.Context, node corev1.Node, metricsclient *metricsclient.Clientset) (float64, error) {
	nodeMetrics, err := metricsclient.MetricsV1beta1().NodeMetricses().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Error fetching metrics for node %s: %v", node.Name, err)
		return 0, err
	}

	currentCPUUsage := nodeMetrics.Usage.Cpu().MilliValue()
	totalCPUCapacity := node.Status.Capacity.Cpu().MilliValue()

	if totalCPUCapacity == 0 {
		err := fmt.Errorf("node %s has zero CPU capacity", node.Name)
		klog.Error(err)
		return 0, err
	}

	cpuUsagePercent := (float64(currentCPUUsage) / float64(totalCPUCapacity)) * 100
	return cpuUsagePercent, nil
}

// Helper function to drain the node (evict pods)
func drainNode(ctx context.Context, r *ParseableClusterChaosReconciler, node *corev1.Node) error {
	klog.Infof("Draining node %s...", node.Name)

	pods := corev1.PodList{}
	podListOpts := []client.ListOption{
		client.MatchingLabels{
			"component": "ingestor",
		},
	}

	err := r.Client.List(ctx, &pods, podListOpts...)
	if err != nil {
		klog.Errorf("Failed to list pods on node %s: %v", node.Name, err)
		return err
	}

	for _, pod := range pods.Items {
		klog.Infof("Deleting pod %s on node %s", pod.Name, node.Name)
		err := r.Client.Delete(ctx, &pod)
		if err != nil {
			klog.Errorf("Failed to delete pod %s: %v", pod.Name, err)
			return err
		}
	}

	klog.Infof("Node %s drained successfully", node.Name)
	return nil
}
