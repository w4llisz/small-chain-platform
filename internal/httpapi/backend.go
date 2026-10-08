package httpapi

import (
	"context"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

type jobBackend interface {
	Submit(context.Context, string, jobs.Spec) (jobs.Job, bool, error)
	Get(context.Context, string) (jobs.Job, error)
	Cancel(context.Context, string) (jobs.Job, error)
	Ready(context.Context) error
	StopAdmission()
}

type memoryBackend struct{ engine *jobs.Engine }

func (m memoryBackend) Submit(_ context.Context, key string, spec jobs.Spec) (jobs.Job, bool, error) {
	return m.engine.Submit(key, spec)
}

func (m memoryBackend) Get(_ context.Context, id string) (jobs.Job, error) {
	return m.engine.Get(id)
}

func (m memoryBackend) Cancel(_ context.Context, id string) (jobs.Job, error) {
	return m.engine.Cancel(id)
}

func (m memoryBackend) Ready(context.Context) error {
	if !m.engine.Stats().Accepting {
		return jobs.ErrClosed
	}
	return nil
}

func (m memoryBackend) StopAdmission() { m.engine.StopAdmission() }

type postgresBackend struct{ store *postgres.Store }

func (p postgresBackend) Submit(ctx context.Context, key string, spec jobs.Spec) (jobs.Job, bool, error) {
	return p.store.Submit(ctx, key, spec)
}

func (p postgresBackend) Get(ctx context.Context, id string) (jobs.Job, error) {
	return p.store.Get(ctx, id)
}

func (p postgresBackend) Cancel(ctx context.Context, id string) (jobs.Job, error) {
	return p.store.Cancel(ctx, id, "canceled by operator")
}

func (p postgresBackend) Ready(ctx context.Context) error { return p.store.Ping(ctx) }

func (postgresBackend) StopAdmission() {}
