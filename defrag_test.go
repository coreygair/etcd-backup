package main_test

import (
	"testing"

	etcdbackup "github.com/coreygair/etcd-backup"
)

// TestResolveDefragTargets checks that defrag targets are resolved either from
// the explicitly configured endpoints or from the full cluster member list.
//
// The cluster nodes advertise docker network hostnames that aren't reachable from the host running the test,
// so only the number of resolved targets is asserted, not their contents.
func TestResolveDefragTargets(t *testing.T) {
	endpoints := newEtcdCluster(t)

	// A client configured with only one of the three cluster endpoints,
	// so the endpoints and cluster resolution modes should return different target lists.
	client := newEtcdClient(t, endpoints[:1])

	t.Run("Endpoints", func(t *testing.T) {
		targets, err := etcdbackup.ResolveDefragTargets(t.Context(), testLogger(t), client, false)
		if err != nil {
			t.Fatalf("failed to resolve defrag targets: %v", err)
		}
		if len(targets) != 1 {
			t.Fatalf("expected 1 target from the configured endpoint, got %d", len(targets))
		}
	})

	t.Run("Cluster", func(t *testing.T) {
		targets, err := etcdbackup.ResolveDefragTargets(t.Context(), testLogger(t), client, true)
		if err != nil {
			t.Fatalf("failed to resolve defrag targets: %v", err)
		}
		if len(targets) != 3 {
			t.Fatalf("expected 3 targets from the cluster member list, got %d", len(targets))
		}
	})
}

func TestDoDefrag(t *testing.T) {
	client := newEtcdClusterWithClient(t)

	// Resolve from the configured endpoints so the targets are reachable from the host.
	targets, err := etcdbackup.ResolveDefragTargets(t.Context(), testLogger(t), client, false)
	if err != nil {
		t.Fatalf("failed to resolve defrag targets: %v", err)
	}

	if err := etcdbackup.DoDefrag(t.Context(), testLogger(t), client, targets, 1.0, 0); err != nil {
		t.Fatalf("DoDefrag returned an error: %v", err)
	}
}
