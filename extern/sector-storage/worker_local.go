package sectorstorage

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elastic/go-sysinfo"
	"github.com/google/uuid"
	"github.com/hashicorp/go-multierror"
	"github.com/ipfs/go-cid"
	"golang.org/x/xerrors"

	ffi "github.com/filecoin-project/filecoin-ffi"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-statestore"
	storage "github.com/filecoin-project/specs-storage/storage"

	"github.com/filecoin-project/lotus/extern/sector-storage/ffiwrapper"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/stores"
	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"
)

var pathTypes = []storiface.SectorFileType{storiface.FTUnsealed, storiface.FTSealed, storiface.FTCache}

type WorkerConfig struct {
	TaskTypes []sealtasks.TaskType
	NoSwap    bool

	PreCommit1Max int64
	PreCommit2Max int64
	CommitMax     int64
	Group         string
}

// used do provide custom proofs impl (mostly used in testing)
type ExecutorFunc func() (ffiwrapper.Storage, error)

type LocalWorker struct {
	storage    stores.Store
	localStore *stores.Local
	sindex     stores.SectorIndex
	ret        storiface.WorkerReturn
	executor   ExecutorFunc
	noSwap     bool

	ct          *workerCallTracker
	acceptTasks map[sealtasks.TaskType]struct{}
	running     sync.WaitGroup
	taskLk      sync.Mutex

	session     uuid.UUID
	testDisable int64
	closing     chan struct{}

	preCommit1Max int64
	preCommit1Now int64
	preCommit2Max int64
	preCommit2Now int64
	commitMax     int64
	commitNow     int64
	group         string
	storeList     taskList

	lkID      sync.Mutex
	taskCount int32
	ID        uuid.UUID
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

func newLocalWorker(executor ExecutorFunc, wcfg WorkerConfig, store stores.Store, local *stores.Local, sindex stores.SectorIndex, ret storiface.WorkerReturn, cst *statestore.StateStore) *LocalWorker {
	acceptTasks := map[sealtasks.TaskType]struct{}{}
	for _, taskType := range wcfg.TaskTypes {
		acceptTasks[taskType] = struct{}{}
	}

	if wcfg.Group == "" {
		wcfg.Group = "all"
	}

	w := &LocalWorker{
		storage:    store,
		localStore: local,
		sindex:     sindex,
		ret:        ret,

		ct: &workerCallTracker{
			st: cst,
		},
		acceptTasks: acceptTasks,
		executor:    executor,
		taskCount:   0,
		noSwap:      wcfg.NoSwap,

		session: uuid.New(),
		closing: make(chan struct{}),

		preCommit1Max: wcfg.PreCommit1Max,
		preCommit2Max: wcfg.PreCommit2Max,
		commitMax:     wcfg.CommitMax,
		group:         wcfg.Group,
		storeList: taskList{
			list: make(map[abi.SectorID]string),
		},
	}

	w.ID = w.session

	if w.executor == nil {
		w.executor = w.ffiExec
	}

	unfinished, err := w.ct.unfinished()
	if err != nil {
		log.Errorf("reading unfinished tasks: %+v", err)
		return w
	}

	go func() {
		for _, call := range unfinished {
			err := storiface.Err(storiface.ErrTempWorkerRestart, xerrors.New("worker restarted"))

			// TODO: Handle restarting PC1 once support is merged

			if doReturn(context.TODO(), call.RetType, call.ID, ret, nil, err) {
				if err := w.ct.onReturned(call.ID); err != nil {
					log.Errorf("marking call as returned failed: %s: %+v", call.RetType, err)
				}
			}
		}
	}()

	return w
}

func NewLocalWorker(wcfg WorkerConfig, store stores.Store, local *stores.Local, sindex stores.SectorIndex, ret storiface.WorkerReturn, cst *statestore.StateStore) *LocalWorker {
	return newLocalWorker(nil, wcfg, store, local, sindex, ret, cst)
}

type localWorkerPathProvider struct {
	w  *LocalWorker
	op storiface.AcquireMode
}

func (l *localWorkerPathProvider) AcquireSector(ctx context.Context, sector storage.SectorRef, existing storiface.SectorFileType, allocate storiface.SectorFileType, sealing storiface.PathType) (storiface.SectorPaths, func(), error) {
	paths, storageIDs, err := l.w.storage.AcquireSector(ctx, sector, existing, allocate, sealing, l.op)
	if err != nil {
		return storiface.SectorPaths{}, nil, err
	}

	releaseStorage, err := l.w.localStore.Reserve(ctx, sector, allocate, storageIDs, storiface.FSOverheadSeal)
	if err != nil {
		return storiface.SectorPaths{}, nil, xerrors.Errorf("reserving storage space: %w", err)
	}

	log.Debugf("acquired sector %d (e:%d; a:%d): %v", sector, existing, allocate, paths)

	return paths, func() {
		releaseStorage()

		for _, fileType := range pathTypes {
			if fileType&allocate == 0 {
				continue
			}

			sid := storiface.PathByType(storageIDs, fileType)

			if err := l.w.sindex.StorageDeclareSector(ctx, stores.ID(sid), sector.ID, fileType, l.op == storiface.AcquireMove); err != nil {
				log.Errorf("declare sector error: %+v", err)
			}
		}
	}, nil
}

func (l *LocalWorker) ffiExec() (ffiwrapper.Storage, error) {
	return ffiwrapper.New(&localWorkerPathProvider{w: l})
}

type ReturnType string

const (
	AddPiece        ReturnType = "AddPiece"
	SealPreCommit1  ReturnType = "SealPreCommit1"
	SealPreCommit2  ReturnType = "SealPreCommit2"
	SealCommit1     ReturnType = "SealCommit1"
	SealCommit2     ReturnType = "SealCommit2"
	FinalizeSector  ReturnType = "FinalizeSector"
	ReleaseUnsealed ReturnType = "ReleaseUnsealed"
	MoveStorage     ReturnType = "MoveStorage"
	UnsealPiece     ReturnType = "UnsealPiece"
	ReadPiece       ReturnType = "ReadPiece"
	Fetch           ReturnType = "Fetch"
)

// in: func(WorkerReturn, context.Context, CallID, err string)
// in: func(WorkerReturn, context.Context, CallID, ret T, err string)
func rfunc(in interface{}) func(context.Context, storiface.CallID, storiface.WorkerReturn, interface{}, *storiface.CallError) error {
	rf := reflect.ValueOf(in)
	ft := rf.Type()
	withRet := ft.NumIn() == 5

	return func(ctx context.Context, ci storiface.CallID, wr storiface.WorkerReturn, i interface{}, err *storiface.CallError) error {
		rctx := reflect.ValueOf(ctx)
		rwr := reflect.ValueOf(wr)
		rerr := reflect.ValueOf(err)
		rci := reflect.ValueOf(ci)

		var ro []reflect.Value

		if withRet {
			ret := reflect.ValueOf(i)
			if i == nil {
				ret = reflect.Zero(rf.Type().In(3))
			}

			ro = rf.Call([]reflect.Value{rwr, rctx, rci, ret, rerr})
		} else {
			ro = rf.Call([]reflect.Value{rwr, rctx, rci, rerr})
		}

		if !ro[0].IsNil() {
			return ro[0].Interface().(error)
		}

		return nil
	}
}

var returnFunc = map[ReturnType]func(context.Context, storiface.CallID, storiface.WorkerReturn, interface{}, *storiface.CallError) error{
	AddPiece:        rfunc(storiface.WorkerReturn.ReturnAddPiece),
	SealPreCommit1:  rfunc(storiface.WorkerReturn.ReturnSealPreCommit1),
	SealPreCommit2:  rfunc(storiface.WorkerReturn.ReturnSealPreCommit2),
	SealCommit1:     rfunc(storiface.WorkerReturn.ReturnSealCommit1),
	SealCommit2:     rfunc(storiface.WorkerReturn.ReturnSealCommit2),
	FinalizeSector:  rfunc(storiface.WorkerReturn.ReturnFinalizeSector),
	ReleaseUnsealed: rfunc(storiface.WorkerReturn.ReturnReleaseUnsealed),
	MoveStorage:     rfunc(storiface.WorkerReturn.ReturnMoveStorage),
	UnsealPiece:     rfunc(storiface.WorkerReturn.ReturnUnsealPiece),
	ReadPiece:       rfunc(storiface.WorkerReturn.ReturnReadPiece),
	Fetch:           rfunc(storiface.WorkerReturn.ReturnFetch),
}

func (l *LocalWorker) asyncCall(ctx context.Context, sector storage.SectorRef, rt ReturnType, work func(ctx context.Context, ci storiface.CallID) (interface{}, error)) (storiface.CallID, error) {
	ci := storiface.CallID{
		Sector: sector.ID,
		ID:     uuid.New(),
	}

	if err := l.ct.onStart(ci, rt); err != nil {
		log.Errorf("tracking call (start): %+v", err)
	}

	l.running.Add(1)

	go func() {
		defer l.running.Done()

		ctx := &wctx{
			vals:    ctx,
			closing: l.closing,
		}

		res, err := work(ctx, ci)

		if err != nil {
			rb, err := json.Marshal(res)
			if err != nil {
				log.Errorf("tracking call (marshaling results): %+v", err)
			} else {
				if err := l.ct.onDone(ci, rb); err != nil {
					log.Errorf("tracking call (done): %+v", err)
				}
			}
		}

		if doReturn(ctx, rt, ci, l.ret, res, toCallError(err)) {
			if err := l.ct.onReturned(ci); err != nil {
				log.Errorf("tracking call (done): %+v", err)
			}
		}
	}()

	return ci, nil
}

func toCallError(err error) *storiface.CallError {
	var serr *storiface.CallError
	if err != nil && !xerrors.As(err, &serr) {
		serr = storiface.Err(storiface.ErrUnknown, err)
	}

	return serr
}

// doReturn tries to send the result to manager, returns true if successful
func doReturn(ctx context.Context, rt ReturnType, ci storiface.CallID, ret storiface.WorkerReturn, res interface{}, rerr *storiface.CallError) bool {
	for {
		err := returnFunc[rt](ctx, ci, ret, res, rerr)
		if err == nil {
			break
		}

		log.Errorf("return error, will retry in 5s: %s: %+v", rt, err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			log.Errorf("failed to return results: %s", ctx.Err())

			// fine to just return, worker is most likely shutting down, and
			// we didn't mark the result as returned yet, so we'll try to
			// re-submit it on restart
			return false
		}
	}

	return true
}

func (l *LocalWorker) NewSector(ctx context.Context, sector storage.SectorRef) error {
	sb, err := l.executor()
	if err != nil {
		return err
	}

	return sb.NewSector(ctx, sector)
}

func (l *LocalWorker) AddPiece(ctx context.Context, sector storage.SectorRef, epcs []abi.UnpaddedPieceSize, sz abi.UnpaddedPieceSize, r io.Reader) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, AddPiece, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		return sb.AddPiece(ctx, sector, epcs, sz, r)
	})
}

