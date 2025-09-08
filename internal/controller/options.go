package controller

import (
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

type Options struct {
	// core behavior
	RSIPNamespace string
	SecretKey     string
	RSIPNamePrefix string
	ClusterNameKey string
	ProjectLabelKey string

	// selectors / filters (raw strings for flags)
	LabelSelectorStr          string
	NamespaceLabelSelectorStr string
	WatchNamespacesCSV        string
	CopyLabelKeysCSV          string
	CopyLabelPrefixesCSV      string

	// parsed / derived
	LabelSelector     labels.Selector
	NamespaceSelector labels.Selector
	WatchNamespaces   []string
	CopyLabelKeys     []string
	CopyLabelPrefixes []string

	// tuning
	MaxConcurrent     int
	CacheSyncTimeout  time.Duration
}
