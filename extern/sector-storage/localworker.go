package sectorstorage

import (
	"context"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/elastic/go-sysinfo"
	"github.com/hashicorp/go-multierror"
	"github.com/ipfs/go-cid"
	"golang.org/x/xerrors"

	ffi "github.com/filecoin-project/filecoin-ffi"
	"github.com/filecoin-project/go-state-types/abi"
	storage2 "github.com/filecoin-project/specs-storage/storage"

	"github.com/filecoin-project/lotus/extern/sector-storage/ffiwrapper"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/stores"
	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"
)

var pathTypes = []stores.SectorFileType{stores.FTUnsealed, stores.FTSealed, stores.FTCache}

type WorkerConfig struct {
	SealProof abi.RegisteredSealProof
	TaskTypes []sealtasks.TaskType
	NoSwap    bool

	PreCommit1Max int64
	PreCommit2Max int64
	CommitMax     int64
	Group         string
}

type LocalWorker struct {
	scfg       *ffiwrapper.Config
	storage    stores.Store
	localStore *stores.Local
	sindex     stores.SectorIndex
	taskCount  int32
	ID         uint64
	noSwap     bool

	acceptTasks map[sealtasks.TaskType]struct{}

	preCommit1Max int64
	preCommit1Now int64
	preCommit2Max int64
	preCommit2Now int64
	commitMax     int64
	commitNow     int64
	group         string
	storeList     taskList
}

type taskList struct {
	lk   sync.Mutex
	list map[abi.SectorID]string
}

type WorkerInfo struct {
	PreCommit1Max int64
	PreCommit1Now int64
	PreCommit2Max int64
	PreCommit2Now int64
	CommitMax     int64
	CommitNow     int64
	Group         string
	StoreList     map[string]string
	AcceptTasks   []string
}

func NewLocalWorker(wcfg WorkerConfig, store stores.Store, local *stores.Local, sindex stores.SectorIndex) *LocalWorker {
	acceptTasks := map[sealtasks.TaskType]struct{}{}
	for _, taskType := range wcfg.TaskTypes {
		acceptTasks[taskType] = struct{}{}
	}

	if wcfg.Group == "" {
		wcfg.Group = "all"
	}

	return &LocalWorker{
		scfg: &ffiwrapper.Config{
			SealProofType: wcfg.SealProof,
		},
		storage:    store,
		localStore: local,
		sindex:     sindex,
		taskCount:  0,
		ID:         ^uint64(0),
		noSwap:     wcfg.NoSwap,

		acceptTasks: acceptTasks,

		preCommit1Max: wcfg.PreCommit1Max,
		preCommit2Max: wcfg.PreCommit2Max,
		commitMax:     wcfg.CommitMax,
		group:         wcfg.Group,
		storeList: taskList{
			list: make(map[abi.SectorID]string),
		},
	}
}

type localWorkerPathProvider struct {
	w  *LocalWorker
	op stores.AcquireMode
}

func (l *localWorkerPathProvider) AcquireSector(ctx context.Context, sector abi.SectorID, existing stores.SectorFileType, allocate stores.SectorFileType, sealing stores.PathType) (stores.SectorPaths, func(), error) {

	paths, storageIDs, err := l.w.storage.AcquireSector(ctx, sector, l.w.scfg.SealProofType, existing, allocate, sealing, l.op)
	if err != nil {
		return stores.SectorPaths{}, nil, err
	}

	releaseStorage, err := l.w.localStore.Reserve(ctx, sector, l.w.scfg.SealProofType, allocate, storageIDs, stores.FSOverheadSeal)
	if err != nil {
		return stores.SectorPaths{}, nil, xerrors.Errorf("reserving storage space: %w", err)
	}

	log.Debugf("acquired sector %d (e:%d; a:%d): %v", sector, existing, allocate, paths)

	return paths, func() {
		releaseStorage()

		for _, fileType := range pathTypes {
			if fileType&allocate == 0 {
				continue
			}

			sid := stores.PathByType(storageIDs, fileType)

			if err := l.w.sindex.StorageDeclareSector(ctx, stores.ID(sid), sector, fileType, l.op == stores.AcquireMove); err != nil {
				log.Errorf("declare sector error: %+v", err)
			}
		}
	}, nil
}

