package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"filippo.io/age"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type CLI struct {
	Endpoints []string `required:"" placeholder:"127.0.0.1:2379" help:"Comma-separated etcd endpoints. Repeatable."`

	Authentication struct {
		InsecureSkipVerify bool `name:"insecure-skip-tls-verify" help:"Skip etcd server certificate verification."`

		Cert   string `type:"existingfile" help:"Client TLS certificate file for etcd connection."`
		Key    string `type:"existingfile" help:"Client TLS key file for etcd connection."`
		CACert string `group:"Authentication" name:"cacert" type:"existingfile" help:"CA bundle to verify etcd server TLS certificates."`

		Username string `help:"Username for etcd authentication."`
		Password string `help:"Password for etcd authentication."`
	} `embed:"" group:"Authentication"`

	// SnapshotStore is nil when --snapshot-store is omitted (snapshot disabled).
	SnapshotStore *SnapshotStoreURL `group:"Snapshot" name:"snapshot-store" help:"Destination for the snapshot. file:///path/to/dir or s3://bucket"`

	Encryption struct {
		Recipient     []string `help:"age recipient to encrypt the snapshot for. Repeatable."`
		RecipientFile []string `name:"recipient-file" type:"existingfile" help:"Path to a file of newline-separated age recipients. Repeatable."`
	} `embed:"" group:"Encryption"`

	Defrag struct {
		// Mode is nil when --defrag is omitted (defragmentation disabled).
		Mode        *DefragMode       `name:"defrag" help:"Defragment etcd nodes after snapshotting. '--defrag=cluster' mode fetches the cluster member list to defrag all nodes, whereas '--defrag=endpoint' mode only defrags the nodes passed to --endpoints."`
		Utilization betweenZeroAndOne `name:"defrag-utilization" default:"1.0" help:"Only defragment nodes where utilization (dbInUse / dbSize) is less than or equal to this."`
		Size        byteQuantity      `name:"defrag-size" help:"Only defragment nodes where DbSize exceeds this. Accepts a byte count or a resource quantity (e.g. 512Mi, 1Gi)."`
	} `embed:"" group:"Defragmentation"`

	Debug bool `help:"Enable client-side debug logging."`

	Timeout time.Duration `default:"30s"`
}

func (c *CLI) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	logger, err := c.buildLogger()
	if err != nil {
		return fmt.Errorf("failed to build logger: %w", err)
	}

	var encryptionRecipients []age.Recipient
	if len(c.Encryption.Recipient) > 0 || len(c.Encryption.RecipientFile) > 0 {
		encryptionRecipients, err = ParseRecipients(c.Encryption.Recipient, c.Encryption.RecipientFile)
		if err != nil {
			return fmt.Errorf("failed to parse age recipients: %w", err)
		}
	}

	tlsConfig, err := c.tlsConfig()
	if err != nil {
		return fmt.Errorf("failed to build TLS config: %w", err)
	}

	cfg := clientv3.Config{
		Context: ctx,

		Endpoints: c.Endpoints,

		TLS: tlsConfig,

		Username: c.Authentication.Username,
		Password: c.Authentication.Password,

		Logger: logger,
	}

	client, err := clientv3.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to etcd: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("error while closing etcd connection", zap.Error(err))
		}
	}()

	if c.SnapshotStore != nil {
		if err := TakeSnapshot(ctx, logger, client, c.SnapshotStore, encryptionRecipients); err != nil {
			return fmt.Errorf("failed to take snapshot: %w", err)
		}
	} else {
		logger.Info("snapshot-store not set, not taking snapshot")
	}

	if c.Defrag.Mode != nil {
		targets, err := ResolveDefragTargets(ctx, logger, client, *c.Defrag.Mode == DefragCluster)
		if err != nil {
			return fmt.Errorf("failed to resolve defragmentation targets: %w", err)
		}
		if err := DoDefrag(ctx, logger, client, targets, float32(c.Defrag.Utilization), c.Defrag.Size.Value()); err != nil {
			return err
		}
	}

	return nil
}

func (c *CLI) buildLogger() (*zap.Logger, error) {
	loggerConfig := zap.NewProductionConfig()
	if c.Debug {
		loggerConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	}

	return loggerConfig.Build()
}

// tlsConfig builds a *tls.Config from the cert flags, or returns nil if none of
// them are set (i.e. plaintext / no client auth).
func (c *CLI) tlsConfig() (*tls.Config, error) {
	if c.Authentication.Cert == "" && c.Authentication.Key == "" && c.Authentication.CACert == "" && !c.Authentication.InsecureSkipVerify {
		return nil, nil
	}

	tlsInfo := transport.TLSInfo{
		CertFile:           c.Authentication.Cert,
		KeyFile:            c.Authentication.Key,
		TrustedCAFile:      c.Authentication.CACert,
		InsecureSkipVerify: c.Authentication.InsecureSkipVerify,
	}
	return tlsInfo.ClientConfig()
}
