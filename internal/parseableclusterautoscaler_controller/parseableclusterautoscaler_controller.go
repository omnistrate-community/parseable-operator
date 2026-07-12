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
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "parseablehq/parseable-operator/api/v1"
	"parseablehq/parseable-operator/pkg/utils"
)

// ParseableClusterAutoscalerReconciler reconciles a ParseableCluster object
type ParseableClusterAutoscalerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ReconcileWait    time.Duration
	Recorder         record.EventRecorder
	metricsClientset *metricsclient.Clientset
}

func NewParseableClusterAutoscalerReconciler(mgr ctrl.Manager) *ParseableClusterAutoscalerReconciler {
	metricsClient, err := metricsclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		fmt.Printf("Failed to initalise metrics client for parseable cluster autoscaler")
		os.Exit(1)
	}
	return &ParseableClusterAutoscalerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		metricsClientset: metricsClient,
		ReconcileWait:    lookupReconcileTime(),
		Recorder:         mgr.GetEventRecorderFor("parseableclusterautoscaler-controller"),
	}
}

// +kubebuilder:rbac:groups=parseable.com,resources=parseableclustersautosaclers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclustersautoscalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclustersautoscalers/finalizers,verbs=update
// +kubebuilder:resource:shortNames=pbca;pbcas
func (r *ParseableClusterAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logr := log.FromContext(ctx)

	parseableAutoscalerCR := &v1.ParseableClusterAutoscaler{}
	err := r.Get(context.TODO(), req.NamespacedName, parseableAutoscalerCR)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	parseableCR := &v1.ParseableCluster{}
	err = r.Get(context.TODO(), types.NamespacedName{
		Name:      parseableAutoscalerCR.Spec.ScaleTargetRef.Name,
		Namespace: req.Namespace,
	}, parseableCR)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if parseableCR.GetDeletionTimestamp() != nil {
		logr.Info("Deletion Timestamp set on parseable cluster CR")
		return ctrl.Result{}, nil
	}

	if parseableAutoscalerCR.Spec.Stop {
		_, _, err = utils.PatchStatus(ctx, r.Client, parseableCR, func(obj client.Object) client.Object {
			in := obj.(*v1.ParseableCluster)
			in.Status.EnableAutoscaling = false
			return in
		})
		logr.Info("Autoscaler is stopped.")
		return ctrl.Result{}, nil
	}
	_, _, err = utils.PatchStatus(ctx, r.Client, parseableCR, func(obj client.Object) client.Object {
		in := obj.(*v1.ParseableCluster)
		in.Status.EnableAutoscaling = true
		return in
	})

	if err := r.do(ctx, parseableAutoscalerCR, parseableCR); err != nil {
		logr.Error(err, err.Error())
		return ctrl.Result{}, err
	} else {
		return ctrl.Result{RequeueAfter: r.ReconcileWait}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ParseableClusterAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.ParseableClusterAutoscaler{}).
		//WithEventFilter(GenericPredicates{}).
		Complete(r)
}

func lookupReconcileTime() time.Duration {
	val, exists := os.LookupEnv("PB_AUTOSCALER_RECONCILE_WAIT")
	if !exists {
		return time.Second * 20
	} else {
		v, err := time.ParseDuration(val)
		if err != nil {
			// Exit Program if not valid
			os.Exit(1)
		}
		return v
	}
}
