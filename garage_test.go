package main_test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	garageTag   = "v2.3.0"
	garageImage = "dxflrs/garage:" + garageTag

	garageBucket = "etcd-backup"
	garageRegion = "garage"
	garageS3Port = "3900/tcp"
)

// garageS3 holds the connection details for the Garage S3 API
// stood up for a test by NewGarageS3.
type garageS3 struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// newGarageS3 starts a single node Garage instance tied to the lifetime of the current test
// and returns the details required to talk to its S3 API.
func newGarageS3(t *testing.T) *garageS3 {
	t.Helper()

	ctx := t.Context()

	accessKeyID := "GK" + randomHex(t, 12)
	secretAccessKey := randomHex(t, 32)

	configToml := fmt.Sprintf(`
metadata_dir = "/tmp/meta"
data_dir = "/tmp/data"
db_engine = "sqlite"
replication_factor = 1

rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "%s"

[s3_api]
s3_region = "%s"
api_bind_addr = "[::]:3900"

[admin]
api_bind_addr = "[::]:3903"
admin_token = "%s"
`, randomHex(t, 32), garageRegion, randomHex(t, 32))

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        garageImage,
			ExposedPorts: []string{garageS3Port},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(configToml),
				ContainerFilePath: "/etc/garage.toml",
				FileMode:          0o444,
			}},
			Env: map[string]string{
				"GARAGE_DEFAULT_ACCESS_KEY": accessKeyID,
				"GARAGE_DEFAULT_SECRET_KEY": secretAccessKey,
				"GARAGE_DEFAULT_BUCKET":     garageBucket,
			},
			Cmd: []string{"/garage", "server", "--single-node", "--default-bucket"},
			// bucket info only exits 0 once the node is up and the default bucket has been provisioned.
			WaitingFor: wait.ForExec([]string{"/garage", "bucket", "info", garageBucket}).WithExitCode(0),
		},
		Started: true,
	})
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("failed to terminate garage container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("failed to start garage container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get garage host: %v", err)
	}
	port, err := container.MappedPort(ctx, garageS3Port)
	if err != nil {
		t.Fatalf("failed to get garage s3 port: %v", err)
	}
	endpoint := url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", host, port.Num()),
	}

	return &garageS3{
		Endpoint:        endpoint.String(),
		Region:          garageRegion,
		Bucket:          garageBucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to generate random bytes: %v", err)
	}
	return hex.EncodeToString(b)
}

// findSnapshotS3Object builds an aws-sdk-go-v2 client with the Garage
// endpoint/credentials and searches the bucket for a single snapshot object.
//
// Makes the assumption a fresh Garage instance/bucket is spun up for each test that needs it
// so only one snapshot object will be present.
func findSnapshotS3Object(t *testing.T, g *garageS3) io.ReadCloser {
	t.Helper()

	ctx := t.Context()

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(g.Region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(g.AccessKeyID, g.SecretAccessKey, ""),
		),
	)
	if err != nil {
		t.Fatalf("failed to load aws config: %v", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(g.Endpoint)
		o.UsePathStyle = true
	})

	listResponse, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(g.Bucket),
	})
	if err != nil {
		t.Fatalf("failed to list objects in bucket %q: %v", g.Bucket, err)
	}
	if len(listResponse.Contents) != 1 {
		t.Fatalf("expected exactly one object in bucket %q, found %d", g.Bucket, len(listResponse.Contents))
	}

	getResponse, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &g.Bucket,
		Key:    listResponse.Contents[0].Key,
	})
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	return getResponse.Body
}
