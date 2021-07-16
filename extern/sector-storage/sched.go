package sectorstorage

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-storage/storage"

	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"
)

type schedPrioCtxKey int

var SchedPriorityKey schedPrioCtxKey
var DefaultSchedPriority = 0
var SelectorTimeout = 5 * time.Second
var InitWait = 3 * time.Second

var (
	SchedWindows = 2
)

func getPriority(ctx context.Context) int {
	sp := ctx.Value(SchedPriorityKey)
	if p, ok := sp.(int); ok {
		return p
	}

	return DefaultSchedPriority
}

func WithPriority(ctx context.Context, priority int) context.Context {
	return context.WithValue(ctx, SchedPriorityKey, priority)
}

const mib = 1 << 20

type WorkerAction func(ctx context.Context, w Worker) error

type WorkerSelector interface {
	Ok(ctx context.Context, task sealtasks.TaskType, spt abi.RegisteredSealProof, a *workerHandle) (bool, error) // true if worker is acceptable for performing a task

	Cmp(ctx context.Context, task sealtasks.TaskType, a, b *workerHandle) (bool, error) // true if a is preferred over b
}

type scheduler struct {
	workersLk sync.RWMutex
	workers   map[WorkerID]*workerHandle

	execSectorWorker sectorGroup
	execGroupList    groupList

	schedule       chan *workerRequest
	windowRequests chan *schedWindowRequest
	workerChange   chan struct{} // worker added / changed/freed resources
	workerDisable  chan workerDisableReq

	// owned by the sh.runSched goroutine
	schedQueue  *requestQueue
	openWindows []*schedWindowRequest

	workTracker *workTracker

	info chan func(interface{})

	closing  chan struct{}
	closed   chan struct{}
	testSync chan struct{} // used for testing
}

type sectorGroup struct {
	lk    sync.RWMutex
	group map[string]string
	path  string
}

type groupList struct {
	lk sync.RWMutex
	list map[string][]WorkerID
}

type workerHandle struct {

	isDeleteGroup bool

	workerRpc Worker

	info storiface.WorkerInfo

	preparing *activeResources
	active    *activeResources
	reqTask   map[sealtasks.TaskType]uint

	lk sync.Mutex

	wndLk         sync.Mutex
	activeWindows []*schedWindow

	enabled bool

	// for sync manager goroutine closing
	cleanupStarted bool
	closedMgr      chan struct{}
	closingMgr     chan struct{}
	ctx            context.Context
}

type schedWindowRequest struct {
	worker WorkerID

	done chan *schedWindow
}

type schedWindow struct {
	allocated activeResources
	todo      []*workerRequest
}

type workerDisableReq struct {
	activeWindows []*schedWindow
	wid           WorkerID
	done          func()
}

type activeResources struct {
	memUsedMin uint64
	memUsedMax uint64
	gpuUsed    bool
	cpuUse     uint64

	cond *sync.Cond
}

type workerRequest struct {
	sector   storage.SectorRef
	taskType sealtasks.TaskType
	priority int // larger values more important
	sel      WorkerSelector

	prepare WorkerAction
	work    WorkerAction

	start time.Time

	index int // The index of the item in the heap.

	indexHeap int
	ret       chan<- workerResponse
	ctx       context.Context
	
	// fic remotec2
	isRemoteC2 bool
	group string
}

type workerResponse struct {
	err error
}

// fic remotec2
type remoteC2Workers struct {
	list map[*workerHandle]bool
	lk   sync.RWMutex
}

