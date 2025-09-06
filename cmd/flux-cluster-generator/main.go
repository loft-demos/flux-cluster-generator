// main.go
// A lightweight controller that watches labeled Secrets and mirrors them into
// ResourceSetInputProviders (RSIPs) for Flux Operator ResourceSets.
// Supports:
//   - Selecting Secrets by label selector
//   - Restricting to Namespaces that match a namespace-label selector
//   - Copying specific label KEYS and any label KEYS that start with configured PREFIXES (e.g. flux-app/)
//   - Creating/Updating an RSIP per matching Secret; deleting RSIP when Secret is deleted or no longer matches

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/workqueue"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RSIP GVK
var rsipGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSetInputProvider",
}

// CLI/config flags
var (
	watchNamespacesCSV        string
	rsipNamespace             string
	labelSelectorStr          string
	secretKey                 string
	rsipNamePrefix            string
	clusterNameKey            string
	copyLabelKeysCSV          string
	copyLabelPrefixesCSV      string
	namespaceLabelSelectorStr string
)

func main() {
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)

	flag.StringVar(&watchNamespacesCSV, "watch-namespaces", "", "Comma-separated namespaces to watch for Secrets. Empty = all namespaces")
	flag.StringVar(&rsipNamespace, "rsip-namespace", "apps", "Namespace to create RSIPs in")
	flag.StringVar(&labelSelectorStr, "label-selector", "", "Label selector for source Secrets, e.g. env=dev,team=payments,fluxcd/secret-type=cluster")
	flag.StringVar(&secretKey, "secret-key", "config", "Key in the Secret data that contains the kubeconfig")
	flag.StringVar(&rsipNamePrefix, "rsip-name-prefix", "inputs-", "Prefix for generated RSIP names")
	flag.StringVar(&clusterNameKey, "cluster-name-label-key", "vcluster.loft.sh/cluster-name", "Label key on the Secret to derive cluster name; fallback to Secret name")
	flag.StringVar(&copyLabelKeysCSV, "copy-label-keys", "env,team,region", "Comma-separated label KEYS to copy from Secret to RSIP")
	flag.StringVar(&copyLabelPrefixesCSV, "copy-label-prefixes", "", "Comma-separated label KEY PREFIXES to copy (e.g. flux-app/)")
	flag.StringVar(&namespaceLabelSelectorStr, "namespace-label-selector", "", "Label selector for Namespaces to include (e.g. flux-cluster-generator-enabled=true)")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})

	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	c := mgr.GetClient()

	// uncached reader (safe before mgr.Start)
    apiReader := mgr.GetAPIReader()

	// Parse Secret label selector
	var secSel labels.Selector
	if labelSelectorStr != "" {
		parsed, err := labels.Parse(labelSelectorStr)
		if err != nil {
			logger.Error(err, "invalid --label-selector")
			os.Exit(1)
		}
		secSel = parsed
	} else {
		secSel = labels.Everything()
	}

	copyLabelKeys := sets.New[string]()
	for _, k := range splitNonEmpty(copyLabelKeysCSV) {
		copyLabelKeys.Insert(strings.TrimSpace(k))
	}
	copyLabelPrefixes := splitNonEmpty(copyLabelPrefixesCSV)

	// create the shared set before using it
	allowedNS := newThreadSafeSet()

	reconciler := &SecretMirrorReconciler{
		Client:            c,
		RSIPNamespace:     rsipNamespace,
		Selector:          secSel,
		SecretKey:         secretKey,
		RSIPNamePrefix:    rsipNamePrefix,
		ClusterNameKey:    clusterNameKey,
		CopyLabelKeys:     copyLabelKeys,
		CopyLabelPrefixes: copyLabelPrefixes,
		AllowedNS:         allowedNS,
	}

	// Namespace watcher maintains allowed namespace set
	var nsSel labels.Selector = labels.Everything()
	if namespaceLabelSelectorStr != "" {
		if s, err := labels.Parse(namespaceLabelSelectorStr); err == nil {
			nsSel = s
		} else {
			logger.Error(err, "invalid --namespace-label-selector")
			os.Exit(1)
		}
	}

	// Seed AllowedNS with existing namespaces that match nsSel (before mgr.Start)
	{
	    var nsList corev1.NamespaceList
	    // use the uncached API reader here
	    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	    defer cancel()
	    if err := apiReader.List(ctx, &nsList); err != nil {
	        logger.Error(err, "failed to list namespaces at startup")
	        os.Exit(1)
	    }
	    for i := range nsList.Items {
	        if nsSel.Matches(labels.Set(nsList.Items[i].Labels)) {
	            allowedNS.Add(nsList.Items[i].Name)
	        }
	    }
	    logger.Info("seeded allowed namespaces", "count", len(nsList.Items))
	}
	
	nsPred := predicate.NewPredicateFuncs(func(o client.Object) bool { return nsSel.Matches(labels.Set(o.GetLabels())) })
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(nsPred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(&NamespaceSetReconciler{Client: c, AllowedNS: allowedNS, Selector: nsSel}); err != nil {
		logger.Error(err, "unable to create namespace controller")
		os.Exit(1)
	}
    // Convert CSV list into a set for quick membership checks
    watchNS := sets.New[string]()
    for _, ns := range splitNonEmpty(watchNamespacesCSV) {
        watchNS.Insert(strings.TrimSpace(ns))
    }

	// Secret watcher — allow deletes to pass so cleanup runs
	secretPred := predicate.Funcs{
	    CreateFunc: func(e event.CreateEvent) bool {
	        return (watchNS.Len() == 0 || watchNS.Has(e.Object.GetNamespace())) &&
	            secSel.Matches(labels.Set(e.Object.GetLabels())) &&
	            allowedNS.Has(e.Object.GetNamespace())
	    },
	    UpdateFunc: func(e event.UpdateEvent) bool {
	        return (watchNS.Len() == 0 || watchNS.Has(e.ObjectNew.GetNamespace())) &&
	            secSel.Matches(labels.Set(e.ObjectNew.GetLabels())) &&
	            allowedNS.Has(e.ObjectNew.GetNamespace())
	    },
	    GenericFunc: func(e event.GenericEvent) bool {
	        return (watchNS.Len() == 0 || watchNS.Has(e.Object.GetNamespace())) &&
	            secSel.Matches(labels.Set(e.Object.GetLabels())) &&
	            allowedNS.Has(e.Object.GetNamespace())
	    },
	    // Key change: ALWAYS reconcile on delete so ensureRSIPAbsence runs
	    DeleteFunc: func(e event.DeleteEvent) bool {
	        return true
	    },
	}
	
	b := ctrl.NewControllerManagedBy(mgr).
	    For(&corev1.Secret{}, builder.WithPredicates(secretPred)).
	    WithOptions(controller.Options{
	        CacheSyncTimeout:        2 * time.Minute,
	        RecoverPanic:            boolPtr(true),
	        RateLimiter:             workqueue.DefaultControllerRateLimiter(),
	        MaxConcurrentReconciles: 2,
	    })

	if err := b.Complete(reconciler); err != nil {
		logger.Error(err, "unable to create secret controller")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// SecretMirrorReconciler mirrors Secrets -> RSIPs

type SecretMirrorReconciler struct {
	client.Client
	RSIPNamespace     string
	Selector          labels.Selector
	SecretKey         string
	RSIPNamePrefix    string
	ClusterNameKey    string
	CopyLabelKeys     sets.Set[string]
	CopyLabelPrefixes []string
	AllowedNS         *threadSafeSet
}

func (r *SecretMirrorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithName("reconcile").WithValues("secret", types.NamespacedName{Name: req.Name, Namespace: req.Namespace})

	// Fetch Secret
	var sec corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &sec); err != nil {
		// If gone, try to delete corresponding RSIP
		return ctrl.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}

	// Check namespace eligibility and label selector
	if !r.AllowedNS.Has(sec.Namespace) || !r.Selector.Matches(labels.Set(sec.Labels)) {
		// Not eligible -> delete RSIP if exists
		return ctrl.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}

	// Ensure the kubeconfig key exists
	if _, ok := sec.Data[r.SecretKey]; !ok {
		log.Info("secret missing kubeconfig key; skipping RSIP create/update", "key", r.SecretKey)
		return ctrl.Result{}, nil
	}

	// Compute names
	rsipName := r.RSIPNamePrefix + sec.Name
	clusterName := sec.Labels[r.ClusterNameKey]
	if clusterName == "" {
		clusterName = sec.Name
	}
	if errList := validation.IsDNS1123Label(clusterName); len(errList) > 0 {
		log.Info("sanitizing cluster name to DNS-1123", "errors", errList)
		clusterName = sanitizeDNS1123(clusterName)
	}

	// Desired RSIP object
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(rsipGVK)
	desired.SetName(rsipName)
	desired.SetNamespace(r.RSIPNamespace)

	// Labels to apply onto RSIP (baseline)
	labelsToApply := map[string]string{
		"mirror.fluxcd.io/managed":     "true",
		"mirror.fluxcd.io/secretNS":      sec.Namespace, 
    	"mirror.fluxcd.io/secretName":    sec.Name,      
		"mirror.fluxcd.io/secretKey":   r.SecretKey,
		"mirror.fluxcd.io/clusterName": clusterName,
	}
	// Copy exact label keys
	for k := range r.CopyLabelKeys {
		if v, ok := sec.Labels[k]; ok {
			labelsToApply[k] = v
		}
	}
	// Copy labels that match any configured prefix (e.g., flux-app/)
	for k, v := range sec.Labels {
		if hasAnyPrefix(r.CopyLabelPrefixes, k) {
			labelsToApply[k] = v
		}
	}
	desired.SetLabels(labelsToApply)

	// Spec: point to the Secret (we don't copy kubeconfig bytes, just reference)
	spec := map[string]any{
		"type": "Static",
		"defaultValues": map[string]any{
			"name":           clusterName,
			"kubeSecretName": sec.Name,
			"kubeSecretKey":  r.SecretKey,
			"kubeSecretNS":   sec.Namespace,
		},
	}
	_ = unstructured.SetNestedField(desired.Object, spec, "spec")

	// Create or Update
	var existing unstructured.Unstructured
	existing.SetGroupVersionKind(rsipGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: rsipName, Namespace: r.RSIPNamespace}, &existing); err != nil {
		// Create
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("created RSIP", "name", rsipName, "ns", r.RSIPNamespace)
		return ctrl.Result{}, nil
	}

	// Update if drifted
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
			return ctrl.Result{}, err
		}
		log.Info("updated RSIP", "name", rsipName)
	}

	return ctrl.Result{}, nil
}

