package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"k8s.io/client-go/util/workqueue"
)

var rsipGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSetInputProvider",
}

type SecretMirrorReconciler struct {
	client.Client
	APIReader client.Reader

	Opts Options

	// in-memory allow-list for Namespaces
	allowedNS *threadSafeSet
}

func SetupRSIPController(mgr ctrl.Manager, opts Options) error {
	allowed := newThreadSafeSet()

	r := &SecretMirrorReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Opts:      opts,
		allowedNS: allowed,
	}

	// Seed allowlist at start
	var nsList corev1.NamespaceList
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.GetAPIReader().List(ctx, &nsList); err == nil {
		for i := range nsList.Items {
			if opts.NamespaceSelector.Matches(labels.Set(nsList.Items[i].Labels)) {
				allowed.Add(nsList.Items[i].Name)
			}
		}
	}

	// Namespace controller (keeps allowlist up to date)
	nsPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return opts.NamespaceSelector.Matches(labels.Set(e.Object.GetLabels())) },
		UpdateFunc: func(e event.UpdateEvent) bool { return opts.NamespaceSelector.Matches(labels.Set(e.ObjectNew.GetLabels())) },
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return opts.NamespaceSelector.Matches(labels.Set(e.Object.GetLabels())) },
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(nsPred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(&NamespaceSetReconciler{Client: mgr.GetClient(), AllowedNS: allowed, Selector: opts.NamespaceSelector}); err != nil {
		return err
	}

	// Optional NS filter list
	watchNS := sets.New[string](opts.WatchNamespaces...)

	// Secret controller
	secPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return (watchNS.Len() == 0 || watchNS.Has(e.Object.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.Object.GetLabels())) &&
				allowed.Has(e.Object.GetNamespace())
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return (watchNS.Len() == 0 || watchNS.Has(e.ObjectNew.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.ObjectNew.GetLabels())) &&
				allowed.Has(e.ObjectNew.GetNamespace())
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool {
			return (watchNS.Len() == 0 || watchNS.Has(e.Object.GetNamespace())) &&
				opts.LabelSelector.Matches(labels.Set(e.Object.GetLabels())) &&
				allowed.Has(e.Object.GetNamespace())
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(secPred)).
		WithOptions(controller.Options{
			CacheSyncTimeout:        opts.CacheSyncTimeout,
			RecoverPanic:            boolPtr(true),
			RateLimiter:             workqueue.DefaultControllerRateLimiter(),
			MaxConcurrentReconciles: opts.MaxConcurrent,
		}).
		Complete(r)
}

func (r *SecretMirrorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (reconcile.Result, error) {
	log := ctrl.Log.WithName("rsip").WithValues("secret", req.NamespacedName.String())

	var sec corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &sec); err != nil {
		return reconcile.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}

	// filters
	if !r.allowedNS.Has(sec.Namespace) || !r.Opts.LabelSelector.Matches(labels.Set(sec.Labels)) {
		return reconcile.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}
	if _, ok := sec.Data[r.Opts.SecretKey]; !ok {
		log.V(1).Info("missing kubeconfig key; skipping", "key", r.Opts.SecretKey)
		return reconcile.Result{}, nil
	}

	// names/ids
	clusterName := sec.Labels[r.Opts.ClusterNameKey]
	if clusterName == "" {
		clusterName = sec.Name
	}
	if errs := validation.IsDNS1123Label(clusterName); len(errs) > 0 {
		clusterName = sanitizeDNS1123(clusterName)
	}
	project := strings.TrimSpace(sec.Labels[r.Opts.ProjectLabelKey])
	if project == "" {
		project = projectFromNamespace(sec.Namespace)
	}
	project = sanitizeDNS1123(project)

	rsipName := r.Opts.RSIPNamePrefix
	if project != "" {
		rsipName += project + "-"
	}
	rsipName += clusterName

	// desired RSIP
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(rsipGVK)
	desired.SetNamespace(r.Opts.RSIPNamespace)
	desired.SetName(rsipName)

	lbls := map[string]string{
		"mirror.fluxcd.io/managed":     "true",
		"mirror.fluxcd.io/secretNS":    sec.Namespace,
		"mirror.fluxcd.io/secretName":  sec.Name,
		"mirror.fluxcd.io/secretKey":   r.Opts.SecretKey,
		"mirror.fluxcd.io/clusterName": clusterName,
		"mirror.fluxcd.io/project":     project,
	}
	// copy selected labels to RSIP labels
	for _, k := range r.Opts.CopyLabelKeys {
		if v, ok := sec.Labels[k]; ok {
			lbls[k] = v
		}
	}
	for k, v := range sec.Labels {
		if hasAnyPrefix(r.Opts.CopyLabelPrefixes, k) {
			lbls[k] = v
		}
	}
	desired.SetLabels(lbls)

	// defaultValues incl. camelCased copies of selected labels
	dv := map[string]any{
		"name":           clusterName,
		"project":        project,
		"kubeSecretName": sec.Name,
		"kubeSecretKey":  r.Opts.SecretKey,
		"kubeSecretNS":   sec.Namespace,
	}
	reserved := sets.New[string]("name", "project", "kubeSecretName", "kubeSecretKey", "kubeSecretNS")

	for _, k := range r.Opts.CopyLabelKeys {
		if v, ok := sec.Labels[k]; ok {
			ck := toCamel(k)
			if !reserved.Has(ck) {
				dv[ck] = v
			}
		}
	}
	for k, v := range sec.Labels {
		if hasAnyPrefix(r.Opts.CopyLabelPrefixes, k) {
			ck := toCamel(k)
			if !reserved.Has(ck) {
				dv[ck] = v
			}
		}
	}

	_ = unstructured.SetNestedField(desired.Object, map[string]any{
		"type":          "Static",
		"defaultValues": dv,
	}, "spec")

	// create/update
	var existing unstructured.Unstructured
	existing.SetGroupVersionKind(rsipGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: rsipName, Namespace: r.Opts.RSIPNamespace}, &existing); err != nil {
		if err := r.Create(ctx, desired); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("created RSIP", "name", rsipName)
		return reconcile.Result{}, nil
	}

	changed := false
	if !maps.Equal(existing.GetLabels(), desired.GetLabels()) {
		existing.SetLabels(desired.GetLabels())
		changed = true
	}
	curSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	if !mapsEqual(curSpec, desSpec) {
		_ = unstructured.SetNestedMap(existing.Object, desSpec, "spec")
		changed = true
	}
	if changed {
		if err := r.Update(ctx, &existing); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("updated RSIP", "name", rsipName)
	}
	return reconcile.Result{}, nil
}

