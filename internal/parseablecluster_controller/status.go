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
	v1 "parseablehq/parseable-operator/api/v1"
	objects "parseablehq/parseable-operator/pkg/objects"
	"parseablehq/parseable-operator/pkg/utils"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateStatus summarises the StatefulSets owned by the ParseableCluster into
// status.phase / replicas / readyReplicas, so external tooling can gate on a
// flat field instead of inspecting every workload.
func (r *ParseableClusterReconciler) updateStatus(ctx context.Context, pbc *v1.ParseableCluster) error {
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList, client.InNamespace(pbc.Namespace), client.MatchingLabels{"parseable_cr": pbc.Name}); err != nil {
		return err
	}

	ownerRef := objects.MakeOwnerRef(pbc.APIVersion, pbc.Kind, pbc.Name, pbc.UID)

	var replicas, readyReplicas int32
	owned := 0
	settled := true
	for i := range stsList.Items {
		sts := &stsList.Items[i]
		if !isOwnedBy(sts, ownerRef) {
			continue
		}
		owned++
		var desired int32
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		replicas += desired
		readyReplicas += sts.Status.ReadyReplicas
		// updatedReplicas/currentReplicas go stale after a scale to zero and
		// back, so rely on the revisions and per-StatefulSet readiness instead.
		if sts.Status.ObservedGeneration != sts.Generation ||
			sts.Status.CurrentRevision != sts.Status.UpdateRevision ||
			sts.Status.ReadyReplicas != desired {
			settled = false
		}
	}

	phase := v1.ClusterPhaseProgressing
	switch {
	case pbc.GetAnnotations()["parseable.com/workspace"] == "suspend":
		if replicas == 0 && readyReplicas == 0 {
			phase = v1.ClusterPhaseSuspended
		}
	case owned >= len(pbc.Spec.Nodes) && settled && replicas > 0 && readyReplicas == replicas:
		phase = v1.ClusterPhaseReady
	}

	if pbc.Status.Phase == phase &&
		pbc.Status.Replicas == replicas &&
		pbc.Status.ReadyReplicas == readyReplicas &&
		pbc.Status.ObservedGeneration == pbc.Generation {
		return nil
	}

	_, _, err := utils.PatchStatus(ctx, r.Client, pbc, func(obj client.Object) client.Object {
		in := obj.(*v1.ParseableCluster)
		in.Status.Phase = phase
		in.Status.Replicas = replicas
		in.Status.ReadyReplicas = readyReplicas
		in.Status.ObservedGeneration = in.Generation
		return in
	})
	return err
}
