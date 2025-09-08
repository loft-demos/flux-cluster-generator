package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// sweepOrphanRSIPs scans RSIPs in rsipNS and deletes any whose referenced Secret no longer exists.
func sweepOrphanRSIPs(
	ctx context.Context,
	log logr.Logger,
	reader client.Reader, // uncached read
	writer client.Client, // for deletes
	rsipNS string,
) error {
	var rsips unstructured.UnstructuredList
	rsips.SetGroupVersionKind(schema.GroupVersionKind{
		Group: rsipGVK.Group, Version: rsipGVK.Version, Kind: rsipGVK.Kind + "List",
	})

	if err := reader.List(ctx, &rsips, client.InNamespace(rsipNS)); err != nil {
		return fmt.Errorf("list RSIPs: %w", err)
	}

	deleted := 0
	for i := range rsips.Items {
		rsip := &rsips.Items[i]
		lbl := rsip.GetLabels()
		secNS, okNS := lbl["mirror.fluxcd.io/secretNS"]
		secName, okName := lbl["mirror.fluxcd.io/secretName"]
		if !okNS || !okName || secNS == "" || secName == "" {
			continue // not managed by us
		}

		// Does the Secret still exist?
		var sec corev1.Secret
		err := reader.Get(ctx, types.NamespacedName{Namespace: secNS, Name: secName}, &sec)
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "secret existence check failed",
				"rsip", rsip.GetName(), "secret", fmt.Sprintf("%s/%s", secNS, secName))
			continue
		}
		if err == nil {
			continue // Secret exists -> keep RSIP
		}

		// Secret not found -> delete the RSIP
		if err := writer.Delete(ctx, rsip); client.IgnoreNotFound(err) != nil {
			log.Error(err, "failed deleting orphan RSIP", "name", rsip.GetName())
		} else {
			deleted++
			log.Info("deleted orphan RSIP", "name", rsip.GetName(),
				"secret", fmt.Sprintf("%s/%s", secNS, secName))
		}
	}

	if deleted > 0 {
		log.Info("orphan RSIP sweep complete", "namespace", rsipNS, "deleted", deleted)
	}
	return nil
}
