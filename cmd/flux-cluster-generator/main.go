// cmd/flux-cluster-generator/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/go-logr/logr"
)

var rsipGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSetInputProvider",
}

// CLI flags
var (
	watchNamespacesCSV        string
	rsipNamespace             string
	labelSelectorStr          string
	secretKey                 string
	rsipNamePrefix            string
	clusterNameKey            string
	projectLabelKey           string
	copyLabelKeysCSV          string
	copyLabelPrefixesCSV      string
	namespaceLabelSelectorStr string
)

func main() {
	z := zap.Options{Development: false}
	z.BindFlags(flag.CommandLine)

	flag.StringVar(&watchNamespacesCSV, "watch-namespaces", "", "Comma-separated namespaces to watch for Secrets. Empty = all namespaces")
	flag.StringVar(&rsipNamespace, "rsip-namespace", "flux-apps", "Namespace to create RSIPs in")
	flag.StringVar(&labelSelectorStr, "label-selector", "", "Label selector for source Secrets, e.g. env=dev,team=payments,fluxcd.io/secret-type=cluster")
	flag.StringVar(&secretKey, "secret-key", "config", "Key in Secret.data that contains the kubeconfig")
	flag.StringVar(&rsipNamePrefix, "rsip-name-prefix", "inputs-", "Prefix for generated RSIP names")
	flag.StringVar(&clusterNameKey, "cluster-name-label-key", "vci.flux.loft.sh/name", "Label key on the Secret to derive cluster name; fallback to Secret name")
	flag.StringVar(&projectLabelKey, "project-label-key", "vci.flux.loft.sh/project", "Label key on the Secret containing the VCI project")
	flag.StringVar(&copyLabelKeysCSV, "copy-label-keys", "env,team,region", "Comma-separated label KEYS to copy from Secret to RSIP")
	flag.StringVar(&copyLabelPrefixesCSV, "copy-label-prefixes", "", "Comma-separated label KEY PREFIXES to copy (e.g. flux-app/)")
	flag.StringVar(&namespaceLabelSelectorStr, "namespace-label-selector", "", "Label selector for Namespaces to include (e.g. flux-cluster-generator-enabled=true)")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&z)))
	logger := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	c := mgr.GetClient()
	apiReader := mgr.GetAPIReader() // uncached

	// Secret selector
	var secSel labels.Selector
	if labelSelectorStr != "" {
		if parsed, err := labels.Parse(labelSelectorStr); err == nil {
			secSel = parsed
		} else {
			logger.Error(err, "invalid --label-selector")
			os.Exit(1)
		}
	} else {
		secSel = labels.Everything()
	}

	copyLabelKeys := sets.New[string]()
	for _, k := range splitNonEmpty(copyLabelKeysCSV) {
		copyLabelKeys.Insert(strings.TrimSpace(k))
	}
	copyLabelPrefixes := splitNonEmpty(copyLabelPrefixesCSV)

	allowedNS := newThreadSafeSet()

	reconciler := &SecretMirrorReconciler{
		Client:            c,
		APIReader:         apiReader,
		RSIPNamespace:     rsipNamespace,
		Selector:          secSel,
		SecretKey:         secretKey,
		RSIPNamePrefix:    rsipNamePrefix,
		ClusterNameKey:    clusterNameKey,
		ProjectLabelKey:   projectLabelKey,
		CopyLabelKeys:     copyLabelKeys,
		CopyLabelPrefixes: copyLabelPrefixes,
		AllowedNS:         allowedNS,
	}

	// Namespace allowlist maintenance
	nsSel := labels.Everything()
	if namespaceLabelSelectorStr != "" {
		if s, err := labels.Parse(namespaceLabelSelectorStr); err == nil {
			nsSel = s
		} else {
			logger.Error(err, "invalid --namespace-label-selector")
			os.Exit(1)
		}
	}

	// Seed allowlist
	{
		var nsList corev1.NamespaceList
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
		logger.Info("seeded allowed namespaces", "count", len(allowedNS.m))
	}

	// Namespace controller
	nsPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return nsSel.Matches(labels.Set(e.Object.GetLabels())) },
		UpdateFunc: func(e event.UpdateEvent) bool { return nsSel.Matches(labels.Set(e.ObjectNew.GetLabels())) },
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return nsSel.Matches(labels.Set(e.Object.GetLabels())) },
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(nsPred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(&NamespaceSetReconciler{Client: c, AllowedNS: allowedNS, Selector: nsSel}); err != nil {
		logger.Error(err, "unable to create namespace controller")
		os.Exit(1)
	}

	// Optional namespace watch list
	watchNS := sets.New[string]()
	for _, ns := range splitNonEmpty(watchNamespacesCSV) {
		watchNS.Insert(strings.TrimSpace(ns))
	}

	// Secret controller (allow deletes for cleanup)
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
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool {
			return (watchNS.Len() == 0 || watchNS.Has(e.Object.GetNamespace())) &&
				secSel.Matches(labels.Set(e.Object.GetLabels())) &&
				allowedNS.Has(e.Object.GetNamespace())
		},
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(secretPred)).
		WithOptions(controller.Options{
			CacheSyncTimeout:        2 * time.Minute,
			RecoverPanic:            boolPtr(true),
			RateLimiter:             workqueue.DefaultControllerRateLimiter(),
			MaxConcurrentReconciles: 2,
		}).
		Complete(reconciler); err != nil {
		logger.Error(err, "unable to create secret controller")
		os.Exit(1)
	}

	// Periodic GC of orphan RSIPs
	gcLog := ctrl.Log.WithName("gc")
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		if err := sweepOrphanRSIPs(ctx, gcLog, apiReader, c, rsipNamespace); err != nil {
			gcLog.Error(err, "initial GC sweep failed")
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := sweepOrphanRSIPs(ctx, gcLog, apiReader, c, rsipNamespace); err != nil {
					gcLog.Error(err, "periodic GC sweep failed")
				}
			}
		}
	})); err != nil {
		logger.Error(err, "unable to add GC runnable")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// ---- Reconcilers ----

type SecretMirrorReconciler struct {
	client.Client
	APIReader         client.Reader
	RSIPNamespace     string
	Selector          labels.Selector
	SecretKey         string
	RSIPNamePrefix    string
	ClusterNameKey    string
	ProjectLabelKey   string
	CopyLabelKeys     sets.Set[string]
	CopyLabelPrefixes []string
	AllowedNS         *threadSafeSet
}

func (r *SecretMirrorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithName("reconcile").WithValues("secret", types.NamespacedName{Name: req.Name, Namespace: req.Namespace})

	// Load Secret; if gone, delete RSIP(s) by labels
	var sec corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &sec); err != nil {
		return ctrl.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}

	// Filter by namespace allowlist + label selector
	if !r.AllowedNS.Has(sec.Namespace) || !r.Selector.Matches(labels.Set(sec.Labels)) {
		return ctrl.Result{}, r.ensureRSIPAbsence(ctx, req.NamespacedName)
	}

	// Require kubeconfig key
	if _, ok := sec.Data[r.SecretKey]; !ok {
		log.Info("secret missing kubeconfig key; skipping RSIP create/update", "key", r.SecretKey)
		return ctrl.Result{}, nil
	}

	// Names/ids
	clusterName := sec.Labels[r.ClusterNameKey]
	if clusterName == "" {
		clusterName = sec.Name
	}
	if errs := validation.IsDNS1123Label(clusterName); len(errs) > 0 {
		log.Info("sanitizing cluster name to DNS-1123", "errors", errs)
		clusterName = sanitizeDNS1123(clusterName)
	}

	project := strings.TrimSpace(sec.Labels[r.ProjectLabelKey])
	if project == "" {
		project = projectFromNamespace(sec.Namespace)
	}
	project = sanitizeDNS1123(project)

	// RSIP name: <prefix><project>-<cluster>
	rsipName := r.RSIPNamePrefix
	if project != "" {
		rsipName += project + "-"
	}
	rsipName += clusterName

	// Desired RSIP
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(rsipGVK)
	desired.SetNamespace(r.RSIPNamespace)
	desired.SetName(rsipName)

	labelsToApply := map[string]string{
		"mirror.fluxcd.io/managed":     "true",
		"mirror.fluxcd.io/secretNS":    sec.Namespace,
		"mirror.fluxcd.io/secretName":  sec.Name,
		"mirror.fluxcd.io/secretKey":   r.SecretKey,
		"mirror.fluxcd.io/clusterName": clusterName,
		"mirror.fluxcd.io/project":     project,
	}
	// Exact label keys
	for k := range r.CopyLabelKeys {
		if v, ok := sec.Labels[k]; ok {
			labelsToApply[k] = v
		}
	}
	// Prefixed labels
	for k, v := range sec.Labels {
		if hasAnyPrefix(r.CopyLabelPrefixes, k) {
			labelsToApply[k] = v
		}
	}
	desired.SetLabels(labelsToApply)

	spec := map[string]any{
		"type": "Static",
		"defaultValues": map[string]any{
			"name":           clusterName,
			"project":        project, // NEW
			"kubeSecretName": sec.Name,
			"kubeSecretKey":  r.SecretKey,
			"kubeSecretNS":   sec.Namespace,
		},
	}
	_ = unstructured.SetNestedField(desired.Object, spec, "spec")

	// Create/Update
	var existing unstructured.Unstructured
	existing.SetGroupVersionKind(rsipGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: rsipName, Namespace: r.RSIPNamespace}, &existing); err != nil {
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("created RSIP", "name", rsipName, "ns", r.RSIPNamespace)
		return ctrl.Result{}, nil
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
			return ctrl.Result{}, err
		}
		log.Info("updated RSIP", "name", rsipName)
	}
	return ctrl.Result{}, nil
}

