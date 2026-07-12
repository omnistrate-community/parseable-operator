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
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "parseablehq/parseable-operator/api/v1"
)

// ParseableClusterReconciler reconciles a ParseableCluster object
type ParseableClusterReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ReconcileWait time.Duration
	Recorder      record.EventRecorder
}

func NewParseableClusterReconciler(mgr ctrl.Manager) *ParseableClusterReconciler {
	return &ParseableClusterReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ReconcileWait: lookupReconcileTime(),
		Recorder:      mgr.GetEventRecorderFor("parseablecluster-controller"),
	}
}

// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusters/finalizers,verbs=update
// +kubebuilder:resource:shortNames=pbc;pbcs
func (r *ParseableClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logr := log.FromContext(ctx)

	parseableCR := &v1.ParseableCluster{}
	err := r.Get(context.TODO(), req.NamespacedName, parseableCR)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := r.do(ctx, parseableCR); err != nil {
		logr.Error(err, err.Error())
		return ctrl.Result{}, err
	} else {
		return ctrl.Result{RequeueAfter: r.ReconcileWait}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ParseableClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.ParseableCluster{}).
		WithEventFilter(GenericPredicates{}).
		Complete(r)
}

func lookupReconcileTime() time.Duration {
	val, exists := os.LookupEnv("PB_RECONCILE_WAIT")
	if !exists {
		return time.Second * 10
	} else {
		v, err := time.ParseDuration(val)
		if err != nil {
			// Exit Program if not valid
			os.Exit(1)
		}
		return v
	}
}
