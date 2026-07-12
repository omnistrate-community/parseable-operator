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
	"encoding/json"
	"fmt"

	v1 "parseablehq/parseable-operator/api/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	deletePVCFinalizerName = "deletepvc.finalizers.parseable.com"
)

var (
	defaultFinalizers []string
)

func updateFinalizers(
	ctx context.Context,
	client client.Client,
	pbc *v1.ParseableCluster) error {
	desiredFinalizers := pbc.GetFinalizers()
	additionFinalizers := defaultFinalizers

	desiredFinalizers = removeString(desiredFinalizers, deletePVCFinalizerName)

	additionFinalizers = append(additionFinalizers, deletePVCFinalizerName)

	for _, finalizer := range additionFinalizers {
		if !containsString(desiredFinalizers, finalizer) {
			desiredFinalizers = append(desiredFinalizers, finalizer)
		}
	}

	if !equality.Semantic.DeepEqual(pbc.GetFinalizers(), desiredFinalizers) {
		pbc.SetFinalizers(desiredFinalizers)

		finalizersBytes, err := json.Marshal(pbc.GetFinalizers())
		if err != nil {
			return fmt.Errorf("failed to serialize finalizers patch to bytes: %v", err)
		}

		patch := []byte(fmt.Sprintf(`[{"op": "replace", "path": "/metadata/finalizers", "value": %s}]`, finalizersBytes))

		err = client.Patch(ctx, pbc, crclient.RawPatch(types.JSONPatchType, patch))
		if err != nil {
			return err
		}

	}

	return nil
}

func executeFinalizers(
	ctx context.Context,
	client client.Client,
	pbc *v1.ParseableCluster,
	record record.EventRecorder) error {

	if err := executePVCFinalizer(ctx, client, pbc, record); err != nil {
		return err
	}

	return nil
}

/*
executePVCFinalizer will execute a PVC deletion of all parseable's PVCs.
Flow:
 1. Get sts List and PVC List
 2. Range and Delete sts first and then delete pvc. PVC must be deleted after sts termination has been executed
    else pvc finalizer shall block deletion since a pod/sts is referencing it.
 3. Once delete is executed we block program and return.
*/
func executePVCFinalizer(
	ctx context.Context,
	client client.Client,
	pbc *v1.ParseableCluster,
	record record.EventRecorder) error {
	if containsString(pbc.ObjectMeta.Finalizers, deletePVCFinalizerName) {
		commonLabels := map[string]string{
			"parseable_cr": pbc.Name,
		}
		pvcListOpts := []crclient.ListOption{
			crclient.InNamespace(pbc.Namespace),
			crclient.MatchingLabels(commonLabels),
		}
		pvcList := corev1.PersistentVolumeClaimList{}
		err := client.List(ctx, &pvcList, pvcListOpts...)
		if err != nil {
			return err
		}

		stsListOpts := []crclient.ListOption{
			crclient.InNamespace(pbc.Namespace),
			crclient.MatchingLabels(commonLabels),
		}

		stsListObj := appsv1.StatefulSetList{}
		if err := client.List(ctx, &stsListObj, stsListOpts...); err != nil {
			return err
		}

		record.Event(
			pbc,
			corev1.EventTypeNormal,
			pbcOperatorFinalizerTriggered,
			fmt.Sprintf("trigerring finalizer [%s] for cr [%s] in namespace [%s], Waiting for data to sync from pvc to s3 storage.",
				deletePVCFinalizerName, pbc.Name, pbc.Namespace))

		if err = deleteSTS(
			ctx,
			client,
			stsListObj.Items); err != nil {
			record.Event(
				pbc,
				corev1.EventTypeWarning,
				pbcOperatorFinalizerTriggered,
				fmt.Sprintf("triggering finalizer failed [%s] for cr [%s] in namespace [%s], delete sts and pvc",
					deletePVCFinalizerName, pbc.Name, pbc.Namespace))

			return err
		}

		record.Event(
			pbc,
			corev1.EventTypeNormal,
			pbcOperatorFinalizerTriggered,
			fmt.Sprintf(
				"Finalizer [%s] success for CR [%s] in namespace [%s]",
				deletePVCFinalizerName,
				pbc.Name,
				pbc.Namespace),
		)

		// remove our finalizer from the list and update it.
		pbc.ObjectMeta.Finalizers = removeString(pbc.ObjectMeta.Finalizers, deletePVCFinalizerName)

		err = client.Update(ctx, pbc)
		if err != nil {
			return err
		}

	}
	return nil
}

func deleteSTS(
	ctx context.Context,
	client client.Client,
	stsList []appsv1.StatefulSet,
	//pvcList []corev1.PersistentVolumeClaim,
) error {

	for _, sts := range stsList {
		err := client.Delete(ctx, &sts)
		if err != nil {
			return err
		}
	}

	// for i := range pvcList {
	// 	err := client.Delete(ctx, &pvcList[i])
	// 	if err != nil {
	// 		return err
	// 	}
	// }

	return nil
}