func (l *LocalWorker) Fetch(ctx context.Context, sector storage.SectorRef, fileType storiface.SectorFileType, ptype storiface.PathType, am storiface.AcquireMode) (storiface.CallID, error) {
	return l.asyncCall(ctx, sector, Fetch, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		_, done, err := (&localWorkerPathProvider{w: l, op: am}).AcquireSector(ctx, sector, fileType, storiface.FTNone, ptype)
		if err == nil {
			done()
		}

		return nil, err
	})
}

func (l *LocalWorker) SealPreCommit1(ctx context.Context, sector storage.SectorRef, ticket abi.SealRandomness, pieces []abi.PieceInfo) (storiface.CallID, error) {
	return l.asyncCall(ctx, sector, SealPreCommit1, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {

		{
			// cleanup previous failed attempts if they exist
			if err := l.storage.Remove(ctx, sector.ID, storiface.FTSealed, true); err != nil {
				return nil, xerrors.Errorf("cleaning up sealed data: %w", err)
			}

			if err := l.storage.Remove(ctx, sector.ID, storiface.FTCache, true); err != nil {
				return nil, xerrors.Errorf("cleaning up cache data: %w", err)
			}
		}

		sb, err := l.executor()
		if err != nil {
			return nil, err
		}
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		return sb.SealPreCommit1(ctx, sector, ticket, pieces)
	})
}

