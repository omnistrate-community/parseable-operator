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
	"context"
	"errors"
	"fmt"

	v1 "parseablehq/parseable-operator/api/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	objects "parseablehq/parseable-operator/pkg/objects"

	storage "k8s.io/api/storage/v1"
	"k8s.io/client-go/tools/record"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateVolumeClaimTemplateSpec(nodeSpec *v1.NodeSpec, k8sConfigSpec *v1.K8sConfigSpec) error {
	for range k8sConfigSpec.VolumeClaimTemplates {
		if nodeSpec.Kind == "statefulset" {
			if err := validateNodeVolumeClaimTemplateSpec(nodeSpec, k8sConfigSpec); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateNodeVolumeClaimTemplateSpec(nodeSpec *v1.NodeSpec, k8sConfigSpec *v1.K8sConfigSpec) error {
	for _, vct := range k8sConfigSpec.VolumeClaimTemplates {
		if vct.Spec.StorageClassName == nil || *vct.Spec.StorageClassName == "" {
			return fmt.Errorf("node group %s has volume claim template without storage class which is not allowed: %s",
				nodeSpec.Type, vct.Name)
		}
	}
	return nil
}

func (r *ParseableClusterReconciler) expandStatefulSetVolumes(
	ctx context.Context,
	stsName string,
	pbc *v1.ParseableCluster,
	nodeSpec *v1.NodeSpec,
	k8sConfigSpec *v1.K8sConfigSpec,
	record record.EventRecorder,
) error {

	isEnabled, err := isVolumeExpansionEnabled(ctx, r.Client, pbc, nodeSpec, k8sConfigSpec, record)
	if err != nil {
		return err
	}

	if isEnabled {
		err := scalePVCForSts(ctx, stsName, r.Client, pbc, nodeSpec, k8sConfigSpec, record)
		if err != nil {
			return err
		}
	}

	return nil
}

func isVolumeExpansionEnabled(
	ctx context.Context,
	client client.Client,
	pbc *v1.ParseableCluster,
	nodeSpec *v1.NodeSpec,
	k8sConfigSpec *v1.K8sConfigSpec,
	record record.EventRecorder,
) (bool, error) {

	for _, nodeVCT := range k8sConfigSpec.VolumeClaimTemplates {
		if nodeVCT.Spec.StorageClassName == nil {
			err := errors.New("StorageClassName does not exists")
			msg := fmt.Sprintf("NodeType [%s],VolumeClaimTemplate [%s], storage class does not exist Error [%s]",
				nodeSpec.Type, nodeVCT.Name, err)
			record.Event(pbc, corev1.EventTypeWarning, "", msg)
			return false, err
		}
		// sdk, *nodeVCT.Spec.StorageClassName, m, func() object { return &storage.StorageClass{} }, emitEvent
		sc := storage.StorageClass{}
		err := client.Get(ctx, types.NamespacedName{Namespace: pbc.Namespace, Name: *nodeVCT.Spec.StorageClassName}, &sc)
		if err != nil {
			return false, err
		}

		if sc.AllowVolumeExpansion != boolFalse() {
			return true, nil
		}
	}
	return false, nil
}

// scalePVCForSts shall expand the StatefulSet's VolumeClaimTemplates size as well as N no of pvc supported by the sts.
func scalePVCForSts(
	ctx context.Context,
	stsName string,
	client client.Client,
	pbc *v1.ParseableCluster,
	nodeSpec *v1.NodeSpec,
	k8sConfigSpec *v1.K8sConfigSpec,
	record record.EventRecorder,
) error {

	stsListOpts := []crclient.ListOption{
		crclient.InNamespace(pbc.Namespace),
		// TODO : k8sconfig.Name to be passed as labels
		crclient.MatchingLabels(objects.MakeLabels(map[string]string{}, pbc, stsName, nodeSpec.Name, nodeSpec.Type)),
	}

	stsListObj := appsv1.StatefulSetList{}
	if err := client.List(ctx, &stsListObj, stsListOpts...); err != nil {
		return err
	}

	// Dont proceed unless all statefulsets are up and running.
	//  This can cause the go routine to panic

	for _, sts := range stsListObj.Items {
		if sts.Status.Replicas != sts.Status.ReadyReplicas {
			return nil
		}
	}

	// return nil, in case return err the program halts since sts would not be able
	// we would like the operator to create sts.
	stsObj := appsv1.StatefulSet{}
	err := client.Get(ctx, types.NamespacedName{
		Namespace: pbc.Namespace,
		Name:      k8sConfigSpec.Name,
	}, &stsObj)
	if err != nil {
		return nil
	}

	pvcListOpts := []crclient.ListOption{
		crclient.InNamespace(pbc.Namespace),
		// TODO: k8sconfig.Name to be passed as labels
		crclient.MatchingLabels(objects.MakeLabels(map[string]string{}, pbc, stsName, nodeSpec.Name, nodeSpec.Type)),
	}

	pvcListObj := corev1.PersistentVolumeClaimList{}
	if err := client.List(ctx, &pvcListObj, pvcListOpts...); err != nil {
		return err
	}

	desVolumeClaimTemplateSize, currVolumeClaimTemplateSize, pvcSize := getVolumeClaimTemplateSizes(stsObj, k8sConfigSpec, pvcListObj.Items)

	// current number of PVC can't be less than desired number of pvc
	if len(pvcSize) < len(desVolumeClaimTemplateSize) {
		return nil
	}

	// iterate over array for matching each index in desVolumeClaimTemplateSize, currVolumeClaimTemplateSize and pvcSize
	for i := range desVolumeClaimTemplateSize {

		// Validate Request, shrinking of pvc not supported
		// desired size cant be less than current size
		// in that case re-create sts/pvc which is a user execute manual step

		desiredSize, _ := desVolumeClaimTemplateSize[i].AsInt64()
		currentSize, _ := currVolumeClaimTemplateSize[i].AsInt64()

		if desiredSize < currentSize {
			e := fmt.Errorf("Request for Shrinking of sts pvc size [sts:%s] in [namespace:%s] is not Supported", stsObj.Name, stsObj.Namespace)
			record.Event(pbc, corev1.EventTypeWarning, "", e.Error())
			return e
		}

		// In case size dont match and dessize > currsize, delete the sts using casacde=false / propagation policy set to orphan
		// The operator on next reconcile shall create the sts with latest changes
		if desiredSize != currentSize {
			msg := fmt.Sprintf("Detected Change in VolumeClaimTemplate Sizes for Statefuleset [%s] in Namespace [%s], desVolumeClaimTemplateSize: [%s], currVolumeClaimTemplateSize: [%s]\n, deleteing STS [%s] with casacde=false]", stsObj.Name, stsObj.Namespace, desVolumeClaimTemplateSize[i].String(), currVolumeClaimTemplateSize[i].String(), stsObj.Name)
			record.Event(pbc, corev1.EventTypeNormal, pbcOperatorPvcReSizeDetected, msg)
			// Create a variable to hold the DeletionPropagation value
			orphanPolicy := metav1.DeletePropagationOrphan

			// Define delete options with PropagationPolicy set to Orphan (cascade=false)
			deleteOptions := &crclient.DeleteOptions{
				PropagationPolicy: &orphanPolicy, // Pass a pointer to the variable
			}

			// Delete the StatefulSet with the specified PropagationPolicy
			if err := client.Delete(ctx, &stsObj, deleteOptions); err != nil {
				return err
			} else {
				// Log successful deletion
				msg := fmt.Sprintf("[StatefulSet:%s] successfully deleted with cascade=false", stsObj.Name)
				record.Event(pbc, corev1.EventTypeNormal, pbcOperatorStsOrphaned, msg)
			}

		}

		// In case size dont match, patch the pvc with the desiredsize from druid CR
		for p := range pvcSize {
			pSize, _ := pvcSize[p].AsInt64()
			if desiredSize != pSize {
				// use deepcopy
				patch := crclient.MergeFrom(pvcListObj.Items[p].DeepCopy())
				pvcListObj.Items[p].Spec.Resources.Requests[corev1.ResourceStorage] = desVolumeClaimTemplateSize[i]
				if err := client.Patch(ctx, pvcListObj.Items[p].DeepCopy(), patch); err != nil {
					return err
				} else {
					msg := fmt.Sprintf("[PVC:%s] successfully Patched with [Size:%s]", pvcListObj.Items[p].Name, desVolumeClaimTemplateSize[i].String())
					record.Event(pbc, corev1.EventTypeNormal, pbcOperatorPvcResizePatch, msg)

				}
			}
		}

	}

	return nil
}

func getVolumeClaimTemplateSizes(sts appsv1.StatefulSet, k8sConfig *v1.K8sConfigSpec, pvc []corev1.PersistentVolumeClaim) (desVolumeClaimTemplateSize, currVolumeClaimTemplateSize, pvcSize []resource.Quantity) {

	for i := range k8sConfig.VolumeClaimTemplates {
		desVolumeClaimTemplateSize = append(desVolumeClaimTemplateSize, k8sConfig.VolumeClaimTemplates[i].Spec.Resources.Requests[corev1.ResourceStorage])
	}

	for i := range sts.Spec.VolumeClaimTemplates {
		currVolumeClaimTemplateSize = append(currVolumeClaimTemplateSize, sts.Spec.VolumeClaimTemplates[i].Spec.Resources.Requests[corev1.ResourceStorage])
	}

	for i := range pvc {
		pvcSize = append(pvcSize, pvc[i].Spec.Resources.Requests[corev1.ResourceStorage])
	}

	return desVolumeClaimTemplateSize, currVolumeClaimTemplateSize, pvcSize

}
