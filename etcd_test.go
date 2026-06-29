package main_test

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/etcd"
	"github.com/testcontainers/testcontainers-go/wait"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	etcdDigest = "sha256:3c2ced08f23b1183e8bd4613064c3fb6b8db5057a4d1f13c3518c76e357a07a8" // v3.6.12
	etcdImage  = "gcr.io/etcd-development/etcd@" + etcdDigest
)

// newEtcdCluster starts a fresh 3 node etcd cluster tied to the lifetime of the current test.
//
// Returns the client endpoints of each node in the cluster.
func newEtcdCluster(t *testing.T) []string {
	t.Helper()

	container, err := etcd.Run(t.Context(), etcdImage, etcd.WithNodes("etcd-a", "etcd-b", "etcd-c"))
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("failed to terminate etcd cluster: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("failed to start etcd cluster: %v", err)
	}

	endpoints, err := container.ClientEndpoints(t.Context())
	if err != nil {
		t.Fatalf("failed to get etcd endpoints: %v", err)
	}

	return endpoints
}

// newEtcdClusterWithClient calls NewEtcdCluster and returns a client for the created cluster.
func newEtcdClusterWithClient(t *testing.T) *clientv3.Client {
	t.Helper()
	return newEtcdClient(t, newEtcdCluster(t))
}

// newEtcdClusterWithSingleEndpointClient starts a fresh cluster and returns a
// client pinned to a single one of its endpoints.
//
// Use this to avoid a race between seedEtcdToTestRestore and taking a snapshot.
// The ectd client Maintenance.Snapshot and KV.Put methods are served by
// whichever member the client's round-robin balancer happens to pick.
// Using a multi-endpoint client to perform a Put followed by a snapshot
// (for example, calling seedEtcdToTestRestore before testing TakeSnapshot)
// can cause the snapshot operation to be routed to a follower that hasn't yet applied
// the just-written key, producing a snapshot that's missing it.
// Pinning to one endpoint keeps the seed write and the snapshot on the same member.
func newEtcdClusterWithSingleEndpointClient(t *testing.T) *clientv3.Client {
	t.Helper()
	endpoints := newEtcdCluster(t)
	return newEtcdClient(t, endpoints[:1])
}

const etcdDataDir = "/data.etcd"

// newEtcdFromSnapshot restores a snapshot with etcdutl and boots
// a fresh single node etcd cluster from the restored data.
//
// Returns the client endpoints of the restored node.
func newEtcdFromSnapshot(t *testing.T, snapshotReader io.Reader) []string {
	t.Helper()

	ctx := t.Context()

	// A named volume shared between the restore step and the boot step.
	// etcdutl writes the restored data directory here, then the etcd node reads it back.
	volumeName := fmt.Sprintf("etcd-restore-%d", time.Now().UnixNano())
	volumeMount := testcontainers.VolumeMount(volumeName, etcdDataDir)

	// Restore the snapshot into the shared volume.
	restore, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      etcdImage,
			Entrypoint: []string{"/usr/local/bin/etcdutl"},
			Cmd:        []string{"snapshot", "restore", "/snapshot.db", "--data-dir", etcdDataDir},
			Files: []testcontainers.ContainerFile{{
				Reader:            snapshotReader,
				ContainerFilePath: "/snapshot.db",
				FileMode:          0o644,
			}},
			Mounts:     testcontainers.Mounts(volumeMount),
			WaitingFor: wait.ForExit(),
		},
		Started: true,
	})
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(restore, testcontainers.RemoveVolumes(volumeName)); err != nil {
			t.Logf("failed to terminate snapshot restore container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("failed to run snapshot restore: %v", err)
	}

	state, err := restore.State(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot restore state: %v", err)
	}
	if state.ExitCode != 0 {
		logs, _ := restore.Logs(ctx)
		out, _ := io.ReadAll(logs)
		t.Fatalf("snapshot restore exited with code %d:\n%s", state.ExitCode, out)
	}

	// Boot a single node etcd cluster from the restored data directory in the shared volume.
	boot, err := etcd.Run(ctx, etcdImage,
		etcd.WithDataDir(), // --data-dir=/data.etcd
		testcontainers.WithMounts(volumeMount),
		testcontainers.WithWaitStrategy(wait.ForLog("ready to serve client requests")),
	)
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(boot); err != nil {
			t.Logf("failed to terminate restored etcd node: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("failed to boot restored etcd node: %v", err)
	}

	endpoints, err := boot.ClientEndpoints(ctx)
	if err != nil {
		t.Fatalf("failed to get restored etcd endpoint: %v", err)
	}

	return endpoints
}

func newEtcdFromSnapshotWithClient(t *testing.T, snapshotReader io.Reader) *clientv3.Client {
	t.Helper()
	return newEtcdClient(t, newEtcdFromSnapshot(t, snapshotReader))
}

func newEtcdClient(t *testing.T, endpoints []string) *clientv3.Client {
	t.Helper()

	cfg := clientv3.Config{
		Context:   t.Context(),
		Endpoints: endpoints,
	}

	client, err := clientv3.New(cfg)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("failed to close etcd client: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("failed to connect to etcd: %v", err)
	}

	return client
}

// seedEtcdToTestRestore seeds an etcd cluster with a random key-value pair,
// returning a function that can be used to check that the same pair
// exists on a fresh etcd that was restored from a snapshot.
func seedEtcdToTestRestore(t *testing.T, client *clientv3.Client) func(*testing.T, *clientv3.Client) {
	t.Helper()

	key := randomHex(t, 8)
	value := randomHex(t, 8)

	if _, err := client.Put(t.Context(), key, value); err != nil {
		t.Fatalf("failed to seed etcd: %v", err)
	}

	return func(t *testing.T, client *clientv3.Client) {
		t.Helper()

		resp, err := client.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("failed to read from restored etcd: %v", err)
		}

		if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != value {
			t.Fatalf("restored etcd missing key %q=%q, got %v", key, value, resp.Kvs)
		}
	}
}
