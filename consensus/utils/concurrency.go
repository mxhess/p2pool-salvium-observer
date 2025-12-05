package utils

import (
	"golang.org/x/sync/errgroup"
	"runtime"
	"sync/atomic"
)

var GOMAXPROCS = min(runtime.GOMAXPROCS(0), runtime.NumCPU())

func SplitWork(routines int, workSize uint64, do func(workIndex uint64, routineIndex int) error, init func(routines, routineIndex int) error) error {
	if routines <= 0 {
		routines = min(GOMAXPROCS, max(GOMAXPROCS-routines, 1))
	}

	if routines == 1 {
		// do not spawn goroutines if we have a single worker
		err := init(routines, 0)
		if err != nil {
			return err
		}

		for workIndex := uint64(0); workIndex < workSize; workIndex++ {
			if err = do(workIndex, 0); err != nil {
				return err
			}
		}
		return nil
	}

	if workSize < uint64(routines) {
		routines = int(workSize)
	}

	var counter atomic.Uint64

	for routineIndex := 0; routineIndex < routines; routineIndex++ {
		if err := init(routines, routineIndex); err != nil {
			return err
		}
	}

	var eg errgroup.Group

	for routineIndex := 0; routineIndex < routines; routineIndex++ {
		innerRoutineIndex := routineIndex
		eg.Go(func() error {
			var err error

			for {
				workIndex := counter.Add(1)
				if workIndex > workSize {
					return nil
				}

				if err = do(workIndex-1, innerRoutineIndex); err != nil {
					return err
				}
			}
		})
	}
	return eg.Wait()
}
