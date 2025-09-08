package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NewRSIPReconciler maps Options into the reconciler.
func NewRSIPReconciler(c client.Client, r client.Reader, opts Options) *SecretMirrorReconciler {
	// convert []string to sets if your reconciler expects sets.Set[string]
	copyKeysSet := toStringSet(opts.CopyLabelKeys)
	return &SecretMirrorReconciler{
		Client:            c,
		APIReader:         r,
		RSIPNamespace:     opts.RSIPNamespace,
		Selector:          opts.LabelSelector,
		SecretKey:         opts.SecretKey,
		RSIPNamePrefix:    opts.RSIPNamePrefix,
		ClusterNameKey:    opts.ClusterNameKey,
		ProjectLabelKey:   opts.ProjectLabelKey,
		CopyLabelKeys:     copyKeysSet,
		CopyLabelPrefixes: opts.CopyLabelPrefixes,
		AllowedNS:         newThreadSafeSet(),
	}
}

// SetupRSIPController wires watches, seeds namespace allowlist, and adds the GC runnable.
func SetupRSIPController(mgr manager.Manager, opts Options) error {
	log := controller_runtime.Log.WithName("setup.rsip")

	rec := NewRSIPReconciler(mgr.GetClient(), mgr.GetAPIReader(), opts)

	// Seed AllowedNS (before mgr.Start)
	{
		var nsList corev1.NamespaceList
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := mgr.GetAPIReader().List(ctx, &nsList); err != nil {
			return fmt.Errorf("list namespaces: %w", err)
		}
		for i := range nsList.Items {
			if opts.NamespaceSelector.Matches(labels.Set(nsList.Items[i].Labels)) {
				rec.AllowedNS.Add(nsList.Items[i].Name)
			}
		}
		log.Info("seeded allowed namespaces", "count", len(rec.AllowedNS.m))
	}

	// Namespace watch keeps AllowedNS up to date
	nsPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return opts.NamespaceSelector.Matches(labels.Set(e.Object.GetLabels()))
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return opts.NamespaceSelector.Matches(labels.Set(e.ObjectNew.GetLabels()))
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return opts.NamespaceSelector.Matches(labels.Set(e.Object.GetLabels())) },
	}
	if err := controller_runtime.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(nsPred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(&NamespaceSetReconciler{
			Client:    mgr.GetClient(),
			AllowedNS: rec.AllowedNS,
			Selector:  opts.NamespaceSelector,
		}); err != nil {
		return err
	}

	// Optional explicit namespace allowlist (watch-namespaces)
	watchSet := toStringSet(opts.WatchNamespaces)

	// Secret watch (allow deletes for cleanup)
	secPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return (watchSet.Len() == 0 || watchSet.Has(e.Object.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.Object.GetLabels())) &&
				rec.AllowedNS.Has(e.Object.GetNamespace())
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return (watchSet.Len() == 0 || watchSet.Has(e.ObjectNew.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.ObjectNew.GetLabels())) &&
				rec.AllowedNS.Has(e.ObjectNew.GetNamespace())
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool {
			return (watchSet.Len() == 0 || watchSet.Has(e.Object.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.Object.GetLabels())) &&
				rec.AllowedNS.Has(e.Object.GetNamespace())
		},
	}

	if err := controller_runtime.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(secPred)).
		WithOptions(controller.Options{
			CacheSyncTimeout:        opts.CacheSyncTimeout,
			RecoverPanic:            boolPtr(true),
			RateLimiter:             workqueue.DefaultControllerRateLimiter(),
			MaxConcurrentReconciles: opts.MaxConcurrent,
		}).
		Complete(rec); err != nil {
		return err
	}

	// Periodic GC runnable
	gcLog := controller_runtime.Log.WithName("gc")
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		if err := sweepOrphanRSIPs(ctx, gcLog, mgr.GetAPIReader(), mgr.GetClient(), opts.RSIPNamespace); err != nil {
			gcLog.Error(err, "initial GC sweep failed")
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := sweepOrphanRSIPs(ctx, gcLog, mgr.GetAPIReader(), mgr.GetClient(), opts.RSIPNamespace); err != nil {
					gcLog.Error(err, "periodic GC sweep failed")
				}
			}
		}
	}))
}

// tiny helper converting []string to a set (compatible with your existing type)
type stringSet interface {
	Has(string) bool
	Len() int
	Insert(string)
}

func toStringSet(items []string) setsString { // local minimal set type
	s := setsString{m: map[string]struct{}{}}
	for _, it := range items {
		if it != "" {
			s.m[it] = struct{}{}
		}
	}
	return s
}

type setsString struct{ m map[string]struct{} }

func (s setsString) Has(k string) bool { _, ok := s.m[k]; ok = ok; return ok }
func (s setsString) Len() int          { return len(s.m) }
func (s setsString) Insert(k string)   { s.m[k] = struct{}{} }
