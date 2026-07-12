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
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "parseablehq/parseable-operator/api/v1"
)

// ParseableClusterChoasReconciler reconciles a ParseableClusterChoas object
type ParseableClusterChaosReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ReconcileWait    time.Duration
	metricsClientset *metricsclient.Clientset
	Recorder         record.EventRecorder
}

func NewParseableClusterChaosReconciler(mgr ctrl.Manager) *ParseableClusterChaosReconciler {
	metricsClient, err := metricsclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		fmt.Printf("Failed to initalise metrics client for parseable cluster autoscaler")
		os.Exit(1)
	}
	return &ParseableClusterChaosReconciler{
		Client:           mgr.GetClient(),
		metricsClientset: metricsClient,
		Scheme:           mgr.GetScheme(),
		ReconcileWait:    lookupReconcileTime(),
		Recorder:         mgr.GetEventRecorderFor("parseableclusterchaos-controller"),
	}
}

// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusterschaos,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusterschaos/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=parseable.com,resources=parseableclusterschaos/finalizers,verbs=update
// +kubebuilder:resource:shortNames=pbcc;pbccs
func (r *ParseableClusterChaosReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logr := log.FromContext(ctx)

	parseableClusterChaosCR := &v1.ParseableClusterChaos{}
	err := r.Get(context.TODO(), req.NamespacedName, parseableClusterChaosCR)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := r.do(ctx, parseableClusterChaosCR); err != nil {
		logr.Error(err, err.Error())
		return ctrl.Result{}, err
	} else {
		return ctrl.Result{RequeueAfter: r.ReconcileWait}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ParseableClusterChaosReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.ParseableClusterChaos{}).
		//WithEventFilter(GenericPredicates{}).
		Complete(r)
}

func lookupReconcileTime() time.Duration {
	val, exists := os.LookupEnv("PB_CHAOS_RECONCILE_WAIT")
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