func (l *LocalWorker) SealPreCommit2(ctx context.Context, sector storage.SectorRef, phase1Out storage.PreCommit1Out) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealPreCommit2, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()

		return sb.SealPreCommit2(ctx, sector, phase1Out)
	})
}

func (l *LocalWorker) SealCommit1(ctx context.Context, sector storage.SectorRef, ticket abi.SealRandomness, seed abi.InteractiveSealRandomness, pieces []abi.PieceInfo, cids storage.SectorCids) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealCommit1, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		return sb.SealCommit1(ctx, sector, ticket, seed, pieces, cids)
	})
}

func (l *LocalWorker) SealCommit2(ctx context.Context, sector storage.SectorRef, phase1Out storage.Commit1Out) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealCommit2, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		return sb.SealCommit2(ctx, sector, phase1Out)
	})
}

func (l *LocalWorker) FinalizeSector(ctx context.Context, sector storage.SectorRef, keepUnsealed []storage.Range) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, FinalizeSector, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		if err := sb.FinalizeSector(ctx, sector, keepUnsealed); err != nil {
			return nil, xerrors.Errorf("finalizing sector: %w", err)
		}

		if len(keepUnsealed) == 0 {
			if err := l.storage.Remove(ctx, sector.ID, storiface.FTUnsealed, true); err != nil {
				return nil, xerrors.Errorf("removing unsealed data: %w", err)
			}
		}

		return nil, err
	})
}

