package main_test

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	etcdbackup "github.com/coreygair/etcd-backup"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestTakeSnapshot_FileStore(t *testing.T) {
	client := newEtcdClusterWithSingleEndpointClient(t)
	check := seedEtcdToTestRestore(t, client)

	dir := t.TempDir()
	if err := etcdbackup.TakeSnapshot(t.Context(), testLogger(t), client, fileSnapshotStoreURL(t, dir), nil); err != nil {
		t.Fatalf("failed to take snapshot: %v", err)
	}

	snapshotFile, err := os.Open(findSnapshotFile(t, dir))
	if err != nil {
		t.Fatalf("failed to open snapshot file: %v", err)
	}
	t.Cleanup(func() { _ = snapshotFile.Close() })

	restored := newEtcdFromSnapshotWithClient(t, snapshotFile)
	check(t, restored)
}

func TestTakeSnapshot_S3Store(t *testing.T) {
	g := newGarageS3(t)

	// The s3 snapshot saver reads its credentials from the environment.
	t.Setenv("S3_ACCESS_KEY_ID", g.AccessKeyID)
	t.Setenv("S3_SECRET_ACCESS_KEY", g.SecretAccessKey)

	client := newEtcdClusterWithSingleEndpointClient(t)
	check := seedEtcdToTestRestore(t, client)

	if err := etcdbackup.TakeSnapshot(t.Context(), testLogger(t), client, s3SnapshotStoreURL(t, g), nil); err != nil {
		t.Fatalf("failed to take snapshot: %v", err)
	}

	body := findSnapshotS3Object(t, g)
	defer func() { _ = body.Close() }()

	restored := newEtcdFromSnapshotWithClient(t, body)
	check(t, restored)
}

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zaptest.NewLogger(t)
}

func fileSnapshotStoreURL(t *testing.T, dir string) *etcdbackup.SnapshotStoreURL {
	t.Helper()
	return &etcdbackup.SnapshotStoreURL{Scheme: "file", Path: dir}
}

func s3SnapshotStoreURL(t *testing.T, g *garageS3) *etcdbackup.SnapshotStoreURL {
	t.Helper()

	u := url.URL{
		Scheme: "s3",
		Host:   g.Bucket,
		RawQuery: url.Values{
			"region":   {g.Region},
			"endpoint": {g.Endpoint},
		}.Encode(),
	}
	return (*etcdbackup.SnapshotStoreURL)(&u)
}

// findSnapshotFile returns the path of the single snapshot file written into dir
// by the file snapshot saver, failing if there is not exactly one.
func findSnapshotFile(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read snapshot directory %q: %v", dir, err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one snapshot file in %q, found %d: %v", dir, len(files), files)
	}

	return filepath.Join(dir, files[0])
}
