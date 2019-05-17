package flinkapplication

import (
	"context"

	"github.com/lyft/flinkk8soperator/pkg/apis/app/v1alpha1"
	"github.com/lyft/flinkk8soperator/pkg/controller/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"time"

	"github.com/lyft/flinkk8soperator/pkg/controller/k8"
	"github.com/lyft/flytestdlib/contextutils"
	"github.com/lyft/flytestdlib/logger"
	v1 "k8s.io/api/apps/v1"
	coreV1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileFlinkApplication reconciles a FlinkApplication resource
type ReconcileFlinkApplication struct {
	client            client.Client
	cache             cache.Cache
	flinkStateMachine FlinkHandlerInterface
}

func (r *ReconcileFlinkApplication) getResource(ctx context.Context, key types.NamespacedName, obj runtime.Object) error {
	err := r.cache.Get(ctx, key, obj)
	if err != nil && k8.IsK8sObjectNotExists(err) {
		return r.client.Get(ctx, key, obj)
	}

	return nil
}

// For failures, we do not want to retry immediately, as we want the underlying resource to recover.
// At the same time, we want to retry faster than the regular success interval.
func (r *ReconcileFlinkApplication) getFailureRetryInterval() time.Duration {
	return config.GetConfig().ResyncPeriod.Duration / 2
}

func (r *ReconcileFlinkApplication) getReconcileResultForError(err error) reconcile.Result {
	if err == nil {
		return reconcile.Result{}
	}
	return reconcile.Result{
		RequeueAfter: r.getFailureRetryInterval(),
	}
}

func (r *ReconcileFlinkApplication) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	ctx := context.Background()
	ctx = contextutils.WithNamespace(ctx, request.Namespace)
	ctx = contextutils.WithAppName(ctx, request.Name)
	typeMeta := metaV1.TypeMeta{
		Kind:       v1alpha1.FlinkApplicationKind,
		APIVersion: v1alpha1.SchemeGroupVersion.String(),
	}
	// Fetch the FlinkApplication instance
	instance := &v1alpha1.FlinkApplication{
		TypeMeta: typeMeta,
	}

	err := r.getResource(ctx, request.NamespacedName, instance)
	if err != nil {
		if k8.IsK8sObjectNotExists(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - we will check again in next loop
		return r.getReconcileResultForError(err), nil
	}
	// We are seeing instances where getResource is removing TypeMeta
	instance.TypeMeta = typeMeta
	ctx = contextutils.WithPhase(ctx, string(instance.Status.Phase))
	err = r.flinkStateMachine.Handle(ctx, instance)
	if err != nil {
		if errors.IsConflict(err) {
			return reconcile.Result{
				RequeueAfter: config.GetConfig().ResyncPeriod.Duration,
			}, err
		}
		logger.Warnf(ctx, "Failed to reconcile for object [%v]", err)
	}
	return r.getReconcileResultForError(err), err
}

// Add creates a new FlinkApplication Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(ctx context.Context, mgr manager.Manager, cfg config.RuntimeConfig) error {
	k8sCluster := k8.NewK8Cluster(mgr)
	flinkStateMachine := NewFlinkStateMachine(k8sCluster, cfg)

	reconciler := ReconcileFlinkApplication{
		client:            mgr.GetClient(),
		cache:             mgr.GetCache(),
		flinkStateMachine: flinkStateMachine,
	}

	c, err := controller.New("flinkAppController", mgr, controller.Options{
		MaxConcurrentReconciles: config.GetConfig().Workers,
		Reconciler:              &reconciler,
	})

	if err != nil {
		return err
	}

	if err = c.Watch(&source.Kind{Type: &v1alpha1.FlinkApplication{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return err
	}

	// Watch deployments and services for the application
	if err := c.Watch(&source.Kind{Type: &v1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &v1alpha1.FlinkApplication{},
	}, getPredicateFuncs()); err != nil {
		return err
	}

	if err := c.Watch(&source.Kind{Type: &coreV1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &v1alpha1.FlinkApplication{},
	}, getPredicateFuncs()); err != nil {
		return err
	}
	return nil
}

func isOwnedByFlinkApplication(ownerReferences []metaV1.OwnerReference) bool {
	for _, ownerReference := range ownerReferences {
		if ownerReference.APIVersion == v1alpha1.SchemeGroupVersion.String() &&
			ownerReference.Kind == v1alpha1.FlinkApplicationKind {
			return true
		}
	}
	return false
}

// Predicate filters events before enqueuing the keys.
// We are only interested in kubernetes objects that are owned by the FlinkApplication
// This filters all the objects, and ensures only subset is
func getPredicateFuncs() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isOwnedByFlinkApplication(e.Meta.GetOwnerReferences())
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isOwnedByFlinkApplication(e.MetaNew.GetOwnerReferences())
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isOwnedByFlinkApplication(e.Meta.GetOwnerReferences())
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isOwnedByFlinkApplication(e.Meta.GetOwnerReferences())
		},
	}
}