func (l *LocalWorker) ReleaseUnsealed(ctx context.Context, sector storage.SectorRef, safeToFree []storage.Range) (storiface.CallID, error) {
	return storiface.UndefCall, xerrors.Errorf("implement me")
}

func (l *LocalWorker) Remove(ctx context.Context, sector abi.SectorID) error {
	var err error
	atomic.AddInt32(&l.taskCount, 1)
	defer func() {
		atomic.AddInt32(&l.taskCount, -1)
	}()
	if rerr := l.storage.Remove(ctx, sector, storiface.FTSealed, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (sealed): %w", rerr))
	}
	if rerr := l.storage.Remove(ctx, sector, storiface.FTCache, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (cache): %w", rerr))
	}
	if rerr := l.storage.Remove(ctx, sector, storiface.FTUnsealed, true); rerr != nil {
		err = multierror.Append(err, xerrors.Errorf("removing sector (unsealed): %w", rerr))
	}

	return err
}

func (l *LocalWorker) MoveStorage(ctx context.Context, sector storage.SectorRef, types storiface.SectorFileType) (storiface.CallID, error) {
	return l.asyncCall(ctx, sector, MoveStorage, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		return nil, l.storage.MoveStorage(ctx, sector, types)
	})
}

func (l *LocalWorker) UnsealPiece(ctx context.Context, sector storage.SectorRef, index storiface.UnpaddedByteIndex, size abi.UnpaddedPieceSize, randomness abi.SealRandomness, cid cid.Cid) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, UnsealPiece, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()
		if err = sb.UnsealPiece(ctx, sector, index, size, randomness, cid); err != nil {
			return nil, xerrors.Errorf("unsealing sector: %w", err)
		}

		if err = l.storage.RemoveCopies(ctx, sector.ID, storiface.FTSealed); err != nil {
			return nil, xerrors.Errorf("removing source data: %w", err)
		}

		if err = l.storage.RemoveCopies(ctx, sector.ID, storiface.FTCache); err != nil {
			return nil, xerrors.Errorf("removing source data: %w", err)
		}

		return nil, nil
	})
}

