// internal/controller/rsip_reconciler.go
package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var rsipGVK = schema.GroupVersionKind{
	Group:   "fluxcd.controlplane.io",
	Version: "v1",
	Kind:    "ResourceSetInputProvider",
}

type SecretMirrorReconciler struct {
	client.Client
	APIReader client.Reader
	Recorder  record.EventRecorder

	Opts      Options
	allowedNS *threadSafeSet
}

func (r *SecretMirrorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (reconcile.Result, error) {
	log := ctrl.Log.WithName("rsip").WithValues("secret", req.NamespacedName.String())

	var sec corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &sec); err != nil {
		// Secret is gone — cleanup any RSIPs that referenced it
		if err2 := r.ensureRSIPAbsence(ctx, req.NamespacedName); err2 != nil {
			log.Error(err2, "cleanup after secret deletion failed")
			return reconcile.Result{}, err2
		}
		log.V(1).Info("cleaned up after secret deletion")
		return reconcile.Result{}, nil
	}

	// filters
	if !r.allowedNS.Has(sec.Namespace) {
		log.V(1).Info("namespace not in allowlist; ensuring cleanup", "namespace", sec.Namespace)
		_ = r.ensureRSIPAbsence(ctx, req.NamespacedName)
		return reconcile.Result{}, nil
	}
	if !r.Opts.LabelSelector.Matches(labels.Set(sec.Labels)) {
		log.V(1).Info("secret does not match label selector; ensuring cleanup",
			"selector", r.Opts.LabelSelector.String())
		_ = r.ensureRSIPAbsence(ctx, req.NamespacedName)
		return reconcile.Result{}, nil
	}
	if _, ok := sec.Data[r.Opts.SecretKey]; !ok {
		log.Info("secret missing kubeconfig key; skipping", "key", r.Opts.SecretKey)
		return reconcile.Result{}, nil
	}

	// names/ids
	clusterName := sec.Labels[r.Opts.ClusterNameKey]
	if clusterName == "" {
		clusterName = sec.Name
	}
	if errs := validation.IsDNS1123Label(clusterName); len(errs) > 0 {
		log.V(1).Info("sanitizing cluster name to DNS-1123", "errors", errs)
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
			r.Recorder.Eventf(&sec, corev1.EventTypeWarning, "RSIPCreateFailed",
				"failed to create RSIP %s/%s: %v", r.Opts.RSIPNamespace, rsipName, err)
			log.Error(err, "create RSIP failed", "name", rsipName, "ns", r.Opts.RSIPNamespace)
			return reconcile.Result{}, err
		}
		r.Recorder.Eventf(&sec, corev1.EventTypeNormal, "RSIPCreated",
			"created RSIP %s/%s", r.Opts.RSIPNamespace, rsipName)
		log.Info("created RSIP", "name", rsipName, "ns", r.Opts.RSIPNamespace)
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
			r.Recorder.Eventf(&sec, corev1.EventTypeWarning, "RSIPUpdateFailed",
				"failed to update RSIP %s/%s: %v", r.Opts.RSIPNamespace, rsipName, err)
			log.Error(err, "update RSIP failed", "name", rsipName)
			return reconcile.Result{}, err
		}
		r.Recorder.Eventf(&sec, corev1.EventTypeNormal, "RSIPUpdated",
			"updated RSIP %s/%s", r.Opts.RSIPNamespace, rsipName)
		log.Info("updated RSIP", "name", rsipName)
	} else {
		log.V(1).Info("RSIP up-to-date", "name", rsipName)
	}
	return reconcile.Result{}, nil
}

func (r *SecretMirrorReconciler) ensureRSIPAbsence(ctx context.Context, secretNN types.NamespacedName) error {
	log := ctrl.Log.WithName("gc")

	// List by labels (works even when the Secret is already gone)
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: rsipGVK.Group, Version: rsipGVK.Version, Kind: rsipGVK.Kind + "List",
	})
	if err := r.APIReader.List(ctx, &list,
		client.InNamespace(r.Opts.RSIPNamespace),
		client.MatchingLabels{
			"mirror.fluxcd.io/secretNS":   secretNN.Namespace,
			"mirror.fluxcd.io/secretName": secretNN.Name,
		},
	); err != nil {
		log.Error(err, "list RSIPs for cleanup failed", "secret", secretNN.String())
		return err
	}

	if len(list.Items) == 0 {
		log.V(1).Info("no RSIPs to delete for secret", "secret", secretNN.String())
		return nil
	}

	deleted := 0
	var errs []error
	for i := range list.Items {
		rsip := &list.Items[i]
		if err := r.Delete(ctx, rsip); client.IgnoreNotFound(err) != nil {
			errs = append(errs, err)
			log.Error(err, "delete RSIP failed", "name", rsip.GetName())
		} else {
			deleted++
			log.Info("deleted RSIP", "name", rsip.GetName())
		}
	}
	// No Eventf here: we don't have a runtime.Object for a deleted Secret.
	if deleted > 0 {
		log.Info("deleted RSIPs for secret", "secret", secretNN.String(), "count", deleted)
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleanup had %d error(s), deleted=%d", len(errs), deleted)
	}
	return nil
}
