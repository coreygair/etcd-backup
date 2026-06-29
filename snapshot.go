package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"filippo.io/age"
	"github.com/alecthomas/kong"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// SnapshotSaver is the interface TakeSnapshot uses to store the snapshot.
type SnapshotSaver interface {
	SaveSnapshot(context.Context, io.Reader) error
}

func TakeSnapshot(ctx context.Context, logger *zap.Logger, client *clientv3.Client, snapshotStore *SnapshotStoreURL, encryptionRecipients []age.Recipient) error {
	encrypted := len(encryptionRecipients) > 0

	extension := ".db"
	if encrypted {
		extension += ".age"
	}

	var snapshotSaver SnapshotSaver
	var err error
	switch snapshotStore.Scheme {
	case "file":
		snapshotSaver = newFileSnapshotSaver(logger, snapshotStore.Path, extension)
	case "s3":
		if snapshotSaver, err = newS3SnapshotSaver(ctx, logger, snapshotStore, extension); err != nil {
			return fmt.Errorf("failed to create s3 snapshot saver: %w", err)
		}
	default:
		return fmt.Errorf("unsupported snapshot store scheme %q", snapshotStore.Scheme)
	}

	snapshotReader, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get snapshot from etcd: %w", err)
	}
	defer func() { _ = snapshotReader.Close() }()

	var reader io.Reader = snapshotReader
	if encrypted {
		reader, err = EncryptSnapshot(encryptionRecipients, snapshotReader)
		if err != nil {
			return fmt.Errorf("failed to encrypt snapshot: %w", err)
		}
	}

	if err := snapshotSaver.SaveSnapshot(ctx, reader); err != nil {
		return err
	}

	return nil
}

var (
	snapshotStoreAllowedSchemes = []string{"file", "s3"}
)

type SnapshotStoreURL url.URL

func (u *SnapshotStoreURL) Decode(ctx *kong.DecodeContext) error {
	var s string
	if err := ctx.Scan.PopValueInto("snapshot-store-url", &s); err != nil {
		return err
	}

	parsed, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", s, err)
	}
	*u = SnapshotStoreURL(*parsed)

	return nil
}

func (u *SnapshotStoreURL) Validate() error {
	if !slices.Contains(snapshotStoreAllowedSchemes, u.Scheme) {
		return fmt.Errorf("unknown scheme %s, must be one of %v", u.Scheme, snapshotStoreAllowedSchemes)
	}

	return nil
}

// fileSnapshotSaver saves snapshots as files in a directory on the local filesystem.
type fileSnapshotSaver struct {
	logger *zap.Logger

	directory string
	extension string
}

func newFileSnapshotSaver(logger *zap.Logger, directory, extension string) *fileSnapshotSaver {
	return &fileSnapshotSaver{
		logger:    logger,
		directory: directory,
		extension: extension,
	}
}

func (f *fileSnapshotSaver) SaveSnapshot(ctx context.Context, snapshotReader io.Reader) error {
	if err := os.MkdirAll(f.directory, 0o755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	snapshotPath := filepath.Join(f.directory, snapshotFilename(f.extension))

	snapshotFile, err := os.Create(snapshotPath)
	if err != nil {
		return fmt.Errorf("failed to create snapshot file: %w", err)
	}

	if _, err = io.Copy(snapshotFile, snapshotReader); err != nil {
		return fmt.Errorf("failed to write snapshot to file: %w", err)
	}

	if err = snapshotFile.Close(); err != nil {
		return fmt.Errorf("failed to close snapshot file: %w", err)
	}

	f.logger.Info("saved snapshot to file", zap.String("path", snapshotPath))

	return nil
}

// s3SnapshotSaver saves snapshots as objects in an S3 (or S3-compatible) bucket.
type s3SnapshotSaver struct {
	logger *zap.Logger

	client    *s3.Client
	bucket    string
	extension string
}

func newS3SnapshotSaver(ctx context.Context, logger *zap.Logger, u *SnapshotStoreURL, extension string) (*s3SnapshotSaver, error) {
	bucket := u.Host
	q := (*url.URL)(u).Query()

	region := q.Get("region")
	endpoint := q.Get("endpoint")

	var optFns []func(*config.LoadOptions) error
	if region != "" {
		optFns = append(optFns, config.WithRegion(region))
	}

	// Accept S3 credentials from environment, useful in non-AWS contexts.
	accessKeyID := os.Getenv("S3_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("S3_SECRET_ACCESS_KEY")
	if accessKeyID != "" && secretAccessKey != "" {
		optFns = append(optFns, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		))
	} else if accessKeyID != "" || secretAccessKey != "" {
		return nil, errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together")
	}
	// If environment credentials aren't set, we fall through to the default AWS SDK credential chain.

	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	usePathStyle := endpoint != ""
	if q.Has("usePathStyle") {
		v, err := strconv.ParseBool(q.Get("usePathStyle"))
		if err != nil {
			return nil, fmt.Errorf("invalid usePathStyle value %q: %w", q.Get("usePathStyle"), err)
		}
		usePathStyle = v
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = new(endpoint)
		}
		o.UsePathStyle = usePathStyle
	})

	return &s3SnapshotSaver{
		logger:    logger,
		client:    client,
		bucket:    bucket,
		extension: extension,
	}, nil
}

func (s *s3SnapshotSaver) SaveSnapshot(ctx context.Context, snapshotReader io.Reader) error {
	key := snapshotFilename(s.extension)

	// The snapshot arrives as an unseekable stream. The AWS SDK can only upload
	// such a stream over HTTPS (via a trailing checksum); against a plain-HTTP
	// S3-compatible endpoint it requires a seekable body. Spool the snapshot to
	// a temp file first so the upload works regardless of endpoint scheme.
	tmp, err := os.CreateTemp("", "etcd-snapshot-*.db")
	if err != nil {
		return fmt.Errorf("failed to create temp snapshot file: %w", err)
	}
	defer func() {
		if err := tmp.Close(); err != nil {
			s.logger.Error("error while closing temporary file", zap.Error(err))
		}
		if err := os.Remove(tmp.Name()); err != nil {
			s.logger.Error("error while deleting temporary file", zap.Error(err))
		}
	}()

	if _, err := io.Copy(tmp, snapshotReader); err != nil {
		return fmt.Errorf("failed to buffer snapshot to temp file: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind temp snapshot file: %w", err)
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: new(s.bucket),
		Key:    new(key),
		Body:   tmp,
	})
	if err != nil {
		return fmt.Errorf("failed to put snapshot object: %w", err)
	}

	s.logger.Info("saved snapshot to s3", zap.String("bucket", s.bucket), zap.String("key", key))

	return nil
}

func snapshotFilename(extension string) string {
	return fmt.Sprintf("%d%s", time.Now().Unix(), extension)
}
