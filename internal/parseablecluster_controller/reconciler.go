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
	"errors"
	"fmt"
	v1 "parseablehq/parseable-operator/api/v1"
	objects "parseablehq/parseable-operator/pkg/objects"
	"parseablehq/parseable-operator/pkg/utils"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
)

const (
	pbcOperatorCreateFail                            = "ParseableOperatorCreateFail"
	pbcOperatorCreateSuccess                         = "ParseableOperatorCreateSuccess"
	pbcOperatorUpdateFail                            = "ParseableOperatorUpdateFail"
	pbcOperatorUpdateSuccess                         = "ParseableOperatorSuccess"
	pbcOperatorStorageClassVolumeExpansionNotEnabled = "ParseableOperatorStorageClassVolumeExpansionNotEnabled"
	pbcOperatorPvcReSizeFail                         = "ParseableOperatorPvcReSizeFail"
	pbcOperatorPvcReSizeDetected                     = "ParseableOperatorPvcReSizeDetected"
	pbcOperatorStsOrphaned                           = "ParseableOperatorStsOrphaned"
	pbcOperatorPvcResizePatch                        = "ParseableOperatorPvcResizePatch"
	pbcOperatorFinalizerTriggered                    = "ParseableOperatorFinalizerTriggered"
	pbcOperatorFinalizerSuccess                      = "ParseableOperatorFinalizerSuccess"
	pbcOperatorFinalizerFail                         = "ParseableOperatorFinalizerFail"
	pbcOperatorHaltWaitForEquilibrium                = "ParseableOperatorHaltWaitForEquilibrium"
)

