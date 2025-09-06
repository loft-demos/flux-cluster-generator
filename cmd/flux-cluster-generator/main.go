package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

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
	namespaceLabelSelectorStr string
)

func main() {
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)

	flag.StringVar(&watchNamespacesCSV, "watch-namespaces", "", "Comma-separated namespaces to watch for Secrets. Empty = all namespaces")
	flag.StringVar(&rsipNamespace, "rsip-namespace", "apps", "Namespace to create RSIPs in")
	flag.StringVar(&labelSelectorStr, "label-selector", "", "Label selector for source Secrets, e.g. env=dev,team=payments")
	flag.StringVar(&secretKey, "secret-key", "config", "Key in the Secret data that contains the kubeconfig")
	flag.StringVar(&rsipNamePrefix, "rsip-name-prefix", "inputs-", "Prefix for generated RSIP names")
	flag.StringVar(&clusterNameKey, "cluster-name-label-key", "vcluster.loft.sh/cluster-name", "Label key on the Secret to derive cluster name")
	flag.StringVar(&copyLabelKeysCSV, "copy-label-keys", "env,team,region", "Comma-separated label keys to copy from Secret to RSIP")
	flag.StringVar(&namespaceLabelSelectorStr, "namespace-label-selector", "", "Label selector for Namespaces to include")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: runtime.NewScheme(),
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	c := mgr.GetClient()

	var ls labels.Selector
	if labelSelectorStr != "" {
		parsed, err := labels.Parse(labelSelectorStr)
		if err != nil {
			logger.Error(err, "invalid label selector")
			os.Exit(1)
		}
		ls = parsed
	} else {
		ls = labels.Everything()
	}

	copyLabelKeys := sets.New[string]()
	for _, k := range splitNonEmpty(copyLabelKeysCSV) {
		copyLabelKeys.Insert(strings.TrimSpace(k))
	}

	allowedNS := newThreadSafeSet()

	reconciler := &SecretMirrorReconciler{
		Client:          c,
		RSIPNamespace:   rsipNamespace,
		Selector:        ls,
		SecretKey:       secretKey,
		RSIPNamePrefix:  rsipNamePrefix,
		ClusterNameKey:  clusterNameKey,
		CopyLabelKeys:   copyLabelKeys,
		AllowedNS:       allowedNS,
	}

	// Namespace watcher
	var nsSel labels.Selector = labels.Everything()
	if namespaceLabelSelectorStr != "" {
		if s, err := labels.Parse(namespaceLabelSelectorStr); err == nil {
			nsSel = s
		} else {
			logger.Error(err, "invalid namespace label selector")
			os.Exit(1)
		}
	}
	nsPred := predicate.NewPredicateFuncs(func(o client.Object) bool { return nsSel.Matches(labels.Set(o.GetLabels())) })
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(nsPred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(&NamespaceSetReconciler{Client: c, AllowedNS: allowedNS, Selector: nsSel}); err != nil {
		logger.Error(err, "unable to create namespace controller")
		os.Exit(1)
	}

	// Secret watcher
	b := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return ls.Matches(labels.Set(o.GetLabels())) && allowedNS.Has(o.GetNamespace())
		}))).
		WithOptions(controller.Options{CacheSyncTimeout: 2 * time.Minute, MaxConcurrentReconciles: 2})
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

// SecretMirrorReconciler

type SecretMirrorReconciler struct {
	client.Client
	RSIPNamespace  string
	Selector       labels.Selector
	SecretKey      string
	RSIPNamePrefix string
	ClusterNameKey string
	CopyLabelKeys  sets.Set[string]
	AllowedNS      *threadSafeSet
}
