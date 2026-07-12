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
	"strings"

	appsv1 "k8s.io/api/apps/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type metricsK8s interface {
	getCPUUsageForSTS() (int64, error)
	getCPURequestsForSTS() (int64, error)
	getCurrentReplicasForSTS() (int32, error)
}

type metricsK8sHolder struct {
	ctx            context.Context
	pbca           *v1.ParseableClusterAutoscaler
	metricsClient  *metricsclient.Clientset
	k8sClient      client.Client
	selectorLabels map[string]string
}

// newMetricsK8sHolder is a constructor for the metricsK8sHolder struct.
func newMetricsK8sHolder(
	ctx context.Context,
	pbca *v1.ParseableClusterAutoscaler,
	metricsClient *metricsclient.Clientset,
	k8sClient client.Client,
	selectorLabels map[string]string,
) metricsK8s {
	return &metricsK8sHolder{
		ctx:            ctx,
		pbca:           pbca,
		metricsClient:  metricsClient,
		k8sClient:      k8sClient,
		selectorLabels: selectorLabels,
	}
}

func (mk *metricsK8sHolder) getCPUUsageForSTS() (int64, error) {
	podMetrics, err := mk.metricsClient.MetricsV1beta1().
		PodMetricses(mk.pbca.Namespace).
		List(mk.ctx, metav1.ListOptions{
			LabelSelector: makeLabelsForPodSelection(
				mk.pbca.Spec.ScaleTargetRef,
				mk.selectorLabels).AsString(),
		},
		)
	if err != nil {
		return 0, err
	}

	stsList := &appsv1.StatefulSetList{}
	stsListOpts := []client.ListOption{
		client.InNamespace(mk.pbca.Namespace),
		client.MatchingLabels(mk.selectorLabels),
	}

	err = mk.k8sClient.List(mk.ctx, stsList, stsListOpts...)
	if err != nil {
		return 0, err
	}

	if len(stsList.Items) == 0 {
		return 0, fmt.Errorf("No StatefulSets found")
	}

	fmt.Println(len(stsList.Items))
	// Find the StatefulSet with the latest CreationTimestamp
	latestSTS := &stsList.Items[0]
	for _, sts := range stsList.Items {
		if sts.CreationTimestamp.Time.After(latestSTS.CreationTimestamp.Time) {
			latestSTS = &sts
		}
	}

	// Log the StatefulSet being used
	klog.Infof("Using StatefulSet for CPU usage calculation: %s", latestSTS.Name)

	var allContainerUsageHolder []int64
	for _, podMetric := range podMetrics.Items {
		//if podMetric.Labels["sts_name"] == latestSTS.Labels["sts_name"] {
		for _, container := range podMetric.Containers {
			allContainerUsageHolder = append(allContainerUsageHolder, container.Usage.Cpu().MilliValue())
		}
		//}
	}

	return sumInt64Slice(allContainerUsageHolder), nil
}

func (mk *metricsK8sHolder) getCPURequestsForSTS() (int64, error) {

	stsListOpts := []client.ListOption{
		client.InNamespace(mk.pbca.Namespace),
		client.MatchingLabels(makeLabelsForPodSelection(mk.pbca.Spec.ScaleTargetRef, mk.selectorLabels).AsMap()),
	}

	stsListObj := appsv1.StatefulSetList{}

	err := mk.k8sClient.List(mk.ctx, &stsListObj, stsListOpts...)
	if err != nil {
		return 0, nil
	}

	if len(stsListObj.Items) == 0 {
		return 0, fmt.Errorf("No StatefulSets found")
	}

	latestSTS := &stsListObj.Items[0]
	for _, sts := range stsListObj.Items {
		if sts.CreationTimestamp.Time.After(latestSTS.CreationTimestamp.Time) {
			latestSTS = &sts
		}
	}

	// Log the StatefulSet being used
	klog.Infof("Using StatefulSet for CPU requests calculation: %s", latestSTS.Name)

	for _, container := range latestSTS.Spec.Template.Spec.Containers {
		return container.Resources.Requests.Cpu().MilliValue(), nil
	}

	return 0, nil
}

func (mk *metricsK8sHolder) getCurrentReplicasForSTS() (int32, error) {

	stsListOpts := []client.ListOption{
		client.InNamespace(mk.pbca.Namespace),
		client.MatchingLabels(makeLabelsForPodSelection(mk.pbca.Spec.ScaleTargetRef, mk.selectorLabels).AsMap()),
	}

	stsListObj := appsv1.StatefulSetList{}

	err := mk.k8sClient.List(mk.ctx, &stsListObj, stsListOpts...)
	if err != nil {
		return 0, nil
	}

	if len(stsListObj.Items) == 0 {
		return 0, fmt.Errorf("No StatefulSets found")
	}

	latestSTS := &stsListObj.Items[0]
	for _, sts := range stsListObj.Items {
		if sts.CreationTimestamp.Time.After(latestSTS.CreationTimestamp.Time) {
			latestSTS = &sts
		}
	}

	// Log the StatefulSet being used
	klog.Infof("Using StatefulSet for replica count: %s", latestSTS.Name)

	return *latestSTS.Spec.Replicas, nil
}

// labelsForPodSelection holds both string and map representations of labels.
type labelsForPodSelection struct {
	labelsMap    map[string]string
	labelsString string
}

// AsMap returns the map representation of the labels.
func (l labelsForPodSelection) AsMap() map[string]string {
	return l.labelsMap
}

// AsString returns the string representation of the labels.
func (l labelsForPodSelection) AsString() string {
	return l.labelsString
}

// makeLabelsForPodSelection creates the LabelsForPodSelection struct.
func makeLabelsForPodSelection(
	targetRef v1.ScaleTargetRef,
	selectorLabels map[string]string,
) labelsForPodSelection {
	labelsMap := map[string]string{
		"app":          "parseable-cluster",
		"parseable_cr": targetRef.Name,
	}

	for key, value := range selectorLabels {
		labelsMap[key] = value
	}

	var labelStrings []string
	for key, value := range labelsMap {
		labelStrings = append(labelStrings, key+"="+value)
	}

	return labelsForPodSelection{
		labelsMap:    labelsMap,
		labelsString: strings.Join(labelStrings, ","),
	}
}
