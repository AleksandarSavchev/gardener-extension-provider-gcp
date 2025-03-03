// Copyright (c) 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package worker

import (
	"context"
	"fmt"

	extensionscontroller "github.com/gardener/gardener/extensions/pkg/controller"
	"github.com/gardener/gardener/extensions/pkg/controller/common"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	gardencorev1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/controllerutils"
	reconcilerutils "github.com/gardener/gardener/pkg/controllerutils/reconciler"
	kutil "github.com/gardener/gardener/pkg/utils/kubernetes"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/runtime/inject"
)

type reconciler struct {
	actuator        Actuator
	watchdogManager common.WatchdogManager

	client        client.Client
	reader        client.Reader
	statusUpdater extensionscontroller.StatusUpdater
}

// NewReconciler creates a new reconcile.Reconciler that reconciles
// Worker resources of Gardener's `extensions.gardener.cloud` API group.
func NewReconciler(actuator Actuator, watchdogManager common.WatchdogManager) reconcile.Reconciler {
	return reconcilerutils.OperationAnnotationWrapper(
		func() client.Object { return &extensionsv1alpha1.Worker{} },
		&reconciler{
			actuator:        actuator,
			watchdogManager: watchdogManager,
			statusUpdater:   extensionscontroller.NewStatusUpdater(),
		},
	)
}

func (r *reconciler) InjectFunc(f inject.Func) error {
	return f(r.actuator)
}

func (r *reconciler) InjectClient(client client.Client) error {
	r.client = client
	r.statusUpdater.InjectClient(client)
	return nil
}

func (r *reconciler) InjectAPIReader(reader client.Reader) error {
	r.reader = reader
	return nil
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	worker := &extensionsv1alpha1.Worker{}
	if err := r.client.Get(ctx, request.NamespacedName, worker); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Object is gone, stop reconciling")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("error retrieving object from store: %w", err)
	}

	cluster, err := extensionscontroller.GetCluster(ctx, r.client, worker.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}

	if extensionscontroller.IsFailed(cluster) {
		log.Info("Skipping of the reconciliation of Worker of failed shoot")
		return reconcile.Result{}, nil
	}

	operationType := gardencorev1beta1helper.ComputeOperationType(worker.ObjectMeta, worker.Status.LastOperation)

	if cluster.Shoot != nil && operationType != gardencorev1beta1.LastOperationTypeMigrate {
		key := "worker:" + kutil.ObjectName(worker)
		ok, watchdogCtx, cleanup, err := r.watchdogManager.GetResultAndContext(ctx, r.client, worker.Namespace, cluster.Shoot.Name, key)
		if err != nil {
			return reconcile.Result{}, err
		} else if !ok {
			return reconcile.Result{}, fmt.Errorf("this seed is not the owner of shoot %s", kutil.ObjectName(cluster.Shoot))
		}
		ctx = watchdogCtx
		if cleanup != nil {
			defer cleanup()
		}
	}

	switch {
	case extensionscontroller.ShouldSkipOperation(operationType, worker):
		return reconcile.Result{}, nil
	case operationType == gardencorev1beta1.LastOperationTypeMigrate:
		return r.migrate(ctx, log.WithValues("operation", "migrate"), worker, cluster)
	case worker.DeletionTimestamp != nil:
		return r.delete(ctx, log.WithValues("operation", "delete"), worker, cluster)
	case operationType == gardencorev1beta1.LastOperationTypeRestore:
		return r.restore(ctx, log.WithValues("operation", "restore"), worker, cluster)
	default:
		return r.reconcile(ctx, log.WithValues("operation", "reconcile"), worker, cluster, operationType)
	}
}

func (r *reconciler) removeFinalizerFromWorker(ctx context.Context, log logr.Logger, worker *extensionsv1alpha1.Worker) error {
	if controllerutil.ContainsFinalizer(worker, FinalizerName) {
		log.Info("Removing finalizer")
		if err := controllerutils.RemoveFinalizers(ctx, r.client, worker, FinalizerName); err != nil {
			return fmt.Errorf("failed to remove finalizer: %w", err)
		}
	}
	return nil
}

func (r *reconciler) removeAnnotation(ctx context.Context, worker *extensionsv1alpha1.Worker) error {
	return extensionscontroller.RemoveAnnotation(ctx, r.client, worker, v1beta1constants.GardenerOperation)
}