// do performs the reconciliation logic for the ParseableCluster resource.
// It iterates over all NodeSpecs and their corresponding K8sConfigs to ensure
// that the appropriate Kubernetes resources (e.g., StatefulSets) are created or updated.
func (r *ParseableClusterReconciler) do(ctx context.Context, pbc *v1.ParseableCluster) error {

	// Create an owner reference for all objects created by this controller
	ownerRef := objects.MakeOwnerRef(
		pbc.APIVersion,
		pbc.Kind,
		pbc.Name,
		pbc.UID,
	)

	if pbc.GetDeletionTimestamp() != nil {
		return executeFinalizers(ctx, r.Client, pbc, r.Recorder)
	}

	if err := updateFinalizers(ctx, r.Client, pbc); err != nil {
		return err
	}

	// Check for suspend/resume annotation
	if annotations := pbc.GetAnnotations(); annotations != nil {
		if workspaceState, exists := annotations["parseable.com/workspace"]; exists {
			switch workspaceState {
			case "suspend":
				return r.handleSuspend(ctx, pbc, ownerRef)
			case "resume":
				// Restore original replicas before removing annotation
				if err := r.handleResume(ctx, pbc, ownerRef); err != nil {
					return fmt.Errorf("failed to resume workspace: %w", err)
				}

				// Remove the annotation after successful resume
				delete(annotations, "parseable.com/workspace")
				pbc.SetAnnotations(annotations)
				if err := r.Update(ctx, pbc); err != nil {
					return fmt.Errorf("failed to remove resume annotation: %w", err)
				}
			}
		}

		// Check for suspend/resume annotation for ingestor and querier
		if ingestorState, exists := annotations["parseable.com/workspace"]; exists {
			switch ingestorState {
			case "suspend-ingestor":
				return r.handleSuspendIngestor(ctx, pbc, ownerRef)
			case "resume-ingestor":
				// Resume ingestors
				if err := r.handleResumeIngestor(ctx, pbc, ownerRef); err != nil {
					return fmt.Errorf("failed to resume ingestor: %w", err)
				}

				// Remove the annotation after successful resume
				delete(annotations, "parseable.com/workspace")
				pbc.SetAnnotations(annotations)
				if err := r.Update(ctx, pbc); err != nil {
					return fmt.Errorf("failed to remove resume-ingestor annotation: %w", err)
				}
			case "suspend-querier":
				return r.handleSuspendQuerier(ctx, pbc, ownerRef)
			case "resume-querier":
				// Resume queriers
				if err := r.handleResumeQuerier(ctx, pbc, ownerRef); err != nil {
					return fmt.Errorf("failed to resume querier: %w", err)
				}

				// Remove the annotation after successful resume
				delete(annotations, "parseable.com/workspace")
				pbc.SetAnnotations(annotations)
				if err := r.Update(ctx, pbc); err != nil {
					return fmt.Errorf("failed to remove resume-querier annotation: %w", err)
				}
			}
		}
	}

	// Get all NodeTypeNodeSpec objects for the ParseableCluster
	nodeTypeNodeSpecs := getAllNodeSpecForNodeType(pbc)

	// Iterate over each NodeTypeNodeSpec to process them
	for _, nodeTypeNodeSpec := range nodeTypeNodeSpecs {
		for _, parseableConfig := range pbc.Spec.ParseableConfig {
			// Match the NodeSpec's ParseableConfig with the ParseableConfig list
			if nodeTypeNodeSpec.NodeSpec.ParseableConfig == parseableConfig.Name {
				for _, k8sConfig := range pbc.Spec.K8sConfig {
					if nodeTypeNodeSpec.NodeSpec.K8sConfig == k8sConfig.Name {
						if nodeTypeNodeSpec.NodeSpec.Kind == "statefulset" {

							// if pbc.Generation > 1 {
							// 	if err := r.expandStatefulSetVolumes(
							// 		ctx,
							// 		k8sConfig.Name,
							// 		pbc,
							// 		&nodeTypeNodeSpec.NodeSpec,
							// 		&k8sConfig,
							// 		r.Recorder); err != nil {
							// 		return err
							// 	}
							// }

							envSecret := corev1.Secret{}
							err := r.Client.Get(ctx, types.NamespacedName{Name: parseableConfig.SecretName, Namespace: pbc.Namespace}, &envSecret)
							if err != nil {
								return err
							}

							// what we are trying to address here is to make pb controller aware of
							// if autoscaling is available, perform a lookup on status ingestor index
							// autoscaler will continue to change the original name of a sts as it scales
							// each statefulset has a label original_sts, this original sts is mapped with
							// each time a name changes by the autoscaler. This way we don't create or reconcile
							// another sts. Also at this stage if no state change is detected we don't want to change
							// up the replica count decided by the autoscaler. We will get the current replica count
							// and continue to pass on this to the CreateOrUpdate func(). If the cluster is scaling no
							// upgrade will be performed.
							stsName := k8sConfig.Name
							replicas := *nodeTypeNodeSpec.NodeSpec.Replicas
							if pbc.Status.EnableAutoscaling == true && pbc.Status.IngestorIndex[k8sConfig.Name] != "" {
								if nodeTypeNodeSpec.NodeType == v1.IngestorNodeType && pbc.Status.IngestorIndex != nil {
									stsName = pbc.Status.IngestorIndex[k8sConfig.Name]
									stsObj := appsv1.StatefulSet{}
									err := r.Get(ctx, types.NamespacedName{
										Namespace: pbc.Namespace,
										Name:      stsName,
									}, &stsObj)
									if err != nil {
										return fmt.Errorf("Failed to current replica count for STS [%s]", stsName)
									}
									replicas = *stsObj.Spec.Replicas

								}

								if pbc.Status.IsScaling {
									r.Recorder.Eventf(pbc, corev1.EventTypeNormal, pbcOperatorHaltWaitForEquilibrium, "Autoscaler in progress halt any parseable reconcilation, until equilibrium")
									return nil
								}
							}

							stsStatus, err := r.createOrUpdate(
								ctx,
								pbc,
								func() client.Object {
									return objects.MakeStatefulSet(
										pbc,
										stsName,
										replicas,
										&nodeTypeNodeSpec.NodeSpec,
										&k8sConfig,
										&parseableConfig,
										&envSecret,
									)
								},
								&appsv1.StatefulSet{},
								*ownerRef,
								r.Recorder,
							)
							if err != nil {
								return err
							}
							if stsStatus == controllerutil.OperationResultUpdated {
								return nil
							}
							// Ignore isObjFullyDeployed() for the first iteration ie cluster creation
							// will force cluster creation in parallel, post first iteration rolling updates
							// will be sequential.
							if pbc.Generation > 1 {
								done, err := r.isObjFullyDeployed(ctx,
									func() client.Object {
										return objects.MakeStatefulSet(
											pbc,
											stsName,
											replicas,
											&nodeTypeNodeSpec.NodeSpec,
											&k8sConfig,
											&parseableConfig,
											&envSecret,
										)
									})
								if !done {
									return err
								}
							}

						} else if nodeTypeNodeSpec.NodeSpec.Kind == "deployment" {
							//
						}

						for _, svc := range k8sConfig.Service {
							_, err := r.createOrUpdate(
								ctx,
								pbc,
								func() client.Object {
									return objects.MakeService(
										svc,
										pbc,
										k8sConfig.Name,
										nodeTypeNodeSpec.NodeSpec.Type,
									)
								},
								&corev1.Service{},
								*ownerRef,
								r.Recorder,
							)
							if err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// createOrUpdate ensures that the desired state of a Kubernetes resource is either created or updated.
// It compares the desired state with the current state and applies changes if necessary.
func (r *ParseableClusterReconciler) createOrUpdate(
	ctx context.Context,
	pbc *v1.ParseableCluster,
	desiredState func() client.Object,
	currentState client.Object,
	ownerRef metav1.OwnerReference,
	recorder record.EventRecorder,
) (controllerutil.OperationResult, error) {
	// Generate the desired state object
	ds := desiredState()
	// Add the owner reference to the object
	addOwnerRefToObject(ds, ownerRef)
	// Add a hash annotation to the object for tracking changes
	addHashToObject(ds, ownerRef.Kind+"OperatorHash")

	// Check if the object already exists in the cluster
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      ds.GetName(),
		Namespace: ds.GetNamespace()},
		currentState); err != nil {

		// If the object is not found, create it
		if apierrors.IsNotFound(err) {

			err := r.Client.Create(ctx, ds)
			if err != nil {
				recorder.Event(pbc, corev1.EventTypeWarning, string(pbcOperatorCreateFail), err.Error())
				return controllerutil.OperationResultNone, err
			}
			msg := fmt.Sprintf("parseable cluster object created successfully, ObjectName: [%s], ObjectKind: [%s], ObjectNamespace [%s] ", ds.GetName(), detectType(ds), ds.GetNamespace())
			recorder.Event(pbc, corev1.EventTypeNormal, string(pbcOperatorCreateSuccess), msg)
			return controllerutil.OperationResultCreated, nil

			// If there's another error, return it
		} else {
			recorder.Event(pbc, corev1.EventTypeWarning, string(pbcOperatorCreateFail), err.Error())
			return controllerutil.OperationResultNone, err
		}

		// If the object exists, check if it needs to be updated
	} else {
		if ds.GetAnnotations()[ownerRef.Kind+"OperatorHash"] != currentState.GetAnnotations()[ownerRef.Kind+"OperatorHash"] {

			ds.SetResourceVersion(currentState.GetResourceVersion())

			err := r.Client.Update(ctx, ds)
			if err != nil {
				recorder.Event(pbc, corev1.EventTypeWarning, string(pbcOperatorUpdateFail), err.Error())
				return controllerutil.OperationResultNone, err
			}
			msg := fmt.Sprintf("parseable cluster object updated successfully, ObjectName: [%s], ObjectKind: [%s], ObjectNamespace [%s] ", ds.GetName(), detectType(ds), ds.GetNamespace())
			recorder.Event(pbc, corev1.EventTypeNormal, string(pbcOperatorUpdateSuccess), msg)
			return controllerutil.OperationResultUpdated, nil
		} else {
			return controllerutil.OperationResultNone, nil
		}
	}
}

func (r *ParseableClusterReconciler) isObjFullyDeployed(
	ctx context.Context,
	obj func() client.Object,
) (bool, error) {

	dsobj := obj()
	// Get Object
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: dsobj.GetNamespace(), Name: dsobj.GetName()}, dsobj)
	if err != nil {
		return false, err
	}

	if detectType(dsobj) == "*v1.StatefulSet" {
		if dsobj.(*appsv1.StatefulSet).Status.CurrentRevision != dsobj.(*appsv1.StatefulSet).Status.UpdateRevision {
			return false, nil
		} else if dsobj.(*appsv1.StatefulSet).Status.CurrentReplicas != dsobj.(*appsv1.StatefulSet).Status.ReadyReplicas {
			return false, nil
		} else {
			return dsobj.(*appsv1.StatefulSet).Status.CurrentRevision == dsobj.(*appsv1.StatefulSet).Status.UpdateRevision, nil
		}
	} else if detectType(dsobj) == "*v1.Deployment" {
		for _, condition := range dsobj.(*appsv1.Deployment).Status.Conditions {
			// This detects a failure condition, operator should send a rolling deployment failed event
			if condition.Type == appsv1.DeploymentReplicaFailure {
				return false, errors.New(condition.Reason)
			} else if condition.Type == appsv1.DeploymentProgressing && condition.Status != corev1.ConditionTrue || dsobj.(*appsv1.Deployment).Status.ReadyReplicas != dsobj.(*appsv1.Deployment).Status.Replicas {
				return false, nil
			} else {
				return dsobj.(*appsv1.Deployment).Status.ReadyReplicas == dsobj.(*appsv1.Deployment).Status.Replicas, nil
			}
		}
	}
	return false, nil
}

// NodeTypeNodeSpec is a structure that pairs a NodeType with its corresponding NodeSpec.
type NodeTypeNodeSpec struct {
	NodeType v1.NodeType
	NodeSpec v1.NodeSpec
}

// getAllNodeSpecForNodeType constructs a list of NodeTypeNodeSpec objects
// based on the deployment order specified in the ParseableCluster.
func getAllNodeSpecForNodeType(pc *v1.ParseableCluster) []*NodeTypeNodeSpec {
	// Initialize a map to hold NodeSpecs by their NodeType
	nodeSpecsByNodeType := map[v1.NodeType][]*NodeTypeNodeSpec{}
	for _, t := range pc.Spec.DeploymentOrder {
		nodeSpecsByNodeType[v1.NodeType(t)] = []*NodeTypeNodeSpec{}
	}

	// Populate the map with NodeSpecs from the cluster specification
	for _, nodeSpec := range pc.Spec.Nodes {
		nodeSpecServiceSpec := nodeSpecsByNodeType[nodeSpec.Type]
		nodeSpecsByNodeType[nodeSpec.Type] = append(nodeSpecServiceSpec, &NodeTypeNodeSpec{
			NodeType: nodeSpec.Type,
			NodeSpec: nodeSpec,
		})
	}

	// Compile the final list of NodeTypeNodeSpecs in the deployment order
	allNodeSpecs := make([]*NodeTypeNodeSpec, 0, len(pc.Spec.Nodes))
	for _, t := range pc.Spec.DeploymentOrder {
		allNodeSpecs = append(allNodeSpecs, nodeSpecsByNodeType[v1.NodeType(t)]...)
	}

	return allNodeSpecs
}

func shouldPbControllerReconcile(sts *appsv1.StatefulSet) bool {
	for _, managedField := range sts.ManagedFields {
		if managedField.Manager == "pbscalercontroller" {
			// Check if the managed field includes "replicas"
			if managedField.FieldsV1 != nil && managedField.FieldsV1.Raw != nil {
				var fieldsMap map[string]interface{}
				if err := json.Unmarshal(managedField.FieldsV1.Raw, &fieldsMap); err != nil {
					continue
				}

				// Traverse the map to find f:spec -> f:replicas
				spec, specExists := fieldsMap["f:spec"].(map[string]interface{})
				if specExists {
					if _, replicasExists := spec["f:replicas"]; replicasExists {
						return false
					}
				}
			}
		}
	}
	return true
}

func patchReplicasManagedFields(ctx context.Context, k8sClient client.Client, pbc *v1.ParseableCluster) error {
	// DeepCopy the original object for creating the patch

	// Patch the custom resource with managed fields

	_, _, err := utils.PatchObj(ctx, k8sClient, pbc, func(obj client.Object) client.Object {
		in := obj.(*v1.ParseableCluster)
		in.Spec.DeploymentOrder[0] = "query"
		in.Spec.DeploymentOrder[1] = "ingestor"
		in.ObjectMeta.ManagedFields = []metav1.ManagedFieldsEntry{
			{
				Manager:    "parseableclusterautoscaler",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "parseable.com/v1",
				FieldsType: "FieldsV1",
				FieldsV1: &metav1.FieldsV1{
					Raw: []byte(`{
						"f:spec": {
							"f:nodes": {
								"f:0": {
									"f:replicas": {}
								}
							}
						}
					}`),
				},
			},
		}
		return in
	}, client.FieldOwner("parseableclusterautoscaler"))
	if err != nil {
		return err
	}

	return nil
}

func createManagedFields(replicas int32) map[string]interface{} {
	return map[string]interface{}{
		"manager":    "replica-manager-controller",
		"operation":  "Update",
		"apiVersion": "parseable.com/v1",
		"fieldsType": "FieldsV1",
		"fieldsV1": map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:nodes": map[string]interface{}{
					"f:0": map[string]interface{}{
						"f:replicas": replicas,
					},
				},
			},
		},
	}
}

