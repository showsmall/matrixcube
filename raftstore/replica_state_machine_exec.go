// Copyright 2020 MatrixOrigin.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package raftstore

import (
	"bytes"
	"math"
	"sort"

	"github.com/cockroachdb/errors"
	"github.com/fagongzi/util/protoc"
	"github.com/matrixorigin/matrixcube/components/log"
	"github.com/matrixorigin/matrixcube/components/prophet/pb/metapb"
	"github.com/matrixorigin/matrixcube/pb/meta"
	"github.com/matrixorigin/matrixcube/pb/rpc"
	"github.com/matrixorigin/matrixcube/storage"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.uber.org/zap"
)

var (
	ErrNotLearnerReplica = errors.New("not learner")
	ErrReplicaNotFound   = errors.New("replica not found")
	ErrReplicaDuplicated = errors.New("replica duplicated")
)

func (d *stateMachine) execAdminRequest(ctx *applyContext) (rpc.ResponseBatch, error) {
	switch ctx.req.GetAdminCmdType() {
	case rpc.AdminCmdType_ConfigChange:
		return d.doExecConfigChange(ctx)
	case rpc.AdminCmdType_BatchSplit:
		return d.doExecSplit(ctx)
	case rpc.AdminCmdType_UpdateMetadata:
		return d.doUpdateMetadata(ctx)
	case rpc.AdminCmdType_CompactLog:
		return d.doExecCompactLog(ctx)
	case rpc.AdminCmdType_UpdateLabels:
		return d.doUpdateLabels(ctx)
	}

	return rpc.ResponseBatch{}, nil
}

func (d *stateMachine) doExecCompactLog(ctx *applyContext) (rpc.ResponseBatch, error) {
	ctx.metrics.admin.compact++

	req := ctx.req.GetCompactLogRequest()
	compactIndex := req.CompactIndex
	firstIndex := d.getFirstIndex()
	if compactIndex <= firstIndex {
		return rpc.ResponseBatch{}, nil
	}

	compactIndex, err := d.adjustCompactionIndex(compactIndex)
	if err != nil {
		return rpc.ResponseBatch{}, err
	}

	d.setFirstIndex(compactIndex + 1)
	resp := newAdminResponseBatch(rpc.AdminCmdType_CompactLog, &rpc.CompactLogResponse{})
	ctx.adminResult = &adminResult{
		adminType: rpc.AdminCmdType_CompactLog,
		compactionResult: compactionResult{
			index: compactIndex,
		},
	}
	return resp, nil
}

func (d *stateMachine) adjustCompactionIndex(index uint64) (uint64, error) {
	// take current persistent log index into consideration, never compact those
	// raft log entries that might be required after reboot.
	persistentLogIndex, err := d.dataStorage.GetPersistentLogIndex(d.shardID)
	if err != nil {
		d.logger.Error("failed to get persistent log index",
			zap.Error(err))
		return 0, err
	}
	if index > persistentLogIndex {
		d.logger.Info("adjusted compact log index",
			zap.Uint64("persistent-index", persistentLogIndex),
			zap.Uint64("compact-index", index))
		index = persistentLogIndex
	}
	return index, nil
}

