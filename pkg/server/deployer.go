package server

import (
	"context"
	"log/slog"
	"sync"

	"github.com/sst/ion/internal/util"
	"github.com/sst/ion/pkg/project"
	"github.com/sst/ion/pkg/server/bus"
)

func startDeployer(ctx context.Context, p *project.Project) (util.CleanupFunc, error) {
	trigger := make(chan any)
	mutex := sync.RWMutex{}
	watchedFiles := make(map[string]bool)

	bus.Subscribe(ctx, func(event *FileChangedEvent) {
		mutex.Lock()
		defer mutex.Unlock()
		if _, ok := watchedFiles[event.Path]; ok {
			trigger <- true
		}
	})

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			p.Stack.Run(ctx, &project.StackInput{
				Command: "up",
				Dev:     true,
				OnEvent: func(event *project.StackEvent) {
					bus.Publish(event)
				},
				OnFiles: func(files []string) {
					mutex.RLock()
					defer mutex.RUnlock()
					for _, file := range files {
						watchedFiles[file] = true
					}
				},
			})

			slog.Info("waiting for file changes")
			select {
			case <-ctx.Done():
				return
			case <-trigger:
				continue
			}
		}
	}()

	return func() error {
		slog.Info("cleaning up deployer")
		wg.Wait()
		return nil
	}, nil
}