func (r *SecretMirrorReconciler) ensureRSIPAbsence(ctx context.Context, secretNN types.NamespacedName) error {
	log := ctrl.Log.WithName("gc")

	// List by labels (works even when the Secret is already gone)
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: rsipGVK.Group, Version: rsipGVK.Version, Kind: rsipGVK.Kind + "List",
	})
	if err := r.APIReader.List(ctx, &list,
		client.InNamespace(r.RSIPNamespace),
		client.MatchingLabels{
			"mirror.fluxcd.io/secretNS":   secretNN.Namespace,
			"mirror.fluxcd.io/secretName": secretNN.Name,
		},
	); err != nil {
		return err
	}

	if len(list.Items) == 0 {
		log.Info("no RSIPs found for deleted/ignored secret", "secret", secretNN.String(), "rsipNS", r.RSIPNamespace)
		return nil
	}
	for i := range list.Items {
		if err := r.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			log.Error(err, "delete RSIP failed", "name", list.Items[i].GetName())
		} else {
			log.Info("deleted RSIP", "name", list.Items[i].GetName())
		}
	}
	return nil
}

// ---- helpers ----

type threadSafeSet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func boolPtr(b bool) *bool { return &b }

func newThreadSafeSet() *threadSafeSet { return &threadSafeSet{m: map[string]struct{}{}} }

