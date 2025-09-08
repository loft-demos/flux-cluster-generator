// internal/controller/namespace_set.go
package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type threadSafeSet struct {
	mu RWMutex
	m  map[string]struct{}
}
type RWMutex interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}
// you likely already had a concrete sync.RWMutex variant; simplest:
type realRW sync.RWMutex
// but easiest is just reuse your previous implementation:
import "sync"

func newThreadSafeSet() *threadSafeSet { return &threadSafeSet{m: map[string]struct{}{}, mu: new(sync.RWMutex) } }
func (s *threadSafeSet) Has(k string) bool { s.mu.RLock(); defer s.mu.RUnlock(); _, ok := s.m[k]; return ok }
func (s *threadSafeSet) Add(k string)       { s.mu.Lock(); defer s.mu.Unlock(); s.m[k] = struct{}{} }
func (s *threadSafeSet) Delete(k string)    { s.mu.Lock(); defer s.mu.Unlock(); delete(s.m, k) }

type NamespaceSetReconciler struct {
	client.Client
	AllowedNS *threadSafeSet
	Selector  labels.Selector
}

func (r *NamespaceSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		r.AllowedNS.Delete(req.Name)
		return ctrl.Result{}, nil
	}
	if r.Selector.Matches(labels.Set(ns.Labels)) {
		r.AllowedNS.Add(ns.Name)
	} else {
		r.AllowedNS.Delete(ns.Name)
	}
	return ctrl.Result{}, nil
}