func (l *LocalWorker) ReadPiece(ctx context.Context, writer io.Writer, sector storage.SectorRef, index storiface.UnpaddedByteIndex, size abi.UnpaddedPieceSize) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, ReadPiece, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		atomic.AddInt32(&l.taskCount, 1)
		defer func() {
			atomic.AddInt32(&l.taskCount, -1)
		}()

		return sb.ReadPiece(ctx, writer, sector, index, size)
	})
}

func (l *LocalWorker) TaskTypes(context.Context) (map[sealtasks.TaskType]struct{}, error) {
	l.taskLk.Lock()
	defer l.taskLk.Unlock()

	return l.acceptTasks, nil
}

func (l *LocalWorker) TaskDisable(ctx context.Context, tt sealtasks.TaskType) error {
	l.taskLk.Lock()
	defer l.taskLk.Unlock()

	delete(l.acceptTasks, tt)
	return nil
}

func (l *LocalWorker) TaskEnable(ctx context.Context, tt sealtasks.TaskType) error {
	l.taskLk.Lock()
	defer l.taskLk.Unlock()

	l.acceptTasks[tt] = struct{}{}
	return nil
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

func (l *LocalWorker) Session(ctx context.Context) (uuid.UUID, error) {
	if atomic.LoadInt64(&l.testDisable) == 1 {
		return uuid.UUID{}, xerrors.Errorf("disabled")
	}

	select {
	case <-l.closing:
		return ClosedWorkerID, nil
	default:
		return l.session, nil
	}
}

func (l *LocalWorker) Close() error {
	close(l.closing)
	return nil
}

// WaitQuiet blocks as long as there are tasks running
func (l *LocalWorker) WaitQuiet() {
	l.running.Wait()
}

type wctx struct {
	vals    context.Context
	closing chan struct{}
}

func (w *wctx) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (w *wctx) Done() <-chan struct{} {
	return w.closing
}

func (w *wctx) Err() error {
	select {
	case <-w.closing:
		return context.Canceled
	default:
		return nil
	}
}

func (w *wctx) Value(key interface{}) interface{} {
	return w.vals.Value(key)
}

var _ context.Context = &wctx{}

var _ Worker = &LocalWorker{}

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
			log.Debug("this task has other task, task: TTAddPiece")
			return false, nil
		}

	case sealtasks.TTPreCommit1:
		if l.preCommit1Max > 0 {
			if l.preCommit1Now >= l.preCommit1Max {
				log.Debugf("this task is over range, task: TTPreCommit1, max: %v, now: %v", l.preCommit1Max, l.preCommit1Now)
				return false, nil
			}
		}
	case sealtasks.TTPreCommit2:
		if l.preCommit2Max > 0 {
			if l.preCommit2Now >= l.preCommit2Max {
				log.Debugf("this task is over range, task: TTPreCommit2, max: %v, now: %v", l.preCommit2Max, l.preCommit2Now)
				return false, nil
			}
		}
	case sealtasks.TTCommit1, sealtasks.TTCommit2:
		if l.commitMax > 0 {
			if l.commitNow >= l.commitMax {
				log.Debugf("this task is over range, task: TTCommit1, max: %v, now: %v", l.commitMax, l.commitNow)
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

func (l *LocalWorker) SetID(ctx context.Context, ID uuid.UUID) error {
	l.lkID.Lock()
	defer l.lkID.Unlock()
	copy(l.ID[:], ID[:])
	return nil
}

func (l *LocalWorker) GetID(ctx context.Context) uuid.UUID {
	l.lkID.Lock()
	defer l.lkID.Unlock()
	var id uuid.UUID
	copy(id[:], l.ID[:])
	return id
}

func (l *LocalWorker) AddWorkerTask(ctx context.Context, ID uuid.UUID) error {
	return xerrors.Errorf("not supported at this layer")
}

func (l *LocalWorker) GetWorkerWait(ctx context.Context, ID uuid.UUID) int {
	return -1
}
