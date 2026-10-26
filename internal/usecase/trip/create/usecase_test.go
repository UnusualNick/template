package create

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

type transactionKey struct{}

func TestExecute(t *testing.T) {
	id := uuid.MustParse("fa73b327-7c76-4a44-9c4d-39eb684ca5a2")
	userID := uuid.MustParse("5e17cf63-0784-43bc-9094-bb11d0d6ad0a")
	driverID := uuid.MustParse("5def78c5-b00f-4650-ac21-98991e1788b7")
	at := time.Date(2025, 4, 5, 12, 30, 0, 0, time.FixedZone("offset", 3*60*60))
	input := domain.CreateTrip{
		UserID: userID, DriverID: driverID,
		StartPoint: domain.Coordinates{Latitude: 10, Longitude: 20},
		EndPoint:   domain.Coordinates{Latitude: -30, Longitude: 40},
		Price:      150,
	}
	want := domain.Trip{
		ID: id, UserID: userID, DriverID: driverID,
		StartPoint: input.StartPoint, EndPoint: input.EndPoint,
		Price: input.Price, Status: domain.TripStatusActive, StartedAt: at.UTC(),
	}
	insertErr := errors.New("insert failed")
	historyErr := errors.New("history failed")
	commitErr := errors.New("commit failed")

	tests := []struct {
		name        string
		input       domain.CreateTrip
		failure     string
		wantErr     error
		wantField   string
		wantCreated bool
	}{
		{name: "success", input: input, wantCreated: true},
		{name: "invalid input", input: domain.CreateTrip{DriverID: driverID}, wantErr: domain.ErrInvalidInput, wantField: "user_id"},
		{name: "insert failure", input: input, failure: "insert", wantErr: insertErr},
		{name: "history failure", input: input, failure: "history", wantErr: historyErr},
		{name: "commit failure", input: input, failure: "commit", wantErr: commitErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := NewMocktripRepository(ctrl)
			transactions := NewMocktxManager(ctrl)
			ctx := context.WithValue(context.Background(), transactionKey{}, "request")
			txCtx := context.WithValue(ctx, transactionKey{}, "transaction")
			created := 0
			committed := false
			calls := 0
			uc := New(repository, transactions, func() time.Time {
				calls++
				return at
			}, func() uuid.UUID {
				calls++
				return id
			}, func(hookCtx context.Context) {
				if hookCtx != ctx {
					t.Error("hook received transaction context rather than request context")
				}
				if !committed {
					t.Error("hook ran before commit")
				}
				created++
			})

			if tt.wantErr != domain.ErrInvalidInput {
				transactions.EXPECT().Do(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, fn func(context.Context) error) error {
					if err := fn(txCtx); err != nil {
						return err
					}
					if tt.failure == "commit" {
						return commitErr
					}
					committed = true
					return nil
				})
				// Expectations within Do check that both writes use the transaction context.
				repository.EXPECT().Insert(txCtx, want).Return(func() error {
					if tt.failure == "insert" {
						return insertErr
					}
					return nil
				}())
				if tt.failure != "insert" {
					repository.EXPECT().InsertStatusHistory(txCtx, id, nil, domain.TripStatusActive, "trip created").Return(func() error {
						if tt.failure == "history" {
							return historyErr
						}
						return nil
					}())
				}
			}

			got, err := uc.Execute(ctx, tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Execute() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
				}
				if got != (domain.Trip{}) {
					t.Errorf("Execute() trip = %+v, want zero value", got)
				}
			} else if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Execute() = (%+v, %v), want (%+v, nil)", got, err, want)
			}
			if tt.wantField != "" {
				var fieldErr *domain.FieldError
				if !errors.As(err, &fieldErr) || fieldErr.Field != tt.wantField {
					t.Errorf("Execute() error = %v, want field %s", err, tt.wantField)
				}
			}
			if created != boolToInt(tt.wantCreated) {
				t.Errorf("hook calls = %d, want %t", created, tt.wantCreated)
			}
			wantCalls := 2
			if tt.wantField != "" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Errorf("clock and ID calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestNewUsesDefaultClockAndID(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := NewMocktripRepository(ctrl)
	transactions := NewMocktxManager(ctrl)
	uc := New(repository, transactions, nil, nil, nil)
	if uc.now == nil || uc.newID == nil {
		t.Fatal("nil defaults for clock or ID generator")
	}
	if uc.newID() == uuid.Nil || uc.now().IsZero() {
		t.Fatal("invalid clock or ID defaults")
	}
}
