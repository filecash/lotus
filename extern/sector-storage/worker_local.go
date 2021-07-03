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

type TaskAction int

const (
	StartTask TaskAction = 0
	EndTask   TaskAction = 1
)

type WorkerConfig struct {
	TaskTypes []sealtasks.TaskType
	NoSwap    bool
	AddPieceMax   int64
	PreCommit1Max int64
	PreCommit2Max int64
	CommitMax     int64
	ApP1max       int64
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
	apP1Share  bool

	ct          *workerCallTracker
	acceptTasks map[sealtasks.TaskType]struct{}
	autoTaskLimit map[string]int64
	running     sync.WaitGroup
	lk          sync.Mutex

	session     uuid.UUID
	testDisable int64
	closing     chan struct{}

	addPieceMax   int64
	addPieceNow   int64
	preCommit1Max int64
	preCommit1Now int64
	preCommit2Max int64
	preCommit2Now int64
	commit1Max    int64
	commit1Now    int64
	commit2Max    int64
	commit2Now    int64
	apAndP1Max    int64
	apAndP1Now    int64
	storeList     taskList

	group string
	ID        uuid.UUID
}

type taskList struct {
	list map[abi.SectorID]string
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
		autoTaskLimit: make(map[string]int64),
		executor:    executor,
		noSwap:      wcfg.NoSwap,

		session: uuid.New(),
		closing: make(chan struct{}),
		addPieceMax:   wcfg.AddPieceMax,
		preCommit1Max: wcfg.PreCommit1Max,
		preCommit2Max: wcfg.PreCommit2Max,
		commit1Max:    wcfg.CommitMax,
		commit2Max:    wcfg.CommitMax,
		apAndP1Max:    wcfg.ApP1max,
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
		return sb.AddPiece(ctx, sector, epcs, sz, r, "")
	})
}

func (l *LocalWorker) Fetch(ctx context.Context, sector storage.SectorRef, fileType storiface.SectorFileType, ptype storiface.PathType, am storiface.AcquireMode) (storiface.CallID, error) {
	return l.asyncCall(ctx, sector, Fetch, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
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
		return sb.SealPreCommit1(ctx, sector, ticket, pieces)
	})
}

func (l *LocalWorker) SealPreCommit2(ctx context.Context, sector storage.SectorRef, phase1Out storage.PreCommit1Out) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealPreCommit2, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		return sb.SealPreCommit2(ctx, sector, phase1Out)
	})
}

func (l *LocalWorker) SealCommit1(ctx context.Context, sector storage.SectorRef, ticket abi.SealRandomness, seed abi.InteractiveSealRandomness, pieces []abi.PieceInfo, cids storage.SectorCids) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealCommit1, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		return sb.SealCommit1(ctx, sector, ticket, seed, pieces, cids)
	})
}

func (l *LocalWorker) SealCommit2(ctx context.Context, sector storage.SectorRef, phase1Out storage.Commit1Out) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, SealCommit2, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		return sb.SealCommit2(ctx, sector, phase1Out)
	})
}

func (l *LocalWorker) FinalizeSector(ctx context.Context, sector storage.SectorRef, keepUnsealed []storage.Range) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, FinalizeSector, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
		if err := sb.FinalizeSector(ctx, sector, keepUnsealed); err != nil {
			return nil, xerrors.Errorf("finalizing sector: %w", err)
		}

		/*if len(keepUnsealed) == 0 {
			if err := l.storage.Remove(ctx, sector.ID, storiface.FTUnsealed, true); err != nil {
				return nil, xerrors.Errorf("removing unsealed data: %w", err)
			}
		}*/

		return nil, err
	})
}

func (l *LocalWorker) ReleaseUnsealed(ctx context.Context, sector storage.SectorRef, safeToFree []storage.Range) (storiface.CallID, error) {
	return storiface.UndefCall, xerrors.Errorf("implement me")
}

func (l *LocalWorker) Remove(ctx context.Context, sector abi.SectorID) error {
	var err error
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
		return nil, l.storage.MoveStorage(ctx, sector, types)
	})
}

func (l *LocalWorker) UnsealPiece(ctx context.Context, sector storage.SectorRef, index storiface.UnpaddedByteIndex, size abi.UnpaddedPieceSize, randomness abi.SealRandomness, cid cid.Cid) (storiface.CallID, error) {
	sb, err := l.executor()
	if err != nil {
		return storiface.UndefCall, err
	}

	return l.asyncCall(ctx, sector, UnsealPiece, func(ctx context.Context, ci storiface.CallID) (interface{}, error) {
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
		return sb.ReadPiece(ctx, writer, sector, index, size)
	})
}

