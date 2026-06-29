package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/alecthomas/kong"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
)

type DefragMode string

const (
	DefragEndpoints DefragMode = "endpoints"
	DefragCluster   DefragMode = "cluster"
)

// IsBool lets --defrag be passed bare (defaulting to "cluster") in addition to --defrag=<mode>.
func (*DefragMode) IsBool() bool { return true }

// Decode reads the optional --defrag value.
//
// A bare --defrag defaults to defragCluster.
func (d *DefragMode) Decode(ctx *kong.DecodeContext) error {
	if ctx.Scan.Peek().Type != kong.FlagValueToken {
		// --defrag
		*d = DefragCluster
		return nil
	}

	// --defrag=<mode>
	token := ctx.Scan.Pop()
	mode, _ := token.Value.(string)
	switch DefragMode(mode) {
	case DefragEndpoints, DefragCluster:
		*d = DefragMode(mode)
		return nil
	default:
		return fmt.Errorf("must be %q or %q (or bare) but got %q", DefragEndpoints, DefragCluster, mode)
	}
}

type betweenZeroAndOne float32

func (x betweenZeroAndOne) Validate() error {
	if x < 0 || x > 1.0 {
		return errors.New("must be between 0.0 and 1.0")
	}
	return nil
}

type byteQuantity struct {
	resource.Quantity
}

func (q *byteQuantity) Decode(ctx *kong.DecodeContext) error {
	var raw string
	if err := ctx.Scan.PopValueInto("size", &raw); err != nil {
		return err
	}
	parsed, err := resource.ParseQuantity(raw)
	if err != nil {
		return fmt.Errorf("invalid quantity: %w", err)
	}
	q.Quantity = parsed
	return nil
}

type DefragTarget struct {
	name     string
	endpoint string
}

// ResolveDefragTargets determines which etcd nodes to defragment.
//
// If cluster == true, all cluster members are discovered via the membership list.
// If not, only the explicitly configured endpoints are used.
//
// Members with no client URLs are skipped.
func ResolveDefragTargets(ctx context.Context, logger *zap.Logger, client *clientv3.Client, cluster bool) ([]DefragTarget, error) {
	if !cluster {
		endpoints := client.Endpoints()

		targets := make([]DefragTarget, len(endpoints))
		for i, endpoint := range endpoints {
			targets[i] = DefragTarget{name: endpoint, endpoint: endpoint}
		}
		return targets, nil
	}

	memberList, err := client.MemberList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list cluster members: %w", err)
	}

	targets := make([]DefragTarget, 0, len(memberList.Members))
	for _, member := range memberList.Members {
		if len(member.ClientURLs) == 0 {
			logger.Debug("skipping member with no client URLs", zap.String("member", member.Name))
			continue
		}
		targets = append(targets, DefragTarget{name: member.Name, endpoint: member.ClientURLs[0]})
	}
	return targets, nil
}

// DoDefrag calls the defragment endpoint one-by-one on a list of targets.
//
// Attempts to defragment each target before reporting whether any errors occurred,
// although individual successes/failures are logged as they happen.
//
// Targets with database utilization (DbSizeInUse / DbSize)
// greater than utilizationThreshold will be skipped.
//
// Targets with DbSize less than or equal to minDbSize will also be skipped.
func DoDefrag(ctx context.Context, logger *zap.Logger, client *clientv3.Client, targets []DefragTarget, utilizationThreshold float32, minDbSize int64) error {
	logger.Info("starting defragmentation", zap.Int("nTargets", len(targets)))

	// Log errors where they happen instead of accumulating them into the returned error.
	// Keep count of errors as useful context though.
	var failures int

	// Aim to log exactly once per target at info level or above
	// to make it really easy to pick out the status of each defrag attempt.
	for _, target := range targets {
		logger := logger.With(zap.String("endpoint", target.endpoint))
		if target.name != target.endpoint {
			logger = logger.With(zap.String("member", target.name))
		}

		status, err := client.Status(ctx, target.endpoint)
		if err != nil {
			logger.Error("failed to check target status before defragmentation", zap.Error(err))
			failures++
			continue
		}

		logger = logger.With(
			zap.Int64("dbSize", status.DbSize),
			zap.Int64("dbSizeInUse", status.DbSizeInUse),
		)

		if status.DbSize == 0 {
			logger.Info("skipping defragmentation due to dbSize == 0")
			continue
		}

		if status.DbSize <= minDbSize {
			logger.Info("skipping defragmentation due to dbSize below threshold")
			continue
		}

		utilization := float32(status.DbSizeInUse) / float32(status.DbSize)

		logger = logger.With(zap.String("utilization", fmt.Sprintf("%.2f", utilization)))

		if utilization > utilizationThreshold {
			logger.Info("skipping defragmentation due to high utilization")
			continue
		}

		if _, err := client.Defragment(ctx, target.endpoint); err != nil {
			logger.Error("defragmentation failed", zap.Error(err))
			failures++
			continue
		}

		status, err = client.Status(ctx, target.endpoint)
		if err != nil {
			logger.Debug("failed to get target status after defragmentation", zap.Error(err))
		} else {
			// DbSize is the only interesting value after defragmentation
			// considering we aleady added fields for the old DbSize, DbSizeInUse
			// and the calculated utilization prior above.
			//
			// DbSizeInUse will not change much and utilization will be nearly 100%.
			logger = logger.With(zap.Int64("defragmentedDbSize", status.DbSize))
		}

		logger.Info("defragmentation succeeded")
	}

	if failures > 0 {
		return fmt.Errorf("defragmentation failed for %d target(s)", failures)
	}

	return nil
}