func newScheduler() *scheduler {

	var sectorGroups map[string]string
	minerPath := filepath.Join(os.Getenv("LOTUS_MINER_PATH"), "sectorGroup.json")

	_, err := os.Stat(minerPath)
	if err == nil {
		sector, err := ioutil.ReadFile(minerPath)
		if err == nil {
			err = json.Unmarshal(sector, &sectorGroups)
			if err != nil {
				sectorGroups = map[string]string{}
			}
		} else {
			sectorGroups = map[string]string{}
		}
	} else {
		sectorGroups = map[string]string{}
	}

	return &scheduler{
		workers: map[WorkerID]*workerHandle{},

		execSectorWorker: sectorGroup{
			group: sectorGroups,
			path:  minerPath,
		},

		execGroupList: groupList{
			list: map[string][]WorkerID{},
		},

		schedule:       make(chan *workerRequest),
		windowRequests: make(chan *schedWindowRequest, 20),
		workerChange:   make(chan struct{}, 20),
		workerDisable:  make(chan workerDisableReq),

		schedQueue: &requestQueue{},

		workTracker: &workTracker{
			done:    map[storiface.CallID]struct{}{},
			running: map[storiface.CallID]trackedWork{},
		},

		info: make(chan func(interface{})),

		closing: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (sh *scheduler) Schedule(ctx context.Context, sector storage.SectorRef, taskType sealtasks.TaskType, sel WorkerSelector, prepare WorkerAction, work WorkerAction) error {
	ret := make(chan workerResponse)

	// fic remotec2
	isRemoteC2 := false
	if ok := ctx.Value("remoteC2"); ok != nil {
		isRemoteC2 = ok.(bool)
	}

	select {
	case sh.schedule <- &workerRequest{
		sector:   sector,
		taskType: taskType,
		priority: getPriority(ctx),
		sel:      sel,

		prepare: prepare,
		work:    work,

		start: time.Now(),

		ret: ret,
		ctx: ctx,

		isRemoteC2: isRemoteC2,
	}:
	case <-sh.closing:
		return xerrors.New("closing")
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case resp := <-ret:
		return resp.err
	case <-sh.closing:
		return xerrors.New("closing")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sh *scheduler) ScheduleExt(ctx context.Context, sector storage.SectorRef, taskType sealtasks.TaskType, sel WorkerSelector, prepare WorkerAction, work WorkerAction, group string) error {
	ret := make(chan workerResponse)

	// fic remotec2
	isRemoteC2 := false
	if ok := ctx.Value("remoteC2"); ok != nil {
		isRemoteC2 = ok.(bool)
	}

	select {
	case sh.schedule <- &workerRequest{
		sector:   sector,
		taskType: taskType,
		priority: getPriority(ctx),
		sel:      sel,

		prepare: prepare,
		work:    work,

		start: time.Now(),

		ret: ret,
		ctx: ctx,

		isRemoteC2: isRemoteC2,
		group:      group,
	}:
	case <-sh.closing:
		return xerrors.New("closing")
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case resp := <-ret:
		return resp.err
	case <-sh.closing:
		return xerrors.New("closing")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *workerRequest) respond(err error) {
	select {
	case r.ret <- workerResponse{err: err}:
	case <-r.ctx.Done():
		log.Warnf("request got cancelled before we could respond")
	}
}

type SchedDiagRequestInfo struct {
	Sector   abi.SectorID
	TaskType sealtasks.TaskType
	Priority int
}

type SchedDiagInfo struct {
	Requests    []SchedDiagRequestInfo
	OpenWindows []string
}

func (sh *scheduler) runSched() {
	defer close(sh.closed)

	iw := time.After(InitWait)
	var initialised bool

	for {
		var doSched bool
		var toDisable []workerDisableReq

		select {
		case <-sh.workerChange:
			doSched = true
		case dreq := <-sh.workerDisable:
			toDisable = append(toDisable, dreq)
			doSched = true
		case req := <-sh.schedule:
			sh.schedQueue.Push(req)
			doSched = true

			if sh.testSync != nil {
				sh.testSync <- struct{}{}
			}
		case req := <-sh.windowRequests:
			sh.openWindows = append(sh.openWindows, req)
			doSched = true
		case ireq := <-sh.info:
			ireq(sh.diag())

		case <-iw:
			initialised = true
			iw = nil
			doSched = true
		case <-sh.closing:
			sh.schedClose()
			return
		}

		if doSched && initialised {
			// First gather any pending tasks, so we go through the scheduling loop
			// once for every added task
		loop:
			for {
				select {
				case <-sh.workerChange:
				case dreq := <-sh.workerDisable:
					toDisable = append(toDisable, dreq)
				case req := <-sh.schedule:
					sh.schedQueue.Push(req)
					if sh.testSync != nil {
						sh.testSync <- struct{}{}
					}
				case req := <-sh.windowRequests:
					sh.openWindows = append(sh.openWindows, req)
				default:
					break loop
				}
			}

			for _, req := range toDisable {
				for _, window := range req.activeWindows {
					for _, request := range window.todo {
						sh.schedQueue.Push(request)
					}
				}

				openWindows := make([]*schedWindowRequest, 0, len(sh.openWindows))
				for _, window := range sh.openWindows {
					if window.worker != req.wid {
						openWindows = append(openWindows, window)
					}
				}
				sh.openWindows = openWindows

				sh.workersLk.Lock()
				sh.workers[req.wid].enabled = false
				sh.workersLk.Unlock()

				req.done()
			}

			sh.trySched()
		}

	}
}

func (sh *scheduler) diag() SchedDiagInfo {
	var out SchedDiagInfo

	for sqi := 0; sqi < sh.schedQueue.Len(); sqi++ {
		task := (*sh.schedQueue)[sqi]

		out.Requests = append(out.Requests, SchedDiagRequestInfo{
			Sector:   task.sector.ID,
			TaskType: task.taskType,
			Priority: task.priority,
		})
	}

	sh.workersLk.RLock()
	defer sh.workersLk.RUnlock()

	for _, window := range sh.openWindows {
		out.OpenWindows = append(out.OpenWindows, uuid.UUID(window.worker).String())
	}

	return out
}

func (sh *scheduler) trySched() {
	/*
		This assigns tasks to workers based on:
		- Task priority (achieved by handling sh.schedQueue in order, since it's already sorted by priority)
		- Worker resource availability
		- Task-specified worker preference (acceptableWindows array below sorted by this preference)
		- Window request age

		1. For each task in the schedQueue find windows which can handle them
		1.1. Create list of windows capable of handling a task
		1.2. Sort windows according to task selector preferences
		2. Going through schedQueue again, assign task to first acceptable window
		   with resources available
		3. Submit windows with scheduled tasks to workers

	*/

	sh.workersLk.RLock()
	defer sh.workersLk.RUnlock()

	windowsLen := len(sh.openWindows)
	queuneLen := sh.schedQueue.Len()

	log.Debugf("SCHED %d queued; %d open windows", queuneLen, windowsLen)

	if windowsLen == 0 || queuneLen == 0 {
		// nothing to schedule on
		return
	}

	windows := make([]schedWindow, windowsLen)
	acceptableWindows := make([][]int, queuneLen)

	// Step 1
	throttle := make(chan struct{}, windowsLen)

	var wg sync.WaitGroup
	wg.Add(queuneLen)
	for i := 0; i < queuneLen; i++ {
		throttle <- struct{}{}

		go func(sqi int) {
			defer wg.Done()
			defer func() {
				<-throttle
			}()

			task := (*sh.schedQueue)[sqi]
			needRes := ResourceTable[task.taskType][task.sector.ProofType]

			sh.execSectorWorker.lk.RLock()
			sectorGroup, exist := sh.execSectorWorker.group[task.sector.ID.Number.String()]
			sh.execSectorWorker.lk.RUnlock()
			if !exist && task.taskType == sealtasks.TTAddPiece && task.group != "" {
				log.Debugf("execSectorWorker.group info: exist = %v, taskType = %v, taskGroup = %s\n", exist, task.taskType, task.group)
				exist = true
				sectorGroup = task.group
			}
			task.indexHeap = sqi
			for wnd, windowRequest := range sh.openWindows {
				worker, ok := sh.workers[windowRequest.worker]
				if !ok {
					log.Errorf("worker referenced by windowRequest not found (worker: %s)", windowRequest.worker)
					// TODO: How to move forward here?
					continue
				}

				if !worker.enabled {
					log.Debugw("skipping disabled worker", "worker", windowRequest.worker)
					continue
				}

				// fic remotec2
				if task.isRemoteC2 {
					log.Debugf("Step 1 this task is remoteC2, sectorId: %v", task.sector.ID.Number)
					rpcCtx, cancel := context.WithTimeout(task.ctx, SelectorTimeout)
					ok, err := worker.workerRpc.HasRemoteC2(rpcCtx)
					cancel()
					if err != nil {
						log.Errorf("trySched(1) req.HasRemoteC2 error: %+v", err)
						continue
					}
					if !ok {
						log.Warnf("trySched(1) req.HasRemoteC2 dont exist %+v", worker.info.Hostname)
						continue
					}
				} else {
					// TODO: allow bigger windows
					if !windows[wnd].allocated.canHandleRequest(needRes, windowRequest.worker, "schedAcceptable", worker.info.Resources) {
						continue
					}
				}

				if task.taskType != sealtasks.TTFetch {
					workerGroup := worker.workerRpc.GetWorkerGroup(task.ctx)
					if exist && sectorGroup != "all" && workerGroup != "all" {
						if workerGroup != sectorGroup {
							log.Infof("sectorGroup does not match workerGroup, sectorid: %v, sectorGroup: %s, workerGroup: %s, taskType: %s \n", task.sector, sectorGroup, workerGroup, task.taskType)
							continue
						}
					}
				}

				rpcCtx, cancel := context.WithTimeout(task.ctx, SelectorTimeout)
				ok, err := task.sel.Ok(rpcCtx, task.taskType, task.sector.ProofType, worker)
				cancel()
				if err != nil {
					log.Errorf("trySched(1) req.sel.Ok error: %+v", err)
					continue
				}

				if !ok {
					continue
				}

				acceptableWindows[sqi] = append(acceptableWindows[sqi], wnd)
			}

			if len(acceptableWindows[sqi]) == 0 {
				return
			}

			// Pick best worker (shuffle in case some workers are equally as good)
			rand.Shuffle(len(acceptableWindows[sqi]), func(i, j int) {
				acceptableWindows[sqi][i], acceptableWindows[sqi][j] = acceptableWindows[sqi][j], acceptableWindows[sqi][i] // nolint:scopelint
			})
			sort.SliceStable(acceptableWindows[sqi], func(i, j int) bool {
				wii := sh.openWindows[acceptableWindows[sqi][i]].worker // nolint:scopelint
				wji := sh.openWindows[acceptableWindows[sqi][j]].worker // nolint:scopelint

				fmt.Println(acceptableWindows[sqi])
				fmt.Printf("acceptableWindows[sqi][i]: %d\n", acceptableWindows[sqi][i])
				fmt.Printf("acceptableWindows[sqi][j]: %d\n", acceptableWindows[sqi][j])
				fmt.Printf("wii: %s\n", wii.String())
				fmt.Printf("wji: %s\n", wji.String())

				if wii == wji {
					// for the same worker prefer older windows
					return acceptableWindows[sqi][i] < acceptableWindows[sqi][j] // nolint:scopelint
				}

				wi := sh.workers[wii]
				wj := sh.workers[wji]

				rpcCtx, cancel := context.WithTimeout(task.ctx, SelectorTimeout)
				defer cancel()

				r, err := task.sel.Cmp(rpcCtx, task.taskType, wi, wj)
				if err != nil {
					log.Errorf("selecting best worker: %s", err)
				}
				return r
			})
		}(i)
	}

	wg.Wait()

	log.Debugf("SCHED windows: %+v", windows)
	log.Debugf("SCHED Acceptable win: %+v", acceptableWindows)

	// Step 2
	scheduled := 0
	rmQueue := make([]int, 0, queuneLen)

	for sqi := 0; sqi < queuneLen; sqi++ {
		task := (*sh.schedQueue)[sqi]
		needRes := ResourceTable[task.taskType][task.sector.ProofType]

		selectedWindow := -1
		for _, wnd := range acceptableWindows[task.indexHeap] {
			wid := sh.openWindows[wnd].worker
			wr := sh.workers[wid].info.Resources
			worker := sh.workers[wid]

			ok, err := worker.workerRpc.AllowableRange(task.ctx, task.taskType)
			if !ok || err != nil {
				// worker.lk.Unlock()
				// windows[wnd].lk.Unlock()
				continue
			}

			log.Debugf("SCHED try assign sqi:%d sector %d to window %d", sqi, task.sector.ID.Number, wnd)

			// fic remotec2
			if !task.isRemoteC2 {
				// TODO: allow bigger windows
				if !windows[wnd].allocated.canHandleRequest(needRes, wid, "schedAssign", wr) {
					continue
				}
			}

			log.Debugf("SCHED ASSIGNED sqi:%d sector %d task %s to window %d", sqi, task.sector.ID.Number, task.taskType, wnd)

			err = worker.workerRpc.AddStore(task.ctx, task.sector.ID, task.taskType)
			if err != nil {
				continue
			}

			log.Debugf("SCHED ASSIGNED sqi:%d sector %d task %s to window %d", sqi, task.sector.ID.Number, task.taskType, wnd)

			// fic remotec2
			if !task.isRemoteC2 {
				windows[wnd].allocated.add(wr, needRes)
			}
			//windows[wnd].allocated.add(wr, needRes)
			// TODO: We probably want to re-sort acceptableWindows here based on new
			//  workerHandle.utilization + windows[wnd].allocated.utilization (workerHandle.utilization is used in all
			//  task selectors, but not in the same way, so need to figure out how to do that in a non-O(n^2 way), and
			//  without additional network roundtrips (O(n^2) could be avoided by turning acceptableWindows.[] into heaps))

			selectedWindow = wnd
			break
		}

		if selectedWindow < 0 {
			// fic remotec2
			if task.isRemoteC2 {
				rmQueue = append(rmQueue, sqi)
				log.Errorf("error: sectorID:[%+v] allocate remote c2 failed,because there`s none remoteC2", task.sector.ID)
				go task.respond(xerrors.Errorf("error: sectorID:[%+v] allocate remote c2 failed,because there`s none remoteC2", task.sector.ID))
			}

			// all windows full
			continue
		}

		windows[selectedWindow].todo = append(windows[selectedWindow].todo, task)

		rmQueue = append(rmQueue, sqi)
		scheduled++
	}

	if len(rmQueue) > 0 {
		for i := len(rmQueue) - 1; i >= 0; i-- {
			sh.schedQueue.Remove(rmQueue[i])
		}
	}

	// Step 3

	if scheduled == 0 {
		return
	}

	scheduledWindows := map[int]struct{}{}
	for wnd, window := range windows {
		if len(window.todo) == 0 {
			// Nothing scheduled here, keep the window open
			continue
		}

		scheduledWindows[wnd] = struct{}{}

		window := window // copy
		select {
		case sh.openWindows[wnd].done <- &window:
			fmt.Printf("run task worker id: %s\n", sh.openWindows[wnd].worker.String())
		default:
			log.Error("expected sh.openWindows[wnd].done to be buffered")
		}
	}

	// Rewrite sh.openWindows array, removing scheduled windows
	newOpenWindows := make([]*schedWindowRequest, 0, windowsLen-len(scheduledWindows))
	for wnd, window := range sh.openWindows {
		if _, scheduled := scheduledWindows[wnd]; scheduled {
			// keep unscheduled windows open
			continue
		}

		newOpenWindows = append(newOpenWindows, window)
	}

	sh.openWindows = newOpenWindows
}

func (sh *scheduler) schedClose() {
	sh.workersLk.Lock()
	defer sh.workersLk.Unlock()
	log.Debugf("closing scheduler")

	for i, w := range sh.workers {
		sh.workerCleanup(i, w)
	}
}

func (sh *scheduler) Info(ctx context.Context) (interface{}, error) {
	ch := make(chan interface{}, 1)

	sh.info <- func(res interface{}) {
		ch <- res
	}

	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (sh *scheduler) Close(ctx context.Context) error {
	close(sh.closing)
	select {
	case <-sh.closed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func newRemoteC2Workers() remoteC2Workers {
	return remoteC2Workers{
		list: make(map[*workerHandle]bool),
	}
}

func (sh *scheduler) updateSectorGroupFile() error {
	fmt.Printf("create or modify : %s\n", sh.execSectorWorker.path)
	f, err := os.OpenFile(sh.execSectorWorker.path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	sector, err := json.MarshalIndent(sh.execSectorWorker.group, "", "\t")
	if err != nil {
		return err
	}
	_, err = f.Write(sector)
	if err != nil {
		return err
	}
	return f.Close()
}