func (d *stateMachine) doExecConfigChange(ctx *applyContext) (rpc.ResponseBatch, error) {
	req := ctx.req.GetConfigChangeRequest()
	replica := req.Replica
	current := d.getShard()

	d.logger.Info("begin to apply change replica",
		zap.Uint64("index", ctx.index),
		log.ShardField("current", current),
		log.ConfigChangeField("request", &req))

	res := Shard{}
	protoc.MustUnmarshal(&res, protoc.MustMarshal(&current))
	res.Epoch.ConfVer++
	p := findReplica(res, replica.ContainerID)
	switch req.ChangeType {
	case metapb.ConfigChangeType_AddNode:
		exists := false
		if p != nil {
			exists = true
			if p.ID == replica.ID {
				if p.Role != metapb.ReplicaRole_Learner {
					err := errors.Wrapf(ErrReplicaDuplicated,
						"shardID %d, replicaID %d, role %v", res.ID, p.ID, p.Role)
					return rpc.ResponseBatch{}, err
				}
			} else {
				err := errors.Wrapf(ErrReplicaDuplicated,
					"shardID %d, replicaID %d found on container %d", res.ID, p.ID, replica.ContainerID)
				return rpc.ResponseBatch{}, err
			}
			p.Role = metapb.ReplicaRole_Voter
			d.logger.Info("learner promoted to voter",
				log.ReplicaField("replica", *p),
				log.StoreIDField(replica.ContainerID))
		}
		if !exists {
			replica.Role = metapb.ReplicaRole_Voter
			res.Replicas = append(res.Replicas, replica)
		}
	case metapb.ConfigChangeType_RemoveNode:
		if p != nil {
			if p.ID != replica.ID {
				err := errors.Wrapf(ErrReplicaNotFound,
					"shardID %d, replicaID %d found on container %d", res.ID, p.ID, replica.ContainerID)
				return rpc.ResponseBatch{}, err
			} else {
				removeReplica(&res, replica.ContainerID)
			}

			if d.replica.ID == replica.ID {
				// Remove ourself, will destroy all shard data later.
				d.setRemoved()
				d.logger.Info("replica remoted itself",
					log.ReplicaField("replica", *p),
					log.StoreIDField(replica.ContainerID))
			}
		} else {
			err := errors.Wrapf(ErrReplicaNotFound,
				"shardID %d, replicaID %d found on container %d",
				res.ID,
				replica.ID, replica.ContainerID)
			return rpc.ResponseBatch{}, err
		}
	case metapb.ConfigChangeType_AddLearnerNode:
		if p != nil {
			err := errors.Wrapf(ErrReplicaDuplicated,
				"shardID %d, replicaID %d role %v already exist on store %d",
				res.ID, p.ID, p.Role, replica.ContainerID)
			return rpc.ResponseBatch{}, err
		}
		replica.Role = metapb.ReplicaRole_Learner
		res.Replicas = append(res.Replicas, replica)
	}
	state := meta.ReplicaState_Normal
	if d.isRemoved() {
		state = meta.ReplicaState_Tombstone
	}
	d.updateShard(res)
	if err := d.saveShardMetedata(ctx.index, ctx.term, res, state); err != nil {
		d.logger.Fatal("failed to save metadata",
			zap.Error(err))
	}

	d.logger.Info("apply change replica completed",
		log.ShardField("metadata", res),
		zap.String("state", state.String()))

	resp := newAdminResponseBatch(rpc.AdminCmdType_ConfigChange, &rpc.ConfigChangeResponse{
		Shard: res,
	})
	ctx.adminResult = &adminResult{
		adminType: rpc.AdminCmdType_ConfigChange,
		configChangeResult: configChangeResult{
			index:   ctx.index,
			changes: []rpc.ConfigChangeRequest{req},
			shard:   res,
		},
	}
	return resp, nil
}

