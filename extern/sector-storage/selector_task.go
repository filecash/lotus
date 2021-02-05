package sectorstorage

import (
	"context"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-state-types/abi"

	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/stores"
)

type taskSelector struct {
	best []stores.StorageInfo //nolint: unused, structcheck
	task sealtasks.TaskType
}

func newTaskSelector(task sealtasks.TaskType) *taskSelector {
	return &taskSelector{
		task: task,
	}
}

func (s *taskSelector) Ok(ctx context.Context, task sealtasks.TaskType, spt abi.RegisteredSealProof, whnd *workerHandle) (bool, error) {
	tasks, err := whnd.w.TaskTypes(ctx)
	if err != nil {
		return false, xerrors.Errorf("getting supported worker task types: %w", err)
	}
	_, supported := tasks[task]

	return supported, nil
}

func (s *taskSelector) Cmp(ctx context.Context, _ sealtasks.TaskType, a, b *workerHandle) (bool, error) {
	atasks, err := a.w.TaskTypes(ctx)
	if err != nil {
		return true, xerrors.Errorf("getting supported worker task types: %w", err)
	}
	btasks, err := b.w.TaskTypes(ctx)
	if err != nil {
		return true, xerrors.Errorf("getting supported worker task types: %w", err)
	}
	v, ok := a.reqTask[s.task]
        if ok && v > 0 {
                return true, nil
        }
        v, ok = b.reqTask[s.task]
        if ok && v > 0 {
                return false, nil
        }

	if len(atasks) != len(btasks) {
		return len(atasks) < len(btasks), nil // prefer workers which can do less
	}

	return a.utilization() < b.utilization(), nil
}

var _ WorkerSelector = &allocSelector{}
