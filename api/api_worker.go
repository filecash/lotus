package api

import (
	"context"
	"io"

	"github.com/ipfs/go-cid"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/stores"
	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"
	"github.com/filecoin-project/specs-storage/storage"

	"github.com/filecoin-project/lotus/build"

	sectorstorage "github.com/filecoin-project/lotus/extern/sector-storage"
)

type WorkerAPI interface {
	Version(context.Context) (build.Version, error)
	// TODO: Info() (name, ...) ?

	TaskTypes(context.Context) (map[sealtasks.TaskType]struct{}, error) // TaskType -> Weight
	Paths(context.Context) ([]stores.StoragePath, error)
	Info(context.Context) (storiface.WorkerInfo, error)

	AddPiece(ctx context.Context, sector abi.SectorID, pieceSizes []abi.UnpaddedPieceSize, newPieceSize abi.UnpaddedPieceSize, pieceData storage.Data) (abi.PieceInfo, error)

	storage.Sealer

	MoveStorage(ctx context.Context, sector abi.SectorID, types stores.SectorFileType) error

	UnsealPiece(context.Context, abi.SectorID, storiface.UnpaddedByteIndex, abi.UnpaddedPieceSize, abi.SealRandomness, cid.Cid) error
	ReadPiece(context.Context, io.Writer, abi.SectorID, storiface.UnpaddedByteIndex, abi.UnpaddedPieceSize) (bool, error)

	StorageAddLocal(ctx context.Context, path string) error

	Fetch(context.Context, abi.SectorID, stores.SectorFileType, stores.PathType, stores.AcquireMode) error

	Closing(context.Context) (<-chan struct{}, error)

	AllowableRange(ctx context.Context, task sealtasks.TaskType) (bool, error)

	AddRange(ctx context.Context, task sealtasks.TaskType, addType int) error

	GetWorkerInfo(ctx context.Context) sectorstorage.WorkerInfo

	AddStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error

	DeleteStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error

	SetWorkerParams(ctx context.Context, key string, val string) error

	GetWorkerGroup(ctx context.Context) string

	GetTaskCount(ctx context.Context) int32

	SetID(ctx context.Context, ID uint64) error

	GetID(ctx context.Context) uint64

	AddWorkerTask(ctx context.Context, ID uint64) error
        GetWorkerWait(ctx context.Context, ID uint64) int
}