// ----- shared helpers (kept local; move to util/ if you reuse) -----

type threadSafeSet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}
func newThreadSafeSet() *threadSafeSet { return &threadSafeSet{m: map[string]struct{}{}} }
func (s *threadSafeSet) Has(k string) bool { s.mu.RLock(); defer s.mu.RUnlock(); _, ok := s.m[k]; return ok }
func (s *threadSafeSet) Add(k string)       { s.mu.Lock(); defer s.mu.Unlock(); s.m[k] = struct{}{} }
func (s *threadSafeSet) Delete(k string)    { s.mu.Lock(); defer s.mu.Unlock(); delete(s.m, k) }

type NamespaceSetReconciler struct {
	client.Client
	AllowedNS *threadSafeSet
	Selector  labels.Selector
}

func (r *NamespaceSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (reconcile.Result, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		r.AllowedNS.Delete(req.Name)
		return reconcile.Result{}, nil
	}
	if r.Selector.Matches(labels.Set(ns.Labels)) {
		r.AllowedNS.Add(ns.Name)
	} else {
		r.AllowedNS.Delete(ns.Name)
	}
	return reconcile.Result{}, nil
}

// utils
func boolPtr(b bool) *bool { return &b }

func projectFromNamespace(ns string) string {
	if strings.HasPrefix(ns, "p-") && len(ns) > 2 { return ns[2:] }
	return ""
}
func sanitizeDNS1123(in string) string {
	s := strings.ToLower(in)
	mapper := func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' { return r }
		return '-'
	}
	s = strings.Map(mapper, s)
	s = strings.Trim(s, "-")
	if s == "" { return "id" }
	if len(s) > 63 { return s[:63] }
	return s
}
func hasAnyPrefix(prefixes []string, key string) bool {
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" && strings.HasPrefix(key, p) { return true }
	}
	return false
}
func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) { return false }
	for k, va := range a {
		vb, ok := b[k]; if !ok { return false }
		switch at := va.(type) {
		case map[string]any:
			bt, ok := vb.(map[string]any); if !ok { return false }
			if !mapsEqual(at, bt) { return false }
		default:
			if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) { return false }
		}
	}
	return true
}
func toCamel(s string) string {
	if s == "" { return s }
	sep := func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '/' || r == ':' }
	parts := strings.FieldsFunc(s, sep)
	if len(parts) == 0 { return s }
	for i := range parts { parts[i] = strings.TrimSpace(parts[i]) }
	out := strings.ToLower(parts[0])
	for _, p := range parts[1:] {
		if p == "" { continue }
		out += strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	// strip non-alnum
	b := strings.Builder{}
	for _, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') { b.WriteRune(r) }
	}
	return b.String()
}
