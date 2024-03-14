/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1 "github.com/imwithye/namespaceclass/api/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NamespaceClassReconciler reconciles a NamespaceClass object
type NamespaceClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ops.akuity.io,resources=namespaceclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ops.akuity.io,resources=namespaceclasses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ops.akuity.io,resources=namespaceclasses/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NamespaceClass object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.0/pkg/reconcile
func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, ns); err == nil {
		return r.reconcileNamespace(ctx, ns)
	}

	ncls := &opsv1.NamespaceClass{}
	if err := r.Get(ctx, req.NamespacedName, ncls); err == nil {
		return r.reconcileNamespaceClass(ctx, ncls)
	}

	return ctrl.Result{}, nil
}

// reconcileNamespace reconciles a Namespace object
func (r *NamespaceClassReconciler) reconcileNamespace(ctx context.Context, ns *corev1.Namespace) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling namespace", "name", ns.Name)

	nclsName, ok := ns.Labels["namespaceclass.akuity.io/name"]
	if !ok {
		// the namespace is either not managed by a namespace class or the namespace class is removed
		// when namespace class is removed, we shall delete the resources
		// setting the spec to nil will trigger the deletion of the resource
		return r.reconcileResource(ctx, nil, ns.Name, "ncls-networkpolicy", &networkingv1.NetworkPolicy{}, nil)
	}

	ncls := &opsv1.NamespaceClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: nclsName}, ncls); err != nil {
		if apierrors.IsNotFound(err) {
			// the namespace class is removed
			// setting the spec to nil will trigger the deletion of the resource
			return r.reconcileResource(ctx, nil, ns.Name, "ncls-networkpolicy", &networkingv1.NetworkPolicy{}, nil)
		}
		logger.Error(err, "unable to get namespace class", "name", nclsName)
		return ctrl.Result{}, err
	}

	// reconcile the namespace
	return r.reconcileResource(ctx, ncls, ns.Name, "ncls-networkpolicy", &networkingv1.NetworkPolicy{}, ncls.Spec.NetworkPolicy)
}

// reconcileNamespaceClass reconciles a NamespaceClass object
func (r *NamespaceClassReconciler) reconcileNamespaceClass(ctx context.Context, ncls *opsv1.NamespaceClass) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling namespace class", "name", ncls.Name)

	namespaces := corev1.NamespaceList{}
	if err := r.List(ctx, &namespaces); err != nil {
		logger.Error(err, "unable to list namespaces")
		return ctrl.Result{}, err
	}

	for _, namespace := range namespaces.Items {
		nclsName, ok := namespace.Labels["namespaceclass.akuity.io/name"]
		if !ok || nclsName != ncls.Name {
			// the namespace is not managed by this namespace class
			continue
		}

		// reconcile the namespace
		result, err := ctrl.Result{}, error(nil)

		result, err = r.reconcileResource(ctx, ncls, namespace.Name, "ncls-networkpolicy",
			&networkingv1.NetworkPolicy{}, ncls.Spec.NetworkPolicy)
		if err != nil {
			return result, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *NamespaceClassReconciler) reconcileResource(ctx context.Context, ncls *opsv1.NamespaceClass, resNamespace string, resName string,
	resObj client.Object, resSpec interface{}) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	namespacedName := types.NamespacedName{Namespace: resNamespace, Name: resName}
	err := r.Get(ctx, namespacedName, resObj)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "unable to get resource", "namespacedName", namespacedName)
		return ctrl.Result{}, err
	}

	// delete the resource
	if (resSpec == nil || reflect.ValueOf(resSpec).IsNil()) && err == nil {
		if err := r.Delete(ctx, resObj); err != nil {
			logger.Error(err, "unable to delete resource", "namespacedName", namespacedName)
			return ctrl.Result{}, err
		}
		logger.Info("resource deleted", "namespacedName", namespacedName)
		return ctrl.Result{}, nil
	}

	// create the resource
	if !(resSpec == nil || reflect.ValueOf(resSpec).IsNil()) && err != nil {
		resObj.SetNamespace(resNamespace)
		resObj.SetName(resName)
		resObj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: ncls.APIVersion, Kind: ncls.Kind, Name: ncls.Name, UID: ncls.GetUID()}})
		reflect.ValueOf(resObj).Elem().FieldByName("Spec").Set(reflect.ValueOf(resSpec).Elem())
		if err := r.Create(ctx, resObj); err != nil {
			logger.Error(err, "unable to create resource", "namespacedName", namespacedName)
			return ctrl.Result{}, err
		}
		logger.Info("resource created", "namespacedName", namespacedName)
		return ctrl.Result{}, nil
	}

	// update the resource
	if !(resSpec == nil || reflect.ValueOf(resSpec).IsNil()) && err == nil {
		resObj.SetNamespace(resNamespace)
		resObj.SetName(resName)
		resObj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: ncls.APIVersion, Kind: ncls.Kind, Name: ncls.Name, UID: ncls.GetUID()}})
		reflect.ValueOf(resObj).Elem().FieldByName("Spec").Set(reflect.ValueOf(resSpec).Elem())
		if err := r.Update(ctx, resObj); err != nil {
			logger.Error(err, "unable to update resource", "namespacedName", namespacedName)
			return ctrl.Result{}, err
		}
		logger.Info("resource updated", "namespacedName", namespacedName)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1.NamespaceClass{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Watches(&corev1.Namespace{}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}