func (l *LocalWorker) sb() (ffiwrapper.Storage, error) {
	return ffiwrapper.New(&localWorkerPathProvider{w: l}, l.scfg)
}

func (l *LocalWorker) NewSector(ctx context.Context, sector abi.SectorID) error {
	sb, err := l.sb()
	if err != nil {
		return err
	}

	return sb.NewSector(ctx, sector)
}

func (l *LocalWorker) AddPiece(ctx context.Context, sector abi.SectorID, epcs []abi.UnpaddedPieceSize, sz abi.UnpaddedPieceSize, r io.Reader) (abi.PieceInfo, error) {
	sb, err := l.sb()
	if err != nil {
		return abi.PieceInfo{}, err
	}
	atomic.AddInt32(&l.taskCount, 1)
	defer func(){
		atomic.AddInt32(&l.taskCount, -1)
	}()

	return sb.AddPiece(ctx, sector, epcs, sz, r)
}

func (l *LocalWorker) Fetch(ctx context.Context, sector abi.SectorID, fileType stores.SectorFileType, ptype stores.PathType, am stores.AcquireMode) error {
	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()
	_, done, err := (&localWorkerPathProvider{w: l, op: am}).AcquireSector(ctx, sector, fileType, stores.FTNone, ptype)
	if err != nil {
		return err
	}
	done()
	return nil
}

