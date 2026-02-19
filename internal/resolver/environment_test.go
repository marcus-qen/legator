/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resolver

import (
	"testing"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

func TestBuildDataIndex(t *testing.T) {
	dr := &corev1alpha1.DataResourcesSpec{
		Databases: []corev1alpha1.DataResourceRef{
			{Kind: "cnpg.io/Cluster", Namespace: "backstage", Name: "backstage-db"},
			{Kind: "cnpg.io/Cluster", Namespace: "ideavault", Name: "ideavault-db"},
		},
		PersistentStorage: []corev1alpha1.DataResourceRef{
			{Kind: "PersistentVolumeClaim", Namespace: "harbor", Name: "harbor-registry"},
		},
		ObjectStorage: []corev1alpha1.ObjectStorageRef{
			{Kind: "s3-bucket", Name: "cnpg-backups", Provider: "minio"},
		},
	}

	idx := buildDataIndex(dr)

	// Namespace checks
	if !idx.HasDataInNamespace("backstage") {
		t.Error("backstage should have data")
	}
	if !idx.HasDataInNamespace("ideavault") {
		t.Error("ideavault should have data")
	}
	if !idx.HasDataInNamespace("harbor") {
		t.Error("harbor should have data")
	}
	if idx.HasDataInNamespace("monitoring") {
		t.Error("monitoring should NOT have data")
	}

	// Direct resource checks
	if !idx.IsDataResource("cnpg.io/Cluster", "backstage", "backstage-db") {
		t.Error("backstage-db should be a data resource")
	}
	if idx.IsDataResource("Deployment", "backstage", "backstage-frontend") {
		t.Error("backstage-frontend should NOT be a data resource")
	}

	// Object storage checks
	if !idx.IsProtectedObjectStorage("cnpg-backups") {
		t.Error("cnpg-backups should be protected")
	}
	if idx.IsProtectedObjectStorage("random-bucket") {
		t.Error("random-bucket should NOT be protected")
	}
}

func TestBuildDataIndex_Nil(t *testing.T) {
	idx := buildDataIndex(nil)
	if idx.HasDataInNamespace("anything") {
		t.Error("nil spec should have no data namespaces")
	}
}

func TestBuildDataIndex_Empty(t *testing.T) {
	idx := buildDataIndex(&corev1alpha1.DataResourcesSpec{})
	if idx.HasDataInNamespace("anything") {
		t.Error("empty spec should have no data namespaces")
	}
}