func (r *SecretMirrorReconciler) ensureRSIPAbsence(ctx context.Context, secretNN types.NamespacedName) error {
    // A) Best-effort delete by deterministic name
    byName := &unstructured.Unstructured{}
    byName.SetGroupVersionKind(rsipGVK)
    byName.SetNamespace(r.RSIPNamespace)
    byName.SetName(r.RSIPNamePrefix + secretNN.Name)
    _ = r.Delete(ctx, byName) // ignore NotFound

    // B) Delete any RSIPs labeled with the *new* split labels
    var list unstructured.UnstructuredList
    list.SetGroupVersionKind(schema.GroupVersionKind{
        Group: rsipGVK.Group, Version: rsipGVK.Version, Kind: rsipGVK.Kind + "List",
    })
    if err := r.List(ctx, &list,
        client.InNamespace(r.RSIPNamespace),
        client.MatchingLabels{
            "mirror.fluxcd.io/secretNS":   secretNN.Namespace,
            "mirror.fluxcd.io/secretName": secretNN.Name,
        },
    ); err != nil {
        return err
    }
    for i := range list.Items {
        _ = r.Delete(ctx, &list.Items[i])
    }
    return nil
}

// --- helpers ---

type threadSafeSet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func boolPtr(b bool) *bool { return &b }