// handleSuspend scales all deployments and statefulsets owned by this ParseableCluster to 0
func (r *ParseableClusterReconciler) handleSuspend(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Scale down StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster
		if isOwnedBy(&sts, ownerRef) {
			// Store original replicas as annotation if not already stored
			annotations := sts.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}

			// Only store original replicas if not already suspended
			if _, exists := annotations["parseable.com/original-replicas"]; !exists && *sts.Spec.Replicas > 0 {
				annotations["parseable.com/original-replicas"] = fmt.Sprintf("%d", *sts.Spec.Replicas)
				sts.SetAnnotations(annotations)
			}

			// Scale to 0
			zero := int32(0)
			sts.Spec.Replicas = &zero

			if err := r.Update(ctx, &sts); err != nil {
				return fmt.Errorf("failed to scale down statefulset %s: %w", sts.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "Suspended", "Scaled down StatefulSet %s to 0 replicas", sts.Name)
		}
	}

	// List all Deployments owned by this ParseableCluster
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Scale down Deployments
	for _, deploy := range deployments.Items {
		// Check if this Deployment is owned by our ParseableCluster
		if isOwnedBy(&deploy, ownerRef) {
			// Store original replicas as annotation if not already stored
			annotations := deploy.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}

			// Only store original replicas if not already suspended
			if _, exists := annotations["parseable.com/original-replicas"]; !exists && *deploy.Spec.Replicas > 0 {
				annotations["parseable.com/original-replicas"] = fmt.Sprintf("%d", *deploy.Spec.Replicas)
				deploy.SetAnnotations(annotations)
			}

			// Scale to 0
			zero := int32(0)
			deploy.Spec.Replicas = &zero

			if err := r.Update(ctx, &deploy); err != nil {
				return fmt.Errorf("failed to scale down deployment %s: %w", deploy.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "Suspended", "Scaled down Deployment %s to 0 replicas", deploy.Name)
		}
	}

	return nil
}

