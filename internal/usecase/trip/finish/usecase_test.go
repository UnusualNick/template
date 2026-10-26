package finish

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

const unknownStatus = "unknown"

func TestExecute(t *testing.T) {
	id := uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")
	local := time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("local", 3600))
	utc := local.UTC()
	active := domain.Trip{ID: id, Status: domain.TripStatusActive}
	completed := domain.Trip{ID: id, Status: domain.TripStatusCompleted, FinishedAt: &utc}
	failure := errors.New("storage unavailable")

	for _, tc := range []struct {
		name      string
		failAt    string
		wantError error
	}{
		{name: "committed"},
		{name: "begin failure", failAt: "begin", wantError: failure},
		{name: "not found", failAt: "not found", wantError: domain.ErrTripNotFound},
		{name: "get failure", failAt: "get", wantError: failure},
		{name: "already completed", failAt: "completed", wantError: domain.ErrTripCompleted},
		{name: "unknown status", failAt: unknownStatus, wantError: domain.ErrInvalidInput},
		{name: "concurrent finish", failAt: "race", wantError: domain.ErrTripCompleted},
		{name: "finish failure", failAt: "finish", wantError: failure},
		{name: "history failure", failAt: "history", wantError: failure},
		{name: "commit failure", failAt: "commit", wantError: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := NewMocktripRepository(ctrl)
			txManager := NewMocktxManager(ctrl)
			ctx := context.Background()
			type txKey struct{}
			txCtx := context.WithValue(ctx, txKey{}, true)
			finishedHooks := 0
			onFinished := func(received context.Context) {
				if received != ctx {
					t.Errorf("hook context = %v; want original context", received)
				}
				finishedHooks++
			}
			transaction := txManager.EXPECT().Do(ctx, gomock.Any())
			if tc.failAt == "begin" {
				transaction.Return(failure)
			} else {
				transaction.DoAndReturn(func(_ context.Context, fn func(context.Context) error) error {
					err := fn(txCtx)
					if finishedHooks != 0 {
						t.Error("post-commit hook ran inside transaction")
					}
					if err != nil {
						return err
					}
					if tc.failAt == "commit" {
						return failure
					}
					return nil
				})
				current, getError := active, error(nil)
				switch tc.failAt {
				case "not found":
					getError = domain.ErrTripNotFound
				case "get":
					getError = failure
				case "completed":
					current = completed
				case unknownStatus:
					current.Status = unknownStatus
				}
				get := repository.EXPECT().GetByID(txCtx, id).Return(current, getError)
				if getError == nil && tc.failAt != "completed" && tc.failAt != unknownStatus {
					finishError := error(nil)
					switch tc.failAt {
					case "race":
						finishError = domain.ErrTripCompleted
					case "finish":
						finishError = failure
					}
					finish := repository.EXPECT().Finish(txCtx, id, utc).DoAndReturn(func(_ context.Context, _ uuid.UUID, at time.Time) (domain.Trip, error) {
						if at.Location() != time.UTC {
							t.Errorf("finish time location = %v; want UTC", at.Location())
						}
						return completed, finishError
					}).After(get)
					if finishError == nil {
						historyError := error(nil)
						if tc.failAt == "history" {
							historyError = failure
						}
						from := domain.TripStatusActive
						repository.EXPECT().InsertStatusHistory(txCtx, id, &from, domain.TripStatusCompleted, "trip finished").Return(historyError).After(finish)
					}
				}
			}
			useCase := New(repository, txManager, onFinished)
			useCase.now = func() time.Time { return local }
			got, err := useCase.Execute(ctx, id)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) || got != (domain.Trip{}) || finishedHooks != 0 {
					t.Fatalf("Execute() = %+v, %v; hook calls = %d; want error %v and no hook", got, err, finishedHooks, tc.wantError)
				}
			} else if err != nil || got.ID != id || got.Status != domain.TripStatusCompleted || got.FinishedAt == nil || !got.FinishedAt.Equal(utc) || finishedHooks != 1 {
				t.Fatalf("Execute() = %+v, %v; hook calls = %d; want committed trip and one hook", got, err, finishedHooks)
			}
		})
	}
}

func TestExecuteWithoutHook(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := NewMocktripRepository(ctrl)
	txManager := NewMocktxManager(ctrl)
	id := uuid.New()
	at := time.Now().UTC()
	completed := domain.Trip{ID: id, Status: domain.TripStatusCompleted, FinishedAt: &at}
	ctx := context.Background()
	txManager.EXPECT().Do(ctx, gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
		return fn(ctx)
	})
	get := repository.EXPECT().GetByID(ctx, id).Return(domain.Trip{ID: id, Status: domain.TripStatusActive}, nil)
	finish := repository.EXPECT().Finish(ctx, id, at).Return(completed, nil).After(get)
	from := domain.TripStatusActive
	repository.EXPECT().InsertStatusHistory(ctx, id, &from, domain.TripStatusCompleted, "trip finished").Return(nil).After(finish)
	uc := New(repository, txManager)
	uc.now = func() time.Time { return at }
	if _, err := uc.Execute(ctx, id); err != nil {
		t.Fatalf("Execute() without hook: %v", err)
	}
}