func newThreadSafeSet() *threadSafeSet { return &threadSafeSet{m: map[string]struct{}{}} }

func (s *threadSafeSet) Has(k string) bool {
	s.mu.RLock(); defer s.mu.RUnlock()
	_, ok := s.m[k]
	return ok
}
func (s *threadSafeSet) Add(k string) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.m[k] = struct{}{}
}
func (s *threadSafeSet) Delete(k string) {
	s.mu.Lock(); defer s.mu.Unlock()
	delete(s.m, k)
}

// Reconciler to keep AllowedNS in sync with Namespace label selector

type NamespaceSetReconciler struct {
	client.Client
	AllowedNS *threadSafeSet
	Selector  labels.Selector
}

func (r *NamespaceSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		// remove on delete/not found
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

func splitNonEmpty(csv string) []string {
	var out []string
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func sanitizeDNS1123(in string) string {
	s := strings.ToLower(in)
	repl := func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}
	s = strings.Map(repl, s)
	s = strings.Trim(s, "-")
	if len(s) == 0 {
		return "cluster"
	}
	if len(s) > 63 {
		return s[:63]
	}
	return s
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch vaTyped := va.(type) {
		case map[string]any:
			vbTyped, ok := vb.(map[string]any)
			if !ok {
				return false
			}
			if !mapsEqual(vaTyped, vbTyped) {
				return false
			}
		default:
			if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) {
				return false
			}
		}
	}
	return true
}

func hasAnyPrefix(prefixes []string, key string) bool {
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
