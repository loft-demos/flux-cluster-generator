package controller

import (
	"fmt"
	"strings"
	"time"
	"text/template" 

	"k8s.io/apimachinery/pkg/labels"
)

// Options carries raw flag values plus parsed/derived forms used by the controller.
type Options struct {
	// Core behavior
	RSIPNamespace   string
	SecretKey       string
	RSIPNamePrefix  string
	ClusterNameKey  string
	ProjectLabelKey string
	RSIPNameTemplateStr string
	RSIPNameTemplate    *template.Template

	// Selectors / filters (raw strings for flags)
	LabelSelectorStr          string
	NamespaceLabelSelectorStr string
	WatchNamespacesCSV        string
	CopyLabelKeysCSV          string
	CopyLabelPrefixesCSV      string

	// Parsed / derived
	LabelSelector     labels.Selector
	NamespaceSelector labels.Selector
	WatchNamespaces   []string
	CopyLabelKeys     []string
	CopyLabelPrefixes []string

	// Tuning
	MaxConcurrent    int
	CacheSyncTimeout time.Duration
}

// FillAndValidate parses raw strings into selectors/slices, applies defaults, and validates.
func (o *Options) FillAndValidate() error {
	// Defaults
	if o.RSIPNamespace == "" {
		o.RSIPNamespace = "flux-apps"
	}
	if o.SecretKey == "" {
		o.SecretKey = "config"
	}
	if o.RSIPNamePrefix == "" {
		o.RSIPNamePrefix = "inputs-"
	}
	if o.ClusterNameKey == "" {
		o.ClusterNameKey = "vci.flux.loft.sh/name"
	}
	if o.ProjectLabelKey == "" {
		o.ProjectLabelKey = "vci.flux.loft.sh/project"
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 2
	}
	if o.CacheSyncTimeout <= 0 {
		o.CacheSyncTimeout = 2 * time.Minute
	}

	// Parse selectors
	if o.LabelSelectorStr == "" {
		o.LabelSelector = labels.Everything()
	} else {
		sel, err := labels.Parse(o.LabelSelectorStr)
		if err != nil {
			return fmt.Errorf("invalid label selector %q: %w", o.LabelSelectorStr, err)
		}
		o.LabelSelector = sel
	}

	if o.NamespaceLabelSelectorStr == "" {
		o.NamespaceSelector = labels.Everything()
	} else {
		nsSel, err := labels.Parse(o.NamespaceLabelSelectorStr)
		if err != nil {
			return fmt.Errorf("invalid namespace label selector %q: %w", o.NamespaceLabelSelectorStr, err)
		}
		o.NamespaceSelector = nsSel
	}

	// CSV → slices
	o.WatchNamespaces = splitNonEmpty(o.WatchNamespacesCSV)
	o.CopyLabelKeys = splitNonEmpty(o.CopyLabelKeysCSV)
	o.CopyLabelPrefixes = splitNonEmpty(o.CopyLabelPrefixesCSV)

	return nil
}

// splitNonEmpty splits by comma and trims; empty entries are dropped.
func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