func (l *LocalWorker) TaskTypes(context.Context) (map[sealtasks.TaskType]struct{}, error) {
	l.lk.Lock()
	defer l.lk.Unlock()

	return l.acceptTasks, nil
}

func (l *LocalWorker) TaskDisable(ctx context.Context, tt sealtasks.TaskType) error {
	l.lk.Lock()
	defer l.lk.Unlock()

	delete(l.acceptTasks, tt)
	return nil
}

func (l *LocalWorker) TaskEnable(ctx context.Context, tt sealtasks.TaskType) error {
	l.lk.Lock()
	defer l.lk.Unlock()

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
		Group: l.group,
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

func (l *LocalWorker) addRange(ctx context.Context, task sealtasks.TaskType, act TaskAction) error {
	switch task {
	case sealtasks.TTAddPiece:
		if act == StartTask {
			if l.apP1Share {
				l.apAndP1Now++
			}
			l.addPieceNow++
		} else {
			if l.apP1Share {
				l.apAndP1Now--
			}
			l.addPieceNow--
		}
	case sealtasks.TTPreCommit1:
		if act == StartTask {
			if l.apP1Share {
				l.apAndP1Now++
			}
			l.preCommit1Now++
		} else {
			if l.apP1Share {
				l.apAndP1Now--
			}
			l.preCommit1Now--
		}
	case sealtasks.TTPreCommit2:
		if act == StartTask {
			l.preCommit2Now++
		} else {
			l.preCommit2Now--
		}
	case sealtasks.TTCommit1:
		if act == StartTask {
			l.commit1Now++
		} else {
			l.commit1Now--
		}
	case sealtasks.TTCommit2:
		if act == StartTask {
			l.commit2Now++
		} else {
			l.commit2Now--
		}
	}

	return nil
}

func (l *LocalWorker) AllowableRange(ctx context.Context, task sealtasks.TaskType) (bool, error) {
	l.lk.Lock()
	defer l.lk.Unlock()

	switch task {

	/*	prevent addpiece from queuing
		when the worker has other tasks
		this worker will not execute addpiece
	*/
	case sealtasks.TTAddPiece:
		if l.addPieceNow > 0 {
			if l.addPieceNow >= l.addPieceMax {
				log.Debugf("this task has over range, task: TTAddPiece, max: %v, now: %v", l.addPieceMax, l.addPieceNow)
				return false, nil
			}
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
	case sealtasks.TTCommit1:
		if l.commit1Max > 0 {
			if l.commit1Now >= l.commit1Max {
				log.Debugf("this task is over range, task: TTCommit1, max: %d, now: %d", l.commit1Max, l.commit1Now)
				return false, nil
			}
		}
	case sealtasks.TTCommit2:
		if l.commit2Max > 0 {
			if l.commit2Now >= l.commit2Max {
				log.Debugf("this task is over range, task: TTCommit2, max: %d, now: %d", l.commit2Max, l.commit2Now)
				return false, nil
			}
		}
	}

	return true, nil
}

func (l *LocalWorker) GetWorkerInfo(ctx context.Context) storiface.WorkerParams {
	task := make([]string, 0)

	l.lk.Lock()
	defer l.lk.Unlock()

	for info := range l.acceptTasks {
		task = append(task, sealtasks.TaskMean[info])
	}

	sort.Strings(task)

	workerInfo := storiface.WorkerParams{
		ApP1Share:     l.apP1Share,
		AddPieceMax:   l.addPieceMax,
		AddPieceNow:   l.addPieceNow,
		PreCommit1Max: l.preCommit1Max,
		PreCommit1Now: l.preCommit1Now,
		PreCommit2Max: l.preCommit2Max,
		PreCommit2Now: l.preCommit2Now,
		Commit1Max:    l.commit1Max,
		Commit1Now:    l.commit1Now,
		Commit2Max:    l.commit2Max,
		Commit2Now:    l.commit2Now,
		ApAndP1Max:    l.apAndP1Max,
		ApAndP1Now:    l.apAndP1Now,
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
	l.lk.Lock()
	defer l.lk.Unlock()
	l.addRange(ctx, taskType, StartTask)
	l.storeList.list[ID] = sealtasks.TaskMean[taskType]
	return nil
}

func (l *LocalWorker) DeleteStore(ctx context.Context, ID abi.SectorID, taskType sealtasks.TaskType) error {
	l.lk.Lock()
	defer l.lk.Unlock()
	info, exit := l.storeList.list[ID]
	if exit && info == sealtasks.TaskMean[taskType] {
		delete(l.storeList.list, ID)
		l.addRange(ctx, taskType, EndTask)
	}
	return nil
}

func (l *LocalWorker) SetWorkerParams(ctx context.Context, key string, val string) error {
	l.lk.Lock()
	defer l.lk.Unlock()
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
		l.commit1Max = param
		l.commit2Max = param
	case "group":
		l.group = val
	case "app1max":
		l.lk.Lock()
		defer l.lk.Unlock()
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		if param > 0 {
			l.apAndP1Max = param
			l.apAndP1Now = l.addPieceNow + l.preCommit1Now
		} else {
			l.apAndP1Max = 0
			l.apAndP1Now = 0
			l.apP1Share = false
		}
	case "autotaskapp1":
		l.lk.Lock()
		defer l.lk.Unlock()
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		if err := l.setAutoTaskParam("app1", param); err != nil {
			return err
		}
	case "autotaskp2":
		l.lk.Lock()
		defer l.lk.Unlock()
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		if err := l.setAutoTaskParam("p2", param); err != nil {
			return err
		}
	case "autotaskc1":
		l.lk.Lock()
		defer l.lk.Unlock()
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		if err := l.setAutoTaskParam("c1", param); err != nil {
			return err
		}
	case "autotaskc2":
		l.lk.Lock()
		defer l.lk.Unlock()
		param, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return xerrors.Errorf("key is error, string to int failed: %w", err)
		}
		if err := l.setAutoTaskParam("c2", param); err != nil {
			return err
		}
	default:
		return xerrors.Errorf("this param is not fount: %s", key)
	}
	return nil
}

func (l *LocalWorker) GetWorkerGroup(ctx context.Context) string {
	return l.group
}

func (l *LocalWorker) AddAutoTaskLimit(ctx context.Context, limit map[string]int64) error {
	for k, v := range limit {
		if err := l.setAutoTaskParam(k, v); err != nil{
			return err
		}
		continue
	}
	return nil
}

func (l *LocalWorker) AutoTaskLimit(ctx context.Context) storiface.AutoTaskReturn {
	isSend := true
	for t, c := range l.autoTaskLimit {
		switch t {
		case "app1":
			if l.apP1Share {
				if l.addPieceNow + l.preCommit1Now >= c {
					log.Debugf("addPieceNow: %d, preCommit1Now: %d, autoTaskLimit: %d, return: false",l.addPieceNow, l.preCommit1Now, l.autoTaskLimit["app1"])
					isSend = false
					break
				}
			} else {
				if l.preCommit1Now >= c {
					log.Debugf("addPieceNow: %d, preCommit1Now: %d, autoTaskLimit: %d, return: false",l.addPieceNow, l.preCommit1Now, l.autoTaskLimit["app1"])
					isSend = false
					break
				}
			}

		case "p2":
			if l.preCommit2Now >= c {
				isSend = false
				break
			}
		case "c1":
			if l.commit1Now >= c {
				isSend = false
				break
			}
		case "c2":
			if l.commit2Now >= c {
				isSend = false
				break
			}
		}
	}
	return storiface.AutoTaskReturn{
		IsSend: isSend,
		Group: l.group,
	}
}

func (l *LocalWorker) setAutoTaskParam (taskType string, value int64) error {
	switch taskType {
	case "app1":
		if l.apP1Share {
			if l.apAndP1Max != 0 && l.apAndP1Max < value {
				return xerrors.New("ap AND p1 max share, apAndP1Max can not less than app1")
			}
		}else {
			if l.preCommit1Max != 0 && l.preCommit1Max < value {
				return xerrors.New("ap AND p1 max not share, preCommit1Max can not less than app1")
			}
		}
		l.autoTaskLimit["app1"] = value
	case "p2":
		if l.preCommit2Max != 0 && l.preCommit2Max < value {
			return xerrors.New("preCommit2Max can not less than p2")
		}
		l.autoTaskLimit["p2"] = value
	case "c1":
		if l.commit1Max > 0 && l.commit1Max < value {
			return xerrors.New("commit1Max can not less than c1")
		}
		l.autoTaskLimit["c1"] = value
	case "c2":
		if l.commit2Max > 0 && l.commit2Max < value {
			return xerrors.New("commit2Max can not less than c1")
		}
		l.autoTaskLimit["c2"] = value
	}
	return nil
}