// handleResume restores all deployments and statefulsets owned by this ParseableCluster to their original replica counts
func (r *ParseableClusterReconciler) handleResume(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Restore StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster
		if isOwnedBy(&sts, ownerRef) {
			annotations := sts.GetAnnotations()
			if annotations != nil {
				if originalReplicas, exists := annotations["parseable.com/original-replicas"]; exists {
					// Parse original replica count
					var replicas int32
					fmt.Sscanf(originalReplicas, "%d", &replicas)

					// Restore replicas
					sts.Spec.Replicas = &replicas

					// Remove the annotation
					delete(annotations, "parseable.com/original-replicas")
					sts.SetAnnotations(annotations)

					if err := r.Update(ctx, &sts); err != nil {
						return fmt.Errorf("failed to restore statefulset %s: %w", sts.Name, err)
					}

					r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "Resumed", "Restored StatefulSet %s to %d replicas", sts.Name, replicas)
				}
			}
		}
	}

	// List all Deployments owned by this ParseableCluster
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Restore Deployments
	for _, deploy := range deployments.Items {
		// Check if this Deployment is owned by our ParseableCluster
		if isOwnedBy(&deploy, ownerRef) {
			annotations := deploy.GetAnnotations()
			if annotations != nil {
				if originalReplicas, exists := annotations["parseable.com/original-replicas"]; exists {
					// Parse original replica count
					var replicas int32
					fmt.Sscanf(originalReplicas, "%d", &replicas)

					// Restore replicas
					deploy.Spec.Replicas = &replicas

					// Remove the annotation
					delete(annotations, "parseable.com/original-replicas")
					deploy.SetAnnotations(annotations)

					if err := r.Update(ctx, &deploy); err != nil {
						return fmt.Errorf("failed to restore deployment %s: %w", deploy.Name, err)
					}

					r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "Resumed", "Restored Deployment %s to %d replicas", deploy.Name, replicas)
				}
			}
		}
	}

	return nil
}

