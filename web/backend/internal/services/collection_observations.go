package services

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"server-monitor/internal/models"
)

type collectionTiming struct {
	count, failed  int
	total, maximum time.Duration
}
type collectionTimings struct {
	mu     sync.Mutex
	since  time.Time
	stages map[string]collectionTiming
}

func newCollectionTimings() *collectionTimings {
	return &collectionTimings{since: time.Now(), stages: make(map[string]collectionTiming)}
}

// Aggregate by OS/stage, without host names, addresses, commands or credentials.
// Emit one structured summary per minute, not one log per sample.
func (o *collectionTimings) record(os, stage string, duration time.Duration, failed bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	key := os + "/" + stage
	row := o.stages[key]
	row.count++
	row.total += duration
	if failed {
		row.failed++
	}
	if duration > row.maximum {
		row.maximum = duration
	}
	o.stages[key] = row
	if time.Since(o.since) < time.Minute {
		o.mu.Unlock()
		return
	}
	rows := o.stages
	elapsed := time.Since(o.since)
	o.stages = make(map[string]collectionTiming)
	o.since = time.Now()
	o.mu.Unlock()
	type timingSummary struct {
		Count  int   `json:"count"`
		Failed int   `json:"failed"`
		MeanMS int64 `json:"mean_ms"`
		MaxMS  int64 `json:"max_ms"`
	}
	summary := make(map[string]timingSummary, len(rows))
	for name, item := range rows {
		summary[name] = timingSummary{item.count, item.failed, (item.total / time.Duration(item.count)).Milliseconds(), item.maximum.Milliseconds()}
	}
	encoded, _ := json.Marshal(summary)
	log.Printf("collector timing window=%s stages=%s", elapsed.Round(time.Second), encoded)
}

// Metadata has its own connection and deadline. A wedged Docker daemon cannot
// delay basic CPU/memory samples or close the collector's shared transport.
func (c *Collector) refreshDockerInfo(server *models.Server, fingerprint [32]byte) {
	if c.db == nil || c.stopContext.Err() != nil {
		return
	}
	c.mu.Lock()
	if time.Since(c.dockerChecked[server.ID]) < time.Minute {
		c.mu.Unlock()
		return
	}
	select {
	case c.metadataSlots <- struct{}{}:
	default:
		c.mu.Unlock()
		return
	}
	c.dockerChecked[server.ID] = time.Now()
	c.mu.Unlock()
	go func() {
		defer func() { <-c.metadataSlots }()
		started := time.Now()
		failed := true
		defer func() { c.observations.record(server.ServerType, "docker_metadata", time.Since(started), failed) }()
		ctx, cancel := context.WithTimeout(c.stopContext, 15*time.Second)
		defer cancel()
		client, _, err := DialServer(ctx, server)
		if err != nil {
			return
		}
		defer client.Close()
		probe := ProbeDockerContext(ctx, client, server.ServerType)
		if !probe.Known || ctx.Err() != nil {
			return
		}
		// Reject a result collected using connection settings already replaced.
		c.clientMu.Lock()
		current := c.clients[server.ID]
		valid := current != nil && current.fingerprint == fingerprint
		c.clientMu.Unlock()
		if !valid {
			return
		}
		presence := dockerAbsent
		if probe.Installed {
			presence = dockerInstalled
		}
		c.syncDockerInfo(server.ID, presence, probe.Version)
		failed = false
	}()
}