func (d *stateMachine) doExecSplit(ctx *applyContext) (rpc.ResponseBatch, error) {
	ctx.metrics.admin.split++
	splitReqs := ctx.req.GetBatchSplitRequest()

	if len(splitReqs.Requests) == 0 {
		d.logger.Fatal("missing splits request")
	}

	current := d.getShard().Clone()
	if !bytes.Equal(splitReqs.Requests[0].Start, current.Start) ||
		!bytes.Equal(splitReqs.Requests[len(splitReqs.Requests)-1].End, current.End) {
		d.logger.Fatal("invalid splits keys",
			log.HexField("actual-start", splitReqs.Requests[0].Start),
			log.HexField("shard-start", current.Start),
			log.HexField("actual-end", splitReqs.Requests[len(splitReqs.Requests)-1].End),
			log.HexField("shard-end", current.End))
	}

	newShardsCount := len(splitReqs.Requests)
	var newShards []Shard
	current.Epoch.Version += uint64(newShardsCount)
	expectStart := current.Start
	last := len(splitReqs.Requests) - 1
	for idx, req := range splitReqs.Requests {
		if checkKeyInShard(req.Start, current) != nil ||
			(idx != last && checkKeyInShard(req.End, current) != nil) {
			d.logger.Fatal("invalid split reuqest range",
				log.HexField("split-start", req.Start),
				log.HexField("split-end", req.End),
				log.HexField("expect-start", current.Start),
				log.HexField("expect-end", current.End))
		}

		if !bytes.Equal(req.Start, expectStart) {
			d.logger.Fatal("invalid split reuqest start key",
				log.HexField("split-start", req.Start),
				log.HexField("expect-start", expectStart))
		}
		expectStart = req.End

		newShard := Shard{}
		newShard.ID = req.NewShardID
		newShard.Group = current.Group
		newShard.Unique = current.Unique
		newShard.RuleGroups = current.RuleGroups
		newShard.DisableSplit = current.DisableSplit
		newShard.Epoch = current.Epoch
		newShard.Start = req.Start
		newShard.End = req.End
		newShard.Replicas = req.NewReplicas
		newShards = append(newShards, newShard)
		ctx.metrics.admin.splitSucceed++
	}

	// We only create shard init raft log in logdb, create new shards metadata in memory,
	// and update atomically with the old metadata later.
	replicaFactory := d.replicaCreatorFactory()
	replicaFactory.withReason("splited").
		withLogdbContext(d.wc).
		create(newShards)

	// We can't destroy Old Shard directly, but mark it as being destroyed. Because at this time, we are not
	// sure that all Replcias have received the Log of this split, and if we destroy it directly,  then the
	// majority of the entire Raft-Group will destroy itself, causing the minority never to receive this Log.
	// The real destruction is performed in a subsequent asynchronous task.
	current.State = metapb.ResourceState_Destroying
	old := meta.ShardMetadata{
		ShardID:  current.ID,
		LogIndex: ctx.index,
		Metadata: meta.ShardLocalState{
			State:      meta.ReplicaState_Normal,
			Shard:      current,
			RemoveData: false,
		},
	}
	err := d.dataStorage.Split(old, replicaFactory.getShardsMetadata(), splitReqs.Context)
	if err != nil {
		if err == storage.ErrAborted {
			return rpc.ResponseBatch{}, nil
		}
		d.logger.Fatal("failed to split on data storage",
			zap.Error(err))
	}

	d.setSplited()
	d.updateShard(current)
	resp := newAdminResponseBatch(rpc.AdminCmdType_BatchSplit, &rpc.BatchSplitResponse{
		Shards: newShards,
	})
	ctx.adminResult = &adminResult{
		adminType: rpc.AdminCmdType_BatchSplit,
		splitResult: splitResult{
			newShards: newShards,
		},
	}
	return resp, nil
}

func (d *stateMachine) doUpdateLabels(ctx *applyContext) (rpc.ResponseBatch, error) {
	updateReq := ctx.req.GetUpdateLabelsRequest()
	current := d.getShard()

	switch updateReq.Policy {
	case rpc.UpdatePolicy_Add:
		var newLabels []metapb.Pair
		for _, oldLabel := range current.Labels {
			remove := false
			for _, label := range updateReq.Labels {
				if label.Key == oldLabel.Key {
					remove = true
				}
			}

			if !remove {
				newLabels = append(newLabels, oldLabel)
			}
		}
		current.Labels = append(newLabels, updateReq.Labels...)
	case rpc.UpdatePolicy_Remove:
		var newLabels []metapb.Pair
		for _, oldLabel := range current.Labels {
			remove := false
			for _, label := range updateReq.Labels {
				if label.Key == oldLabel.Key {
					remove = true
				}
			}

			if !remove {
				newLabels = append(newLabels, oldLabel)
			}
		}
		current.Labels = newLabels
	case rpc.UpdatePolicy_Reset:
		current.Labels = updateReq.Labels
	case rpc.UpdatePolicy_Clear:
		current.Labels = nil
	}

	err := d.dataStorage.SaveShardMetadata([]meta.ShardMetadata{
		{
			ShardID:  d.shardID,
			LogIndex: ctx.index,
			Metadata: meta.ShardLocalState{
				Shard: current,
				State: meta.ReplicaState_Normal,
			},
		},
	})
	if err != nil {
		d.logger.Fatal("failed to update labels",
			zap.Error(err))
	}

	sort.Slice(current.Labels, func(i, j int) bool {
		return current.Labels[i].Key < current.Labels[j].Key
	})
	d.updateShard(current)

	d.logger.Info("shard labels updated",
		log.ShardField("new-shard", current))

	resp := newAdminResponseBatch(rpc.AdminCmdType_UpdateLabels, &rpc.UpdateLabelsResponse{})
	ctx.adminResult = &adminResult{
		adminType: rpc.AdminCmdType_UpdateLabels,
	}
	return resp, nil
}