func (r *ParseableClusterReconciler) handleSuspendIngestor(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Scale down only ingestor StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster and is an ingestor
		if isOwnedBy(&sts, ownerRef) && strings.Contains(sts.Name, "ingestor") {
			// Scale to 0
			zero := int32(0)
			sts.Spec.Replicas = &zero

			if err := r.Update(ctx, &sts); err != nil {
				return fmt.Errorf("failed to scale down ingestor statefulset %s: %w", sts.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "SuspendedIngestor", "Scaled down ingestor StatefulSet %s to 0 replicas", sts.Name)
		}
	}

	// List all Deployments owned by this ParseableCluster (in case ingestors are deployments)
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Scale down only ingestor Deployments
	for _, deploy := range deployments.Items {
		// Check if this Deployment is owned by our ParseableCluster and is an ingestor
		if isOwnedBy(&deploy, ownerRef) && strings.Contains(deploy.Name, "ingestor") {
			// Scale to 0
			zero := int32(0)
			deploy.Spec.Replicas = &zero

			if err := r.Update(ctx, &deploy); err != nil {
				return fmt.Errorf("failed to scale down ingestor deployment %s: %w", deploy.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "SuspendedIngestor", "Scaled down ingestor Deployment %s to 0 replicas", deploy.Name)
		}
	}

	return nil
}

func (r *ParseableClusterReconciler) handleResumeIngestor(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// Get ingestor replica count from the ParseableCluster spec
	var ingestorReplicas int32 = 1 // default to 1 if not found
	for _, node := range pbc.Spec.Nodes {
		if node.Type == "ingestor" || node.Type == "ingest" {
			if node.Replicas != nil {
				ingestorReplicas = *node.Replicas
			}
			break
		}
	}

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Restore only ingestor StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster and is an ingestor
		if isOwnedBy(&sts, ownerRef) && strings.Contains(sts.Name, "ingestor") {
			// Restore replicas
			sts.Spec.Replicas = &ingestorReplicas

			if err := r.Update(ctx, &sts); err != nil {
				return fmt.Errorf("failed to restore ingestor statefulset %s: %w", sts.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "ResumedIngestor", "Restored ingestor StatefulSet %s to %d replicas", sts.Name, ingestorReplicas)
		}
	}

	// List all Deployments owned by this ParseableCluster (in case ingestors are deployments)
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Restore only ingestor Deployments
	for _, deploy := range deployments.Items {
		// Check if this Deployment is owned by our ParseableCluster and is an ingestor
		if isOwnedBy(&deploy, ownerRef) && strings.Contains(deploy.Name, "ingestor") {
			// Restore replicas
			deploy.Spec.Replicas = &ingestorReplicas

			if err := r.Update(ctx, &deploy); err != nil {
				return fmt.Errorf("failed to restore ingestor deployment %s: %w", deploy.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "ResumedIngestor", "Restored ingestor Deployment %s to %d replicas", deploy.Name, ingestorReplicas)
		}
	}

	return nil
}

func (r *ParseableClusterReconciler) handleSuspendQuerier(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Scale down only querier StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster and is a querier
		if isOwnedBy(&sts, ownerRef) && strings.Contains(sts.Name, "parseable-query") {
			// Scale to 0
			zero := int32(0)
			sts.Spec.Replicas = &zero

			if err := r.Update(ctx, &sts); err != nil {
				return fmt.Errorf("failed to scale down querier statefulset %s: %w", sts.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "SuspendedQuerier", "Scaled down querier StatefulSet %s to 0 replicas", sts.Name)
		}
	}

	return nil
}

func (r *ParseableClusterReconciler) handleResumeQuerier(ctx context.Context, pbc *v1.ParseableCluster, ownerRef *metav1.OwnerReference) error {
	namespace := pbc.Namespace

	// Get querier replica count from the ParseableCluster spec
	var querierReplicas int32 = 1 // default to 1 if not found
	for _, node := range pbc.Spec.Nodes {
		if node.Type == "querier" {
			if node.Replicas != nil {
				querierReplicas = *node.Replicas
			}
			break
		}
	}

	// List all StatefulSets owned by this ParseableCluster
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// Restore only querier StatefulSets
	for _, sts := range statefulSets.Items {
		// Check if this StatefulSet is owned by our ParseableCluster and is a querier
		if isOwnedBy(&sts, ownerRef) && strings.Contains(sts.Name, "parseable-query") {
			// Restore replicas
			sts.Spec.Replicas = &querierReplicas

			if err := r.Update(ctx, &sts); err != nil {
				return fmt.Errorf("failed to restore querier statefulset %s: %w", sts.Name, err)
			}

			r.Recorder.Eventf(pbc, corev1.EventTypeNormal, "ResumedQuerier", "Restored querier StatefulSet %s to %d replicas", sts.Name, querierReplicas)
		}
	}

	return nil
}

// isOwnedBy checks if the object is owned by the given owner reference
func isOwnedBy(obj client.Object, ownerRef *metav1.OwnerReference) bool {
	for _, owner := range obj.GetOwnerReferences() {
		if owner.APIVersion == ownerRef.APIVersion &&
			owner.Kind == ownerRef.Kind &&
			owner.Name == ownerRef.Name &&
			owner.UID == ownerRef.UID {
			return true
		}
	}
	return false
}
