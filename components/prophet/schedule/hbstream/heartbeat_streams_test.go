package hbstream

import (
	"context"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/matrixorigin/matrixcube/components/prophet/config"
	"github.com/matrixorigin/matrixcube/components/prophet/mock/mockcluster"
	"github.com/matrixorigin/matrixcube/components/prophet/mock/mockhbstream"
	"github.com/matrixorigin/matrixcube/components/prophet/pb/metapb"
	"github.com/matrixorigin/matrixcube/components/prophet/pb/rpcpb"
	"github.com/matrixorigin/matrixcube/components/prophet/testutil"
)

func TestActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cluster := mockcluster.NewCluster(config.NewTestOptions())
	cluster.AddResourceContainer(1, 1)
	cluster.AddResourceContainer(2, 0)
	cluster.AddLeaderResource(1, 1)
	resource := cluster.GetResource(1)
	msg := &rpcpb.ResourceHeartbeatRsp{
		ChangePeer: &rpcpb.ChangePeer{
			Peer:       metapb.Peer{ID: 2, ContainerID: 2},
			ChangeType: metapb.ChangePeerType_AddLearnerNode,
		},
	}

	hbs := NewTestHeartbeatStreams(ctx, cluster.ID, cluster, true)
	stream1, stream2 := mockhbstream.NewHeartbeatStream(), mockhbstream.NewHeartbeatStream()

	// Active stream is stream1.
	hbs.BindStream(1, stream1)
	testutil.WaitUntil(t, func(t *testing.T) bool {
		hbs.SendMsg(resource, proto.Clone(msg).(*rpcpb.ResourceHeartbeatRsp))
		return stream1.Recv() != nil && stream2.Recv() == nil
	})
	// Rebind to stream2.
	hbs.BindStream(1, stream2)
	testutil.WaitUntil(t, func(t *testing.T) bool {
		hbs.SendMsg(resource, proto.Clone(msg).(*rpcpb.ResourceHeartbeatRsp))
		return stream1.Recv() == nil && stream2.Recv() != nil
	})

	// Switch back to 1 again.
	hbs.BindStream(1, stream1)
	testutil.WaitUntil(t, func(t *testing.T) bool {
		hbs.SendMsg(resource, proto.Clone(msg).(*rpcpb.ResourceHeartbeatRsp))
		return stream1.Recv() != nil && stream2.Recv() == nil
	})
}