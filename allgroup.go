package consumer

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/service/kinesis"
	"github.com/aws/aws-sdk-go/service/kinesis/kinesisiface"
)

func NewAllGroup(ksis kinesisiface.KinesisAPI, ck Checkpoint, streamName string, logger Logger) *AllGroup {
	return &AllGroup{
		ksis:       ksis,
		shards:     make(map[string]*kinesis.Shard),
		streamName: streamName,
		logger:     logger,
		checkpoint: ck,
	}
}

// AllGroup caches a local list of the shards we are already processing
// and routinely polls the stream looking for new shards to process
type AllGroup struct {
	ksis       kinesisiface.KinesisAPI
	streamName string
	logger     Logger
	checkpoint Checkpoint

	shardMu sync.Mutex
	shards  map[string]*kinesis.Shard
}

// start is a blocking operation which will loop and attempt to find new
// shards on a regular cadence.
func (g *AllGroup) Start(ctx context.Context) chan *kinesis.Shard {
	var (
		shardc = make(chan *kinesis.Shard, 1)
		ticker = time.NewTicker(30 * time.Second)
	)
	g.findNewShards(shardc)

	// Note: while ticker is a rather naive approach to this problem,
	// it actually simplies a few things. i.e. If we miss a new shard while
	// AWS is resharding we'll pick it up max 30 seconds later.

	// It might be worth refactoring this flow to allow the consumer to
	// to notify the broker when a shard is closed. However, shards don't
	// necessarily close at the same time, so we could potentially get a
	// thundering heard of notifications from the consumer.

	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				g.findNewShards(shardc)
			}
		}
	}()

	return shardc
}

func (g *AllGroup) GetCheckpoint(streamName, shardID string) (string, error) {
	return g.checkpoint.Get(streamName, shardID)
}

func (g *AllGroup) SetCheckpoint(streamName, shardID, sequenceNumber string) error {
	return g.checkpoint.Set(streamName, shardID, sequenceNumber)
}

// findNewShards pulls the list of shards from the Kinesis API
// and uses a local cache to determine if we are already processing
// a particular shard.
func (g *AllGroup) findNewShards(shardc chan *kinesis.Shard) {
	g.shardMu.Lock()
	defer g.shardMu.Unlock()

	g.logger.Log("[GROUP]", "fetching shards")

	shards, err := listShards(g.ksis, g.streamName)
	if err != nil {
		g.logger.Log("[GROUP]", err)
		return
	}

	for _, shard := range shards {
		if _, ok := g.shards[*shard.ShardId]; ok {
			continue
		}
		g.shards[*shard.ShardId] = shard
		shardc <- shard
	}
}