func (s *threadSafeSet) Has(k string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[k]
	return ok
}
func (s *threadSafeSet) Add(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = struct{}{}
}
func (s *threadSafeSet) Delete(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
}

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

func sweepOrphanRSIPs(
	ctx context.Context,
	log logr.Logger,
	reader client.Reader, // uncached
	writer client.Client, // deletes
	rsipNS string,
) error {
	var rsips unstructured.UnstructuredList
	rsips.SetGroupVersionKind(schema.GroupVersionKind{
		Group: rsipGVK.Group, Version: rsipGVK.Version, Kind: rsipGVK.Kind + "List",
	})
	if err := reader.List(ctx, &rsips, client.InNamespace(rsipNS)); err != nil {
		return err
	}
	for i := range rsips.Items {
		rsip := &rsips.Items[i]
		lbl := rsip.GetLabels()
		secNS, okNS := lbl["mirror.fluxcd.io/secretNS"]
		secName, okName := lbl["mirror.fluxcd.io/secretName"]
		if !okNS || !okName || secNS == "" || secName == "" {
			continue
		}

		var sec corev1.Secret
		err := reader.Get(ctx, types.NamespacedName{Namespace: secNS, Name: secName}, &sec)
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "secret existence check failed",
				"rsip", rsip.GetName(), "secret", fmt.Sprintf("%s/%s", secNS, secName))
			continue
		}
		if err == nil {
			continue // secret still exists
		}
		if err := writer.Delete(ctx, rsip); client.IgnoreNotFound(err) != nil {
			log.Error(err, "failed deleting orphan RSIP", "name", rsip.GetName())
		} else {
			log.Info("deleted orphan RSIP", "name", rsip.GetName(),
				"secret", fmt.Sprintf("%s/%s", secNS, secName))
		}
	}
	return nil
}

// ---- utils ----

func splitNonEmpty(csv string) []string {
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
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
		return "id"
	}
	if len(s) > 63 {
		return s[:63]
	}
	return s
}

func hasAnyPrefix(prefixes []string, key string) bool {
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" && strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Best-effort derive project from namespace (supports "p-<project>")
func projectFromNamespace(ns string) string {
	if strings.HasPrefix(ns, "p-") && len(ns) > 2 {
		return ns[2:]
	}
	return ""
}

// Deep compare two map[string]any (used for spec drift)
func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch at := va.(type) {
		case map[string]any:
			bt, ok := vb.(map[string]any)
			if !ok {
				return false
			}
			if !mapsEqual(at, bt) {
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
