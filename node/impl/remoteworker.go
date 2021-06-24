package impl

import (
	"context"
	"net/http"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-jsonrpc/auth"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/google/uuid"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/client"
	sectorstorage "github.com/filecoin-project/lotus/extern/sector-storage"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
)

type remoteWorker struct {
	api.WorkerAPI
	closer jsonrpc.ClientCloser
}

func (r *remoteWorker) NewSector(ctx context.Context, sector abi.SectorID) error {
	return xerrors.New("unsupported")
}

func connectRemoteWorker(ctx context.Context, fa api.Common, url string) (*remoteWorker, error) {
	token, err := fa.AuthNew(ctx, []auth.Permission{"admin"})
	if err != nil {
		return nil, xerrors.Errorf("creating auth token for remote connection: %w", err)
	}

	headers := http.Header{}
	headers.Add("Authorization", "Bearer "+string(token))

	wapi, closer, err := client.NewWorkerRPC(context.TODO(), url, headers)
	if err != nil {
		return nil, xerrors.Errorf("creating jsonrpc client: %w", err)
	}

	return &remoteWorker{wapi, closer}, nil
}

func (r *remoteWorker) Close() error {
	r.closer()
	return nil
}

//func (r *remoteWorker) AddRange(ctx context.Context, task sealtasks.TaskType, act sealtasks.TaskAction) error {
//	return r.WorkerAPI.AddRange(ctx, task, act)
//}

func (r *remoteWorker) AllowableRange(ctx context.Context, task sealtasks.TaskType) (bool, error) {
	return r.WorkerAPI.AllowableRange(ctx, task)
}

func (r *remoteWorker) GetWorkerInfo(ctx context.Context) sectorstorage.WorkerInfo {
	return r.WorkerAPI.GetWorkerInfo(ctx)
}

func (r *remoteWorker) AddStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error {
	return r.WorkerAPI.AddStore(ctx, ID, taskType)
}

func (r *remoteWorker) DeleteStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error {
	return r.WorkerAPI.DeleteStore(ctx, ID, taskType)
}

func (r *remoteWorker) SetWorkerParams(ctx context.Context, key string, val string) error {
	return r.WorkerAPI.SetWorkerParams(ctx, key, val)
}

func (r *remoteWorker) GetWorkerGroup(ctx context.Context) string {
	return r.WorkerAPI.GetWorkerGroup(ctx)
}

func (r *remoteWorker) GetTaskCount(ctx context.Context) int32 {
	return r.WorkerAPI.GetTaskCount(ctx)
}

func (r *remoteWorker) SetID(ctx context.Context, ID uuid.UUID) error {
	return r.WorkerAPI.SetID(ctx, ID)
}

func (r *remoteWorker) GetID(ctx context.Context) uuid.UUID {
	return r.WorkerAPI.GetID(ctx)
}

func (r *remoteWorker) AddWorkerTask(ctx context.Context, ID uuid.UUID) error {
	return r.WorkerAPI.AddWorkerTask(ctx, ID)
}

func (r *remoteWorker) GetWorkerWait(ctx context.Context, ID uuid.UUID) int {
	return r.WorkerAPI.GetWorkerWait(ctx, ID)
}

func (r *remoteWorker) AddAutoTaskLimit(ctx context.Context, lim map[string]int64) error {
	return r.WorkerAPI.AddAutoTaskLimit(ctx, lim)
}

func (r *remoteWorker) AutoTaskLimit(ctx context.Context) storiface.AutoTaskReturn {
	return r.WorkerAPI.AutoTaskLimit(ctx)
}

var _ sectorstorage.Worker = &remoteWorker{}
