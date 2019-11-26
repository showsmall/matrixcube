package redis

import (
	"github.com/deepfabric/beehive/pb"
	"github.com/deepfabric/beehive/pb/raftcmdpb"
	"github.com/deepfabric/beehive/pb/redispb"
	"github.com/fagongzi/util/format"
	"github.com/fagongzi/util/hack"
	"github.com/fagongzi/util/protoc"
)

// ============================= write methods

func (h *handler) set(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	err := h.getRedisKV(shard).Set(req.Key, args.Args[0])
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(req.Key) + len(args.Args[0]))
	resp.Value = statusResp
	return writtenBytes, int64(writtenBytes), resp
}

func (h *handler) incrBy(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	incrment, err := format.ParseStrInt64(hack.SliceToString(args.Args[0]))
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	value, err := h.getRedisKV(shard).IncrBy(req.Key, incrment)
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(format.Int64ToString(value)))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: value,
	})
	return writtenBytes, 0, resp
}

func (h *handler) incr(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	value, err := h.getRedisKV(shard).IncrBy(req.Key, 1)
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(format.Int64ToString(value)))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: value,
	})
	return writtenBytes, 0, resp
}

func (h *handler) decrby(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	incrment, err := format.ParseStrInt64(hack.SliceToString(args.Args[0]))
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	value, err := h.getRedisKV(shard).DecrBy(req.Key, incrment)
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(format.Int64ToString(value)))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: value,
	})
	return writtenBytes, 0, resp
}

func (h *handler) decr(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	value, err := h.getRedisKV(shard).DecrBy(req.Key, 1)
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(format.Int64ToString(value)))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: value,
	})
	return writtenBytes, 0, resp
}

func (h *handler) getset(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	value, err := h.getRedisKV(shard).GetSet(req.Key, args.Args[0])
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(value))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:       redispb.BulkResp,
		BulkResult: value,
	})
	return writtenBytes, int64(writtenBytes), resp
}

func (h *handler) append(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	n, err := h.getRedisKV(shard).Append(req.Key, args.Args[0])
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(len(args.Args[0]))
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: n,
	})
	return writtenBytes, int64(writtenBytes), resp
}

func (h *handler) setnx(shard uint64, req *raftcmdpb.Request) (uint64, int64, *raftcmdpb.Response) {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	if len(args.Args) != 1 {
		resp.Value = invalidCommandResp
		return 0, 0, resp
	}

	n, err := h.getRedisKV(shard).SetNX(req.Key, args.Args[0])
	if err != nil {
		resp.Value = errorResp(err)
		return 0, 0, resp
	}

	writtenBytes := uint64(0)
	if n > 0 {
		writtenBytes = uint64(len(req.Key) + len(args.Args[0]))
	}
	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:          redispb.IntegerResp,
		IntegerResult: n,
	})
	return writtenBytes, int64(writtenBytes), resp
}

// ============================= read methods

func (h *handler) get(shard uint64, req *raftcmdpb.Request) *raftcmdpb.Response {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	value, err := h.getRedisKV(shard).Get(req.Key)
	if err != nil {
		resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
			Type:        redispb.ErrorResp,
			ErrorResult: []byte(err.Error()),
		})
		return resp
	}

	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		Type:       redispb.BulkResp,
		BulkResult: value,
	})
	return resp
}

func (h *handler) strlen(shard uint64, req *raftcmdpb.Request) *raftcmdpb.Response {
	resp := pb.AcquireResponse()
	args := &redispb.RedisArgs{}
	protoc.MustUnmarshal(args, req.Cmd)

	value, err := h.getRedisKV(shard).StrLen(req.Key)
	if err != nil {
		resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
			Type:        redispb.ErrorResp,
			ErrorResult: []byte(err.Error()),
		})
		return resp
	}

	resp.Value = protoc.MustMarshal(&redispb.RedisResponse{
		IntegerResult: value,
	})
	return resp
}