func (l *LocalWorker) SealPreCommit1(ctx context.Context, sector abi.SectorID, ticket abi.SealRandomness, pieces []abi.PieceInfo) (out storage2.PreCommit1Out, err error) {
	{
		// cleanup previous failed attempts if they exist
		if err := l.storage.Remove(ctx, sector, stores.FTSealed, true); err != nil {
			return nil, xerrors.Errorf("cleaning up sealed data: %w", err)
		}

		if err := l.storage.Remove(ctx, sector, stores.FTCache, true); err != nil {
			return nil, xerrors.Errorf("cleaning up cache data: %w", err)
		}
	}

	sb, err := l.sb()
	if err != nil {
		return nil, err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	return sb.SealPreCommit1(ctx, sector, ticket, pieces)
}

func (l *LocalWorker) SealPreCommit2(ctx context.Context, sector abi.SectorID, phase1Out storage2.PreCommit1Out) (cids storage2.SectorCids, err error) {
	sb, err := l.sb()
	if err != nil {
		return storage2.SectorCids{}, err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	return sb.SealPreCommit2(ctx, sector, phase1Out)
}

func (l *LocalWorker) SealCommit1(ctx context.Context, sector abi.SectorID, ticket abi.SealRandomness, seed abi.InteractiveSealRandomness, pieces []abi.PieceInfo, cids storage2.SectorCids) (output storage2.Commit1Out, err error) {
	sb, err := l.sb()
	if err != nil {
		return nil, err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	return sb.SealCommit1(ctx, sector, ticket, seed, pieces, cids)
}

func (l *LocalWorker) SealCommit2(ctx context.Context, sector abi.SectorID, phase1Out storage2.Commit1Out) (proof storage2.Proof, err error) {
	sb, err := l.sb()
	if err != nil {
		return nil, err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	return sb.SealCommit2(ctx, sector, phase1Out)
}

func (l *LocalWorker) FinalizeSector(ctx context.Context, sector abi.SectorID, keepUnsealed []storage2.Range) error {
	sb, err := l.sb()
	if err != nil {
		return err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	if err := sb.FinalizeSector(ctx, sector, keepUnsealed); err != nil {
		return xerrors.Errorf("finalizing sector: %w", err)
	}

	if len(keepUnsealed) == 0 {
		if err := l.storage.Remove(ctx, sector, stores.FTUnsealed, true); err != nil {
			return xerrors.Errorf("removing unsealed data: %w", err)
		}
	}

	return nil
}

func (l *LocalWorker) ReleaseUnsealed(ctx context.Context, sector abi.SectorID, safeToFree []storage2.Range) error {
	return xerrors.Errorf("implement me")
}

func (l *LocalWorker) Remove(ctx context.Context, sector abi.SectorID) error {
	var err error

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	if rerr := l.storage.Remove(ctx, sector, stores.FTSealed, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (sealed): %w", rerr))
	}
	if rerr := l.storage.Remove(ctx, sector, stores.FTCache, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (cache): %w", rerr))
	}
	if rerr := l.storage.Remove(ctx, sector, stores.FTUnsealed, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (unsealed): %w", rerr))
	}

	return err
}

func (l *LocalWorker) MoveStorage(ctx context.Context, sector abi.SectorID, types stores.SectorFileType) error {
	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()
	if err := l.storage.MoveStorage(ctx, sector, l.scfg.SealProofType, types); err != nil {
		return xerrors.Errorf("moving sealed data to storage: %w", err)
	}

	return nil
}

func (l *LocalWorker) UnsealPiece(ctx context.Context, sector abi.SectorID, index storiface.UnpaddedByteIndex, size abi.UnpaddedPieceSize, randomness abi.SealRandomness, cid cid.Cid) error {
	sb, err := l.sb()
	if err != nil {
		return err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	if err := sb.UnsealPiece(ctx, sector, index, size, randomness, cid); err != nil {
		return xerrors.Errorf("unsealing sector: %w", err)
	}

	if err := l.storage.RemoveCopies(ctx, sector, stores.FTSealed); err != nil {
		return xerrors.Errorf("removing source data: %w", err)
	}

	if err := l.storage.RemoveCopies(ctx, sector, stores.FTCache); err != nil {
		return xerrors.Errorf("removing source data: %w", err)
	}

	return nil
}

func (l *LocalWorker) ReadPiece(ctx context.Context, writer io.Writer, sector abi.SectorID, index storiface.UnpaddedByteIndex, size abi.UnpaddedPieceSize) (bool, error) {
	sb, err := l.sb()
	if err != nil {
		return false, err
	}

	atomic.AddInt32(&l.taskCount, 1)
        defer func(){
                atomic.AddInt32(&l.taskCount, -1)
        }()

	return sb.ReadPiece(ctx, writer, sector, index, size)
}

func (l *LocalWorker) TaskTypes(context.Context) (map[sealtasks.TaskType]struct{}, error) {
	return l.acceptTasks, nil
}

func (l *LocalWorker) Paths(ctx context.Context) ([]stores.StoragePath, error) {
	return l.localStore.Local(ctx)
}

func (l *LocalWorker) Info(context.Context) (storiface.WorkerInfo, error) {
	hostname, err := os.Hostname() // TODO: allow overriding from config
	if err != nil {
		panic(err)
	}

	gpus, err := ffi.GetGPUDevices()
	if err != nil {
		log.Errorf("getting gpu devices failed: %+v", err)
	}

	h, err := sysinfo.Host()
	if err != nil {
		return storiface.WorkerInfo{}, xerrors.Errorf("getting host info: %w", err)
	}

	mem, err := h.Memory()
	if err != nil {
		return storiface.WorkerInfo{}, xerrors.Errorf("getting memory info: %w", err)
	}

	memSwap := mem.VirtualTotal
	if l.noSwap {
		memSwap = 0
	}

	return storiface.WorkerInfo{
		Hostname: hostname,
		Resources: storiface.WorkerResources{
			MemPhysical: mem.Total,
			MemSwap:     memSwap,
			MemReserved: mem.VirtualUsed + mem.Total - mem.Available, // TODO: sub this process
			CPUs:        uint64(runtime.NumCPU()),
			GPUs:        gpus,
		},
	}, nil
}

func (l *LocalWorker) Closing(ctx context.Context) (<-chan struct{}, error) {
	return make(chan struct{}), nil
}

func (l *LocalWorker) Close() error {
	return nil
}

func (l *LocalWorker) AddRange(ctx context.Context, task sealtasks.TaskType, addType int) error {

	switch task {
	case sealtasks.TTPreCommit1:
		if addType == 1 {
			l.preCommit1Now++
		} else {
			l.preCommit1Now--
		}
	case sealtasks.TTPreCommit2:
		if addType == 1 {
			l.preCommit2Now++
		} else {
			l.preCommit2Now--
		}
	case sealtasks.TTCommit1, sealtasks.TTCommit2:
		if addType == 1 {
			l.commitNow++
		} else {
			l.commitNow--
		}
	}

	return nil
}

func (l *LocalWorker) AllowableRange(ctx context.Context, task sealtasks.TaskType) (bool, error) {

	switch task {

	/*	prevent addpiece from queuing
		when the worker has other tasks
		this worker will not execute addpiece
	*/
	case sealtasks.TTAddPiece:
		taskTotal := l.preCommit1Now + l.preCommit2Now + l.commitNow
		if taskTotal > 0 {
			log.Info("this task has other task")
			return false, nil
		}

	case sealtasks.TTPreCommit1:
		if l.preCommit1Max > 0 {
			if l.preCommit1Now >= l.preCommit1Max {
				log.Infof("this task is over range, task: TTPreCommit1, max: %v, now: %v", l.preCommit1Max, l.preCommit1Now)
				return false, nil
			}
		}
	case sealtasks.TTPreCommit2:
		if l.preCommit2Max > 0 {
			if l.preCommit2Now >= l.preCommit2Max {
				log.Infof("this task is over range, task: TTPreCommit2, max: %v, now: %v", l.preCommit2Max, l.preCommit2Now)
				return false, nil
			}
		}
	case sealtasks.TTCommit1, sealtasks.TTCommit2:
		if l.commitMax > 0 {
			if l.commitNow >= l.commitMax {
				log.Infof("this task is over range, task: TTCommit1, max: %v, now: %v", l.commitMax, l.commitNow)
				return false, nil
			}
		}
	}

	return true, nil
}
func (l *LocalWorker) GetWorkerInfo(ctx context.Context) WorkerInfo {
	task := make([]string, 0)

	for info := range l.acceptTasks {
		task = append(task, sealtasks.TaskMean[info])
	}

	sort.Strings(task)

	workerInfo := WorkerInfo{
		PreCommit1Max: l.preCommit1Max,
		PreCommit1Now: l.preCommit1Now,
		PreCommit2Max: l.preCommit2Max,
		PreCommit2Now: l.preCommit2Now,
		CommitMax:     l.commitMax,
		CommitNow:     l.commitNow,
		AcceptTasks:   task,
		Group:         l.group,
	}

	workerInfo.StoreList = make(map[string]string)

	for id, taskType := range l.storeList.list {
		key := "{" + strconv.FormatUint(uint64(id.Miner), 10) + "," + strconv.FormatUint(uint64(id.Number), 10) + "}"
		workerInfo.StoreList[key] = taskType
	}

	return workerInfo
}
func (l *LocalWorker) AddStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error {
	l.storeList.lk.Lock()
	l.storeList.list[ID] = sealtasks.TaskMean[taskType]
	l.storeList.lk.Unlock()
	return nil
}
func (l *LocalWorker) DeleteStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error {
	l.storeList.lk.Lock()
	info, exit := l.storeList.list[ID]
	if exit && info == sealtasks.TaskMean[taskType] {
		delete(l.storeList.list, ID)
	}
	l.storeList.lk.Unlock()
	return nil
}

func (l *LocalWorker) SetWorkerParams(ctx context.Context, key string, val string) error {
	switch key {
	case "precommit1max":
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		l.preCommit1Max = param
	case "precommit2max":
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		l.preCommit2Max = param
	case "commitmax":
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		l.commitMax = param
	case "group":
		l.group = val
	default:
		return xerrors.Errorf("this param is not fount: %s", key)
	}
	return nil
}

func (l *LocalWorker) GetWorkerGroup(ctx context.Context) string {
	return l.group
}

func (l *LocalWorker) GetTaskCount(ctx context.Context) int32 {
	return atomic.LoadInt32(&l.taskCount)
}

func (l *LocalWorker) SetID(ctx context.Context, ID uint64) error {
	atomic.StoreUint64(&l.ID, ID)
	return nil
}

func (l *LocalWorker) GetID(ctx context.Context) uint64 {
	return atomic.LoadUint64(&l.ID)
}

func (l *LocalWorker) AddWorkerTask(ctx context.Context, ID uint64) error {
	return xerrors.Errorf("not supported at this layer")
}

func (l *LocalWorker) GetWorkerWait(ctx context.Context, ID uint64) int {
	return -1
}


var _ Worker = &LocalWorker{}
