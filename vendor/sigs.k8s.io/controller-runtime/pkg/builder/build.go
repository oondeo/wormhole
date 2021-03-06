/*
Copyright 2018 The Kubernetes Authors.

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

package builder

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Supporting mocking out functions for testing
var getConfig = config.GetConfig
var newController = controller.New
var newManager = manager.New
var getGvk = apiutil.GVKForObject

// Builder builds a Controller.
type Builder struct {
	apiType        runtime.Object
	mgr            manager.Manager
	predicates     []predicate.Predicate
	managedObjects []runtime.Object
	watchRequest   []watchRequest
	config         *rest.Config
	ctrl           controller.Controller
}

// SimpleController returns a new Builder.
//
// Deprecated: Use ControllerManagedBy(Manager) instead.
func SimpleController() *Builder {
	return &Builder{}
}

// ControllerManagedBy returns a new controller builder that will be started by the provided Manager
func ControllerManagedBy(m manager.Manager) *Builder {
	return SimpleController().WithManager(m)
}

// ForType defines the type of Object being *reconciled*, and configures the ControllerManagedBy to respond to create / delete /
// update events by *reconciling the object*.
// This is the equivalent of calling
// Watches(&source.Kind{Type: apiType}, &handler.EnqueueRequestForObject{})
// If the passed in object has implemented the admission.Defaulter interface, a MutatingWebhook will be wired for this type.
// If the passed in object has implemented the admission.Validator interface, a ValidatingWebhook will be wired for this type.
//
// Deprecated: Use For
func (blder *Builder) ForType(apiType runtime.Object) *Builder {
	return blder.For(apiType)
}

// For defines the type of Object being *reconciled*, and configures the ControllerManagedBy to respond to create / delete /
// update events by *reconciling the object*.
// This is the equivalent of calling
// Watches(&source.Kind{Type: apiType}, &handler.EnqueueRequestForObject{})
// If the passed in object has implemented the admission.Defaulter interface, a MutatingWebhook will be wired for this type.
// If the passed in object has implemented the admission.Validator interface, a ValidatingWebhook will be wired for this type.
func (blder *Builder) For(apiType runtime.Object) *Builder {
	blder.apiType = apiType
	return blder
}

// Owns defines types of Objects being *generated* by the ControllerManagedBy, and configures the ControllerManagedBy to respond to
// create / delete / update events by *reconciling the owner object*.  This is the equivalent of calling
// Watches(&handler.EnqueueRequestForOwner{&source.Kind{Type: <ForType-apiType>}, &handler.EnqueueRequestForOwner{OwnerType: apiType, IsController: true})
func (blder *Builder) Owns(apiType runtime.Object) *Builder {
	blder.managedObjects = append(blder.managedObjects, apiType)
	return blder
}

type watchRequest struct {
	src          source.Source
	eventhandler handler.EventHandler
}

// Watches exposes the lower-level ControllerManagedBy Watches functions through the builder.  Consider using
// Owns or For instead of Watches directly.
func (blder *Builder) Watches(src source.Source, eventhandler handler.EventHandler) *Builder {
	blder.watchRequest = append(blder.watchRequest, watchRequest{src: src, eventhandler: eventhandler})
	return blder
}

// WithConfig sets the Config to use for configuring clients.  Defaults to the in-cluster config or to ~/.kube/config.
//
// Deprecated: Use ControllerManagedBy(Manager) and this isn't needed.
func (blder *Builder) WithConfig(config *rest.Config) *Builder {
	blder.config = config
	return blder
}

// WithManager sets the Manager to use for registering the ControllerManagedBy.  Defaults to a new manager.Manager.
//
// Deprecated: Use ControllerManagedBy(Manager) and this isn't needed.
func (blder *Builder) WithManager(m manager.Manager) *Builder {
	blder.mgr = m
	return blder
}

// WithEventFilter sets the event filters, to filter which create/update/delete/generic events eventually
// trigger reconciliations.  For example, filtering on whether the resource version has changed.
// Defaults to the empty list.
func (blder *Builder) WithEventFilter(p predicate.Predicate) *Builder {
	blder.predicates = append(blder.predicates, p)
	return blder
}

// Complete builds the Application ControllerManagedBy.
func (blder *Builder) Complete(r reconcile.Reconciler) error {
	_, err := blder.Build(r)
	return err
}

// Build builds the Application ControllerManagedBy and returns the Manager used to start it.
//
// Deprecated: Use Complete
func (blder *Builder) Build(r reconcile.Reconciler) (manager.Manager, error) {
	if r == nil {
		return nil, fmt.Errorf("must provide a non-nil Reconciler")
	}

	// Set the Config
	if err := blder.doConfig(); err != nil {
		return nil, err
	}

	// Set the Manager
	if err := blder.doManager(); err != nil {
		return nil, err
	}

	// Set the ControllerManagedBy
	if err := blder.doController(r); err != nil {
		return nil, err
	}

	// Set the Webook if needed
	if err := blder.doWebhook(); err != nil {
		return nil, err
	}

	// Set the Watch
	if err := blder.doWatch(); err != nil {
		return nil, err
	}

	return blder.mgr, nil
}

func (blder *Builder) doWatch() error {
	// Reconcile type
	src := &source.Kind{Type: blder.apiType}
	hdler := &handler.EnqueueRequestForObject{}
	err := blder.ctrl.Watch(src, hdler, blder.predicates...)
	if err != nil {
		return err
	}

	// Watches the managed types
	for _, obj := range blder.managedObjects {
		src := &source.Kind{Type: obj}
		hdler := &handler.EnqueueRequestForOwner{
			OwnerType:    blder.apiType,
			IsController: true,
		}
		if err := blder.ctrl.Watch(src, hdler, blder.predicates...); err != nil {
			return err
		}
	}

	// Do the watch requests
	for _, w := range blder.watchRequest {
		if err := blder.ctrl.Watch(w.src, w.eventhandler, blder.predicates...); err != nil {
			return err
		}

	}
	return nil
}

func (blder *Builder) doConfig() error {
	if blder.config != nil {
		return nil
	}
	if blder.mgr != nil {
		blder.config = blder.mgr.GetConfig()
		return nil
	}
	var err error
	blder.config, err = getConfig()
	return err
}

func (blder *Builder) doManager() error {
	if blder.mgr != nil {
		return nil
	}
	var err error
	blder.mgr, err = newManager(blder.config, manager.Options{})
	return err
}

func (blder *Builder) getControllerName() (string, error) {
	gvk, err := getGvk(blder.apiType, blder.mgr.GetScheme())
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-application", strings.ToLower(gvk.Kind))
	return name, nil
}

func (blder *Builder) doController(r reconcile.Reconciler) error {
	name, err := blder.getControllerName()
	if err != nil {
		return err
	}
	blder.ctrl, err = newController(name, blder.mgr, controller.Options{Reconciler: r})
	return err
}

func (blder *Builder) doWebhook() error {
	// Create a webhook for each type
	gvk, err := apiutil.GVKForObject(blder.apiType, blder.mgr.GetScheme())
	if err != nil {
		return err
	}

	partialPath := strings.Replace(gvk.Group, ".", "-", -1) + "-" +
		gvk.Version + "-" + strings.ToLower(gvk.Kind)

	// TODO: When the conversion webhook lands, we need to handle all registered versions of a given group-kind.
	// A potential workflow for defaulting webhook
	// 1) a bespoke (non-hub) version comes in
	// 2) convert it to the hub version
	// 3) do defaulting
	// 4) convert it back to the same bespoke version
	// 5) calculate the JSON patch
	//
	// A potential workflow for validating webhook
	// 1) a bespoke (non-hub) version comes in
	// 2) convert it to the hub version
	// 3) do validation
	if defaulter, isDefaulter := blder.apiType.(admission.Defaulter); isDefaulter {
		mwh := admission.DefaultingWebhookFor(defaulter)
		if mwh != nil {
			path := "/mutate-" + partialPath
			log.Info("Registering a mutating webhook",
				"GVK", gvk,
				"path", path)

			blder.mgr.GetWebhookServer().Register(path, mwh)
		}
	}

	if validator, isValidator := blder.apiType.(admission.Validator); isValidator {
		vwh := admission.ValidatingWebhookFor(validator)
		if vwh != nil {
			path := "/validate-" + partialPath
			log.Info("Registering a validating webhook",
				"GVK", gvk,
				"path", path)
			blder.mgr.GetWebhookServer().Register(path, vwh)
		}
	}

	return err
}
