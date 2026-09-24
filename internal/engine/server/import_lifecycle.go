package server

import (
	"context"
	"sync"
)

type jobLifecycle struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closing bool
}

func newJobLifecycle() *jobLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &jobLifecycle{ctx: ctx, cancel: cancel}
}

func (l *jobLifecycle) beginShutdown() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.closing {
		l.closing = true
		l.cancel()
	}
	l.mu.Unlock()
}

func (l *jobLifecycle) wait() {
	if l != nil {
		l.wg.Wait()
	}
}

// startImportJob creates and owns a user-visible import job for the lifetime of
// the server. A job can outlive the HTTP request that started it, but server
// shutdown cancels its context and waits for the goroutine before tile sources
// and other shared resources are closed.
func (s *Server) startImportJob(
	set string,
	run func(context.Context, string),
) (*importJob, bool) {
	l := s.jobsCtl
	if l == nil {
		return nil, false
	}

	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return nil, false
	}
	job := s.imports.create(set)
	ctx := l.ctx
	l.wg.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.wg.Done()
		defer func() {
			if ctx.Err() != nil {
				s.imports.update(job.ID, func(j *importJob) {
					if j.State == "running" {
						j.State = "error"
						j.Err = "server shutting down"
					}
				})
			}
		}()
		run(ctx, job.ID)
	}()

	return job, true
}