func (d *stateMachine) doUpdateMetadata(ctx *applyContext) (rpc.ResponseBatch, error) {
	ctx.metrics.admin.updateMetadata++
	updateReq := ctx.req.GetUpdateMetadataRequest()

	current := d.getShard()
	if isEpochStale(current.Epoch, updateReq.Metadata.Shard.Epoch) {
		d.logger.Fatal("failed to update metadata",
			log.EpochField("current", current.Epoch),
			log.ShardField("new-shard", updateReq.Metadata.Shard))
	}

	err := d.dataStorage.SaveShardMetadata([]meta.ShardMetadata{
		{
			ShardID:  d.shardID,
			LogIndex: ctx.index,
			Metadata: updateReq.Metadata,
		},
	})
	if err != nil {
		d.logger.Fatal("failed to update metadata",
			log.EpochField("current", current.Epoch),
			log.ShardField("new-shard", updateReq.Metadata.Shard),
			zap.Error(err))
	}

	d.updateShard(updateReq.Metadata.Shard)

	d.logger.Info("shard metadata updated",
		zap.String("replica-state", updateReq.Metadata.State.String()),
		log.ShardField("new-shard", updateReq.Metadata.Shard),
	)

	var cc []raftpb.ConfChangeV2
	sort.Slice(updateReq.Metadata.Shard.Replicas, func(i, j int) bool {
		return updateReq.Metadata.Shard.Replicas[i].ID < updateReq.Metadata.Shard.Replicas[j].ID
	})
	for _, r := range updateReq.Metadata.Shard.Replicas {
		cc = append(cc, raftpb.ConfChangeV2{
			Changes: []raftpb.ConfChangeSingle{
				{
					Type:   raftpb.ConfChangeAddNode,
					NodeID: r.ID,
				},
			},
		})
	}

	resp := newAdminResponseBatch(rpc.AdminCmdType_UpdateMetadata, &rpc.UpdateMetadataResponse{})
	ctx.adminResult = &adminResult{
		adminType: rpc.AdminCmdType_UpdateMetadata,
		updateMetadataResult: updateMetadataResult{
			changes: cc,
		},
	}
	return resp, nil
}

func (d *stateMachine) execWriteRequest(ctx *applyContext) rpc.ResponseBatch {
	d.writeCtx.initialize(d.getShard(), ctx.index, ctx.req)
	for _, req := range ctx.req.Requests {
		if ce := d.logger.Check(zap.DebugLevel, "begin to execute write"); ce != nil {
			ce.Write(log.HexField("id", req.ID),
				log.ShardIDField(d.shardID),
				log.ReplicaIDField(d.replica.ID),
				log.IndexField(ctx.index))
		}
	}
	if err := d.dataStorage.Write(d.writeCtx); err != nil {
		d.logger.Fatal("failed to exec read cmd",
			zap.Error(err))
	}
	for _, req := range ctx.req.Requests {
		if ce := d.logger.Check(zap.DebugLevel, "write completed"); ce != nil {
			ce.Write(log.HexField("id", req.ID),
				log.ShardIDField(d.shardID),
				log.ReplicaIDField(d.replica.ID),
				log.IndexField(ctx.index))
		}
	}

	resp := rpc.ResponseBatch{}
	for _, v := range d.writeCtx.responses {
		ctx.metrics.writtenKeys++
		r := rpc.Response{Value: v}
		resp.Responses = append(resp.Responses, r)
	}
	d.updateWriteMetrics()
	return resp
}

func (d *stateMachine) updateWriteMetrics() {
	d.applyCtx.metrics.writtenBytes += d.writeCtx.writtenBytes
	if d.writeCtx.diffBytes < 0 {
		v := uint64(math.Abs(float64(d.writeCtx.diffBytes)))
		if v >= d.applyCtx.metrics.approximateDiffHint {
			d.applyCtx.metrics.approximateDiffHint = 0
		} else {
			d.applyCtx.metrics.approximateDiffHint -= v
		}
	} else {
		d.applyCtx.metrics.approximateDiffHint += uint64(d.writeCtx.diffBytes)
	}
}

func (d *stateMachine) saveShardMetedata(index uint64, term uint64,
	shard Shard, state meta.ReplicaState) error {
	return d.dataStorage.SaveShardMetadata([]meta.ShardMetadata{{
		ShardID:  shard.ID,
		LogIndex: index,
		Metadata: meta.ShardLocalState{
			State: state,
			Shard: shard,
		},
	}})
}
