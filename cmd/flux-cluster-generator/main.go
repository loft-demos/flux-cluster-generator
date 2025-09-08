package main

import (
	"flag"
	"os"
	"strings"
	"time"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/loft-demos/flux-cluster-generator/internal/controller"
)

func main() {
	// ----- logging / flags
	z := zap.Options{Development: false}
	z.BindFlags(flag.CommandLine)

	opts := controller.Options{} // see options.go
	flag.StringVar(&opts.RSIPNamespace, "rsip-namespace", "flux-apps", "Namespace to create RSIPs in")
	flag.StringVar(&opts.SecretKey, "secret-key", "config", "Key in Secret.data with kubeconfig")
	flag.StringVar(&opts.RSIPNamePrefix, "rsip-name-prefix", "inputs-", "Prefix for RSIP names")
	flag.StringVar(&opts.ClusterNameKey, "cluster-name-label-key", "vci.flux.loft.sh/name", "Label key for cluster name")
	flag.StringVar(&opts.ProjectLabelKey, "project-label-key", "vci.flux.loft.sh/project", "Label key for project")
	flag.StringVar(&opts.LabelSelectorStr, "label-selector", "", "Selector for source Secrets")
	flag.StringVar(&opts.NamespaceLabelSelectorStr, "namespace-label-selector", "", "Selector for Namespaces to include")
	flag.StringVar(&opts.CopyLabelKeysCSV, "copy-label-keys", "env,team,region", "Label KEYS to copy")
	flag.StringVar(&opts.CopyLabelPrefixesCSV, "copy-label-prefixes", "", "Label KEY PREFIXES to copy (e.g. flux-app/)")
	flag.StringVar(&opts.WatchNamespacesCSV, "watch-namespaces", "", "Optional list of namespaces to watch")
	flag.IntVar(&opts.MaxConcurrent, "concurrency", 2, "Max concurrent reconciles")
	flag.DurationVar(&opts.CacheSyncTimeout, "cache-sync-timeout", 2*time.Minute, "Informer cache sync timeout")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&z)))

	// ----- scheme / manager
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: ":8080"},
		HealthProbeBindAddress: ":8081",
		LeaderElection:         true,
		LeaderElectionID:       "flux-cluster-generator",
	})
	if err != nil {
		panic(err)
	}

	// Expand CSVs to slices/sets once
	opts.CopyLabelKeys = controller.SplitNonEmpty(opts.CopyLabelKeysCSV)
	opts.CopyLabelPrefixes = controller.SplitNonEmpty(opts.CopyLabelPrefixesCSV)
	opts.WatchNamespaces = controller.SplitNonEmpty(opts.WatchNamespacesCSV)
	opts.LabelSelector = controller.ParseSelectorOrDie(opts.LabelSelectorStr)
	opts.NamespaceSelector = controller.ParseSelectorOrDie(opts.NamespaceLabelSelectorStr)

	// ----- wire controllers
	if err := controller.SetupNamespaceSetController(mgr, opts); err != nil {
		panic(err)
	}
	if err := controller.SetupRSIPController(mgr, opts); err != nil {
		panic(err)
	}

	// healthz/readyz
	_ = mgr.AddHealthzCheck("ping", healthz.Ping)
	_ = mgr.AddReadyzCheck("ping", healthz.Ping)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}

	_ = os.Stdout.Sync()
	_ = strings.Builder{} // avoid unused imports on some toolchains
}
