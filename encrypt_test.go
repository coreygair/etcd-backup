package main_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	etcdbackup "github.com/coreygair/etcd-backup"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestTakeSnapshot_Encrypted verifies that an encrypted snapshot
// can be decrypted and restored from.
func TestTakeSnapshot_Encrypted(t *testing.T) {
	identity := generateIdentity(t)

	client := newEtcdClusterWithSingleEndpointClient(t)
	check := seedEtcdToTestRestore(t, client)

	dir := t.TempDir()
	if err := etcdbackup.TakeSnapshot(t.Context(), testLogger(t), client, fileSnapshotStoreURL(t, dir), []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatalf("failed to take snapshot: %v", err)
	}

	path := findSnapshotFile(t, dir)
	if !strings.HasSuffix(path, ".age") {
		t.Fatalf("expected encrypted snapshot to have a .age suffix, got %q", path)
	}

	encryptedFile, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open encrypted snapshot: %v", err)
	}
	defer func() { _ = encryptedFile.Close() }()

	decrypted := decryptSnapshot(t, identity, encryptedFile)

	restored := newEtcdFromSnapshotWithClient(t, decrypted)
	check(t, restored)
}

// TestEncryptSnapshot_Recipients verifies that identities passed to EncryptSnapshot
// are able to decrypt the encrypted snapshot.
func TestEncryptSnapshot_Recipients(t *testing.T) {
	// Technically there's no need to use an actual snapshot for this test -
	// we could just pipe any old bytes through the encrypter and check they decrypt correctly.
	client := newEtcdClusterWithClient(t)
	original := snapshotBytes(t, client)

	idA := generateIdentity(t)
	idB := generateIdentity(t)
	idC := generateIdentity(t)

	tests := []struct {
		name             string
		recipients       []string
		recipientFile    []string
		identitiesToTest []*age.X25519Identity
	}{
		{
			name:             "SingleRecipient",
			recipients:       []string{idA.Recipient().String()},
			identitiesToTest: []*age.X25519Identity{idA},
		},
		{
			name:             "SingleRecipientFromFile",
			recipientFile:    []string{idA.Recipient().String(), idB.Recipient().String()},
			identitiesToTest: []*age.X25519Identity{idB},
		},
		{
			name:             "RecipientAndRecipientFile",
			recipients:       []string{idA.Recipient().String()},
			recipientFile:    []string{idB.Recipient().String(), idC.Recipient().String()},
			identitiesToTest: []*age.X25519Identity{idA, idB, idC},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var recipientFiles []string
			if len(tt.recipientFile) > 0 {
				recipientFiles = []string{writeRecipientFile(t, tt.recipientFile...)}
			}

			recipients, err := etcdbackup.ParseRecipients(tt.recipients, recipientFiles)
			if err != nil {
				t.Fatalf("failed to parse recipients: %v", err)
			}

			encryptedReader, err := etcdbackup.EncryptSnapshot(recipients, bytes.NewReader(original))
			if err != nil {
				t.Fatalf("failed to start encrypting snapshot: %v", err)
			}

			encrypted, err := io.ReadAll(encryptedReader)
			if err != nil {
				t.Fatalf("failed to read encrypted snapshot: %v", err)
			}

			for i, identity := range tt.identitiesToTest {
				decrypted, err := io.ReadAll(decryptSnapshot(t, identity, bytes.NewReader(encrypted)))
				if err != nil {
					t.Fatalf("failed to read decrypted snapshot for identity %d: %v", i, err)
				}
				if !bytes.Equal(decrypted, original) {
					t.Fatalf("decrypted snapshot for identity %d does not match original", i)
				}
			}
		})
	}

	t.Run("NoRecipients", func(t *testing.T) {
		if _, err := etcdbackup.ParseRecipients(nil, nil); err == nil {
			t.Fatal("expected an error when parsing no recipients")
		}
	})
}

func generateIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("failed to generate age identity: %v", err)
	}
	return identity
}

func snapshotBytes(t *testing.T, client *clientv3.Client) []byte {
	t.Helper()

	snapshotReader, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("failed to take snapshot from etcd: %v", err)
	}
	defer func() { _ = snapshotReader.Close() }()

	b, err := io.ReadAll(snapshotReader)
	if err != nil {
		t.Fatalf("failed to read snapshot: %v", err)
	}
	return b
}

func writeRecipientFile(t *testing.T, recipients ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "recipients.txt")
	if err := os.WriteFile(path, []byte(strings.Join(recipients, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("failed to write recipient file: %v", err)
	}
	return path
}

func decryptSnapshot(t *testing.T, identity age.Identity, src io.Reader) io.Reader {
	t.Helper()

	r, err := age.Decrypt(src, identity)
	if err != nil {
		t.Fatalf("failed to decrypt snapshot: %v", err)
	}
	return r
}
