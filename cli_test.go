package main_test

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	etcdbackup "github.com/coreygair/etcd-backup"
)

// placeholderEndpoints is a valid --endpoints flag
// reused across cases that don't care about the endpoints.
const placeholderEndpoints = "--endpoints=127.0.0.1:2379"

//nolint:goconst
func TestParseCLI(t *testing.T) {
	t.Run("ValidUsage", func(t *testing.T) {
		certFile := existingFile(t, "client.crt")
		keyFile := existingFile(t, "client.key")
		caFile := existingFile(t, "ca.crt")
		recipientFile := existingFile(t, "recipients.txt")

		tests := []struct {
			name  string
			args  []string
			check func(t *testing.T, cli *etcdbackup.CLI)
		}{
			{
				name: "MultipleEndpoints/CommaSeparated",
				args: []string{"--endpoints", "a:2379,b:2379,c:2379"},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					want := []string{"a:2379", "b:2379", "c:2379"}
					if !slices.Equal(cli.Endpoints, want) {
						t.Errorf("Endpoints = %v, want %v", cli.Endpoints, want)
					}
				},
			},
			{
				name: "MultipleEndpoints/RepeatedFlag",
				args: []string{"--endpoints=a:2379", "--endpoints=b:2379"},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					want := []string{"a:2379", "b:2379"}
					if !slices.Equal(cli.Endpoints, want) {
						t.Errorf("Endpoints = %v, want %v", cli.Endpoints, want)
					}
				},
			},
			{
				name: "SnapshotStore/File",
				args: []string{"--snapshot-store", "file:///foo/bar", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got, want := cli.SnapshotStore.Scheme, "file"; got != want {
						t.Errorf("Scheme = %q, want %q", got, want)
					}
					if got, want := cli.SnapshotStore.Path, "/foo/bar"; got != want {
						t.Errorf("Path = %q, want %q", got, want)
					}
				},
			},
			{
				name: "SnapshotStore/S3",
				args: []string{"--snapshot-store", "s3://my-bucket", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got, want := cli.SnapshotStore.Scheme, "s3"; got != want {
						t.Errorf("Scheme = %q, want %q", got, want)
					}
					if got, want := cli.SnapshotStore.Host, "my-bucket"; got != want {
						t.Errorf("Host = %q, want %q", got, want)
					}
				},
			},
			{
				name: "Timeout",
				args: []string{"--timeout", "2m", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got, want := cli.Timeout, 2*time.Minute; got != want {
						t.Errorf("Timeout = %v, want %v", got, want)
					}
				},
			},
			{
				name: "TLSFiles",
				args: []string{"--cert", certFile, "--key", keyFile, "--cacert", caFile, placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if cli.Authentication.Cert == "" {
						t.Error("Cert is empty, want a path")
					}
					if cli.Authentication.Key == "" {
						t.Error("Key is empty, want a path")
					}
					if cli.Authentication.CACert == "" {
						t.Error("CACert is empty, want a path")
					}
				},
			},
			{
				name: "Recipients",
				args: []string{"--recipient", "age1aaa", "--recipient", "age1bbb", "--recipient-file", recipientFile, placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					want := []string{"age1aaa", "age1bbb"}
					if !slices.Equal(cli.Encryption.Recipient, want) {
						t.Errorf("Recipient = %v, want %v", cli.Encryption.Recipient, want)
					}
					if len(cli.Encryption.RecipientFile) != 1 {
						t.Errorf("RecipientFile = %v, want one entry", cli.Encryption.RecipientFile)
					}
				},
			},
			{
				name: "Defrag/Default",
				args: []string{"--defrag", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if cli.Defrag.Mode == nil {
						t.Fatal("Defrag = nil, want cluster")
					}
					if *cli.Defrag.Mode != etcdbackup.DefragCluster {
						t.Errorf("Defrag = %q, want %q", *cli.Defrag.Mode, etcdbackup.DefragCluster)
					}
				},
			},
			{
				name: "Defrag/Endpoints",
				args: []string{"--defrag=endpoints", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if cli.Defrag.Mode == nil {
						t.Fatal("Defrag = nil, want endpoints")
					}
					if *cli.Defrag.Mode != etcdbackup.DefragEndpoints {
						t.Errorf("Defrag = %q, want %q", *cli.Defrag.Mode, etcdbackup.DefragEndpoints)
					}
				},
			},
			{
				name: "Defrag/Cluster",
				args: []string{"--defrag=cluster", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if cli.Defrag.Mode == nil {
						t.Fatal("Defrag = nil, want cluster")
					}
					if *cli.Defrag.Mode != etcdbackup.DefragCluster {
						t.Errorf("Defrag = %q, want %q", *cli.Defrag.Mode, etcdbackup.DefragCluster)
					}
				},
			},
			{
				name: "DefragUtilization",
				args: []string{"--defrag-utilization", "0.5", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got := float32(cli.Defrag.Utilization); got != 0.5 {
						t.Errorf("DefragUtilization = %v, want 0.5", got)
					}
				},
			},
			{
				name: "DefragSize/Quantity",
				args: []string{"--defrag-size", "512Mi", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got, want := cli.Defrag.Size.Value(), int64(512*1024*1024); got != want {
						t.Errorf("DefragSize = %d, want %d", got, want)
					}
				},
			},
			{
				name: "DefragSize/Integer",
				args: []string{"--defrag-size", "1048576", placeholderEndpoints},
				check: func(t *testing.T, cli *etcdbackup.CLI) {
					t.Helper()
					if got, want := cli.Defrag.Size.Value(), int64(1048576); got != want {
						t.Errorf("DefragSize = %d, want %d", got, want)
					}
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cli, err := parseCLI(t, tt.args...)
				if err != nil {
					t.Fatalf("unexpected parse error: %v", err)
				}
				tt.check(t, cli)
			})
		}
	})

	t.Run("InvalidUsage", func(t *testing.T) {
		tests := []struct {
			name string
			args []string
		}{
			{
				name: "SnapshotStore/Required",
				args: []string{},
			},
			{
				name: "SnapshotStore/UnknownScheme",
				args: []string{"--snapshot-store", "badscheme://"},
			},
			{
				name: "Defrag/UnknownMode",
				args: []string{"--defrag=bogus", placeholderEndpoints},
			},
			{
				name: "DefragUtilization/AboveOne",
				args: []string{"--defrag-utilization", "1.5", placeholderEndpoints},
			},
			{
				name: "DefragUtilization/BelowOne",
				args: []string{"--defrag-utilization", "-0.1", placeholderEndpoints},
			},
			{
				name: "DefragSize/Invalid",
				args: []string{"--defrag-size", "not-a-quantity", placeholderEndpoints},
			},
			{
				name: "Timeout/Invalid",
				args: []string{"--timeout", "abc", placeholderEndpoints},
			},
			{
				name: "TLS/NonexistentFile",
				args: []string{"--cert", "/no/such/cert.pem", placeholderEndpoints},
			},
			{
				name: "UnknownFlag",
				args: []string{"--nope", placeholderEndpoints},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := parseCLI(t, tt.args...); err == nil {
					t.Fatal("expected a parse error, got nil")
				}
			})
		}
	})
}

// parseCLI parses args into a fresh CLI struct,
// returning the populated struct or the parse error.
func parseCLI(t *testing.T, args ...string) (*etcdbackup.CLI, error) {
	t.Helper()

	cli := &etcdbackup.CLI{}
	parser, err := kong.New(
		cli,
		kong.Name("etcd-backup"),
		// Discard usage/help output so failing parses don't spam the test log.
		kong.Writers(io.Discard, io.Discard),
	)
	if err != nil {
		t.Fatalf("failed to build kong parser: %v", err)
	}

	_, err = parser.Parse(args)
	return cli, err
}

// existingFile creates an empty file in a temp dir and returns its absolute path,
// for exercising flags declared with kong's "existingfile" type.
func existingFile(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("failed to create file %q: %v", path, err)
	}
	return path
}
