package sectorstorage

import (
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"time"

	"context"

	"github.com/google/uuid"

	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"
	"golang.org/x/xerrors"
)

func (m *Manager) WorkerStats() map[uuid.UUID]storiface.WorkerStats {
	m.sched.workersLk.RLock()
	defer m.sched.workersLk.RUnlock()

	out := map[uuid.UUID]storiface.WorkerStats{}

	for id, handle := range m.sched.workers {
		out[uuid.UUID(id)] = storiface.WorkerStats{
			Info:    handle.info,
			Enabled: handle.enabled,

			MemUsedMin: handle.active.memUsedMin,
			MemUsedMax: handle.active.memUsedMax,
			GpuUsed:    handle.active.gpuUsed,
			CpuUse:     handle.active.cpuUse,
		}
	}

	return out
}

func (m *Manager) WorkerJobs() map[uuid.UUID][]storiface.WorkerJob {
	out := map[uuid.UUID][]storiface.WorkerJob{}
	calls := map[storiface.CallID]struct{}{}

	for _, t := range m.sched.workTracker.Running() {
		out[uuid.UUID(t.worker)] = append(out[uuid.UUID(t.worker)], t.job)
		calls[t.job.ID] = struct{}{}
	}

	m.sched.workersLk.RLock()

	for id, handle := range m.sched.workers {
		handle.wndLk.Lock()
		for wi, window := range handle.activeWindows {
			for _, request := range window.todo {
				out[uuid.UUID(id)] = append(out[uuid.UUID(id)], storiface.WorkerJob{
					ID:      storiface.UndefCall,
					Sector:  request.sector.ID,
					Task:    request.taskType,
					RunWait: wi + 1,
					Start:   request.start,
				})
			}
		}
		handle.wndLk.Unlock()
	}

	m.sched.workersLk.RUnlock()

	m.workLk.Lock()
	defer m.workLk.Unlock()

	for id, work := range m.callToWork {
		_, found := calls[id]
		if found {
			continue
		}

		var ws WorkState
		if err := m.work.Get(work).Get(&ws); err != nil {
			log.Errorf("WorkerJobs: get work %s: %+v", work, err)
		}

		wait := storiface.RWRetWait
		if _, ok := m.results[work]; ok {
			wait = storiface.RWReturned
		}
		if ws.Status == wsDone {
			wait = storiface.RWRetDone
		}

		out[uuid.UUID{}] = append(out[uuid.UUID{}], storiface.WorkerJob{
			ID:       id,
			Sector:   id.Sector,
			Task:     work.Method,
			RunWait:  wait,
			Start:    time.Unix(ws.StartTime, 0),
			Hostname: ws.WorkerHostname,
		})
	}

	return out
}

func (m *Manager) GetWorker(ctx context.Context) map[string]WorkerInfo {
	m.sched.workersLk.Lock()
	defer m.sched.workersLk.Unlock()

	out := map[string]WorkerInfo{}

	for id, handle := range m.sched.workers {
		info := handle.workerRpc.GetWorkerInfo(ctx)
		out[id.String()] = info
	}
	return out
}

func (m *Manager) SetWorkerParam(ctx context.Context, worker, key, value string) error {
	m.sched.workersLk.Lock()
	defer m.sched.workersLk.Unlock()

	uuidWorker, err := uuid.Parse(worker)
	if err != nil {
		return err
	}
	w, exist := m.sched.workers[WorkerID(uuidWorker)]
	if !exist {
		return xerrors.Errorf("worker not found: %s", key)
	}
	return w.workerRpc.SetWorkerParams(ctx, key, value)
}

func (m *Manager) UpdateSectorGroup(ctx context.Context, SectorNum string, group string) error {
	m.sched.execSectorWorker.lk.Lock()
	defer m.sched.execSectorWorker.lk.Unlock()

	sectorGroup, isExist := m.sched.execSectorWorker.group[SectorNum]
	if !isExist {
		return xerrors.Errorf("SectorID not found: %s", SectorNum)
	}
	if group == sectorGroup {
		return xerrors.Errorf("The original group is the same as the current group")
	}

	m.sched.execSectorWorker.group[SectorNum] = group
	err := m.sched.updateSectorGroupFile()
	return err
}

func (m *Manager) DeleteSectorGroup(ctx context.Context, SectorNum string) error {
	m.sched.execSectorWorker.lk.Lock()
	defer m.sched.execSectorWorker.lk.Unlock()

	delete(m.sched.execSectorWorker.group, SectorNum)

	err := m.sched.updateSectorGroupFile()
	return err
}

func (m *Manager) TrySched(ctx context.Context, group string) (bool, error) {
	m.sched.workersLk.RLock()
	defer m.sched.workersLk.RUnlock()
	sh := m.sched
	wList := make([]WorkerID, 0)
	if group == "all" {
		allList := sh.execGroupList.list
		for _, l := range allList {
			wList = append(wList, l...)
		}
	}else {
		gList, exist := sh.execGroupList.list[group]
		if exist {
			wList = append(wList, gList...)
		}
	}

	if len(wList) < 1 {
		return false, xerrors.Errorf("execGroupList not found：%s", group)
	}

	accpeWorker := make([]WorkerID, 0)
	needRes := ResourceTable[sealtasks.TTPreCommit1][abi.RegisteredSealProof_StackedDrg32GiBV1]
	sel := newAllocSelector(m.index, storiface.FTSealed|storiface.FTCache, storiface.PathSealing, sealtasks.TTPreCommit1)
	for _, w := range wList {
		worker, ok := sh.workers[w]
		if !ok {
			log.Errorf("worker referenced by windowRequest not found (worker: %s)", worker)
			// TODO: How to move forward here?
			continue
		}
		if !worker.enabled {
			log.Debugw("skipping disabled worker", "worker", worker)
			continue
		}

		if worker.active.canHandleRequest(needRes, w, "autoTask", worker.info.Resources) {
			continue
		}
		ok, err := sel.Ok(ctx, sealtasks.TTPreCommit1, abi.RegisteredSealProof_StackedDrg32GiBV1, worker)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		accpeWorker = append(accpeWorker, w)
		break
	}
	if len(accpeWorker) > 0 {
		return false, xerrors.Errorf("can not found worker to do")
	}
	return true, nil
}
