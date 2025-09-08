package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/loft-demos/flux-cluster-generator/internal/controller"
)

func main() {
	// ---- logging ----
	z := zap.Options{Development: false}
	z.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&z)))
	logger := ctrl.Log.WithName("setup")

	// ---- bind flags directly into Options ----
	var opts controller.Options
	flag.StringVar(&opts.RSIPNamespace, "rsip-namespace", "flux-apps", "Namespace to create RSIPs in")
	flag.StringVar(&opts.LabelSelectorStr, "label-selector", "", "Label selector for source Secrets (e.g. fluxcd.io/secret-type=cluster)")
	flag.StringVar(&opts.SecretKey, "secret-key", "config", "Key in Secret.data that contains the kubeconfig")
	flag.StringVar(&opts.RSIPNamePrefix, "rsip-name-prefix", "inputs-", "Prefix for generated RSIP names")
	flag.StringVar(&opts.ClusterNameKey, "cluster-name-label-key", "vci.flux.loft.sh/name", "Label key on the Secret to derive cluster name")
	flag.StringVar(&opts.ProjectLabelKey, "project-label-key", "vci.flux.loft.sh/project", "Label key on the Secret containing the VCI project")
	flag.StringVar(&opts.CopyLabelKeysCSV, "copy-label-keys", "env,team,region", "Comma-separated label KEYS to copy from Secret to RSIP")
	flag.StringVar(&opts.CopyLabelPrefixesCSV, "copy-label-prefixes", "", "Comma-separated label KEY PREFIXES to copy (e.g. flux-app/)")
	flag.StringVar(&opts.NamespaceLabelSelectorStr, "namespace-label-selector", "", "Label selector for Namespaces to include (e.g. flux-cluster-generator-enabled=true)")
	flag.StringVar(&opts.WatchNamespacesCSV, "watch-namespaces", "", "Comma-separated namespaces to watch (empty = all)")

	// tuning
	flag.IntVar(&opts.MaxConcurrent, "max-concurrent", 2, "MaxConcurrentReconciles for the Secret controller")
	var cacheSyncSeconds int
	flag.IntVar(&cacheSyncSeconds, "cache-sync-seconds", 120, "Cache sync timeout (seconds)")

	flag.Parse()
	opts.CacheSyncTimeout = time.Duration(cacheSyncSeconds) * time.Second

	// parse selectors / CSVs and validate required fields
	if err := opts.FillAndValidate(); err != nil {
		logger.Error(err, "invalid options")
		os.Exit(1)
	}

	// ---- manager ----
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// add metrics/probes here if you want:
		// Metrics: server.Options{BindAddress: ":8080"},
		// HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// ---- wire controller(s) ----
	if err := controller.SetupRSIPController(mgr, opts); err != nil {
		logger.Error(err, "unable to setup RSIP controller")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}