func (r *reconciler) migrate(
	ctx context.Context,
	log logr.Logger,
	worker *extensionsv1alpha1.Worker,
	cluster *extensionscontroller.Cluster,
) (
	reconcile.Result,
	error,
) {
	if err := r.statusUpdater.Processing(ctx, log, worker, gardencorev1beta1.LastOperationTypeMigrate, "Starting migration of the Worker"); err != nil {
		return reconcile.Result{}, err
	}

	log.Info("Starting the migration of Worker")
	if err := r.actuator.Migrate(ctx, log, worker, cluster); err != nil {
		_ = r.statusUpdater.Error(ctx, log, worker, err, gardencorev1beta1.LastOperationTypeMigrate, "Error migrating Worker")
		return reconcilerutils.ReconcileErr(err)
	}

	if err := r.statusUpdater.Success(ctx, log, worker, gardencorev1beta1.LastOperationTypeMigrate, "Successfully migrated Worker"); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.removeFinalizerFromWorker(ctx, log, worker); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.removeAnnotation(ctx, worker); err != nil {
		return reconcile.Result{}, fmt.Errorf("error removing annotation from Worker: %+v", err)
	}

	return reconcile.Result{}, nil
}

func (r *reconciler) delete(
	ctx context.Context,
	log logr.Logger,
	worker *extensionsv1alpha1.Worker,
	cluster *extensionscontroller.Cluster,
) (
	reconcile.Result,
	error,
) {
	if !controllerutil.ContainsFinalizer(worker, FinalizerName) {
		log.Info("Deleting Worker causes a no-op as there is no finalizer")
		return reconcile.Result{}, nil
	}

	if err := r.statusUpdater.Processing(ctx, log, worker, gardencorev1beta1.LastOperationTypeDelete, "Deleting the Worker"); err != nil {
		return reconcile.Result{}, err
	}

	log.Info("Starting the deletion of worker")
	if err := r.actuator.Delete(ctx, log, worker, cluster); err != nil {
		_ = r.statusUpdater.Error(ctx, log, worker, err, gardencorev1beta1.LastOperationTypeDelete, "Error deleting Worker")
		return reconcilerutils.ReconcileErr(err)
	}

	if err := r.statusUpdater.Success(ctx, log, worker, gardencorev1beta1.LastOperationTypeDelete, "Successfully deleted Worker"); err != nil {
		return reconcile.Result{}, err
	}

	err := r.removeFinalizerFromWorker(ctx, log, worker)
	return reconcile.Result{}, err
}

func (r *reconciler) reconcile(
	ctx context.Context,
	log logr.Logger,
	worker *extensionsv1alpha1.Worker,
	cluster *extensionscontroller.Cluster,
	operationType gardencorev1beta1.LastOperationType,
) (
	reconcile.Result,
	error,
) {
	if !controllerutil.ContainsFinalizer(worker, FinalizerName) {
		log.Info("Adding finalizer")
		if err := controllerutils.AddFinalizers(ctx, r.client, worker, FinalizerName); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	if err := r.statusUpdater.Processing(ctx, log, worker, operationType, "Reconciling the Worker"); err != nil {
		return reconcile.Result{}, err
	}
	log.Info("Starting the reconciliation of worker")
	if err := r.actuator.Reconcile(ctx, log, worker, cluster); err != nil {
		_ = r.statusUpdater.Error(ctx, log, worker, err, operationType, "Error reconciling Worker")
		return reconcilerutils.ReconcileErr(err)
	}

	if err := r.statusUpdater.Success(ctx, log, worker, operationType, "Successfully reconciled Worker"); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *reconciler) restore(
	ctx context.Context,
	log logr.Logger,
	worker *extensionsv1alpha1.Worker,
	cluster *extensionscontroller.Cluster,
) (
	reconcile.Result,
	error,
) {
	if !controllerutil.ContainsFinalizer(worker, FinalizerName) {
		log.Info("Adding finalizer")
		if err := controllerutils.AddFinalizers(ctx, r.client, worker, FinalizerName); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	if err := r.statusUpdater.Processing(ctx, log, worker, gardencorev1beta1.LastOperationTypeRestore, "Restoring the Worker"); err != nil {
		return reconcile.Result{}, err
	}

	log.Info("Starting the restoration of worker")
	if err := r.actuator.Restore(ctx, log, worker, cluster); err != nil {
		_ = r.statusUpdater.Error(ctx, log, worker, err, gardencorev1beta1.LastOperationTypeRestore, "Error restoring Worker")
		return reconcilerutils.ReconcileErr(err)
	}

	if err := r.statusUpdater.Success(ctx, log, worker, gardencorev1beta1.LastOperationTypeRestore, "Successfully reconciled Worker"); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.removeAnnotation(ctx, worker); err != nil {
		return reconcile.Result{}, fmt.Errorf("error removing annotation from Worker: %+v", err)
	}

	return reconcile.Result{}, nil
}

func isWorkerMigrated(worker *extensionsv1alpha1.Worker) bool {
	return worker.Status.LastOperation != nil &&
		worker.Status.LastOperation.Type == gardencorev1beta1.LastOperationTypeMigrate &&
		worker.Status.LastOperation.State == gardencorev1beta1.LastOperationStateSucceeded
}
