package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/entity/mock"
)

func TestNew_ReturnsEmptyResult(t *testing.T) {
	ex := mock.New()
	got, err := ex.Extract(context.Background(), nil, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entities, want 0", len(got))
	}
}

func TestNewFixed_ReturnsConfiguredEntities(t *testing.T) {
	want := []*entity.Entity{{ID: uuid.New(), Type: "org", Text: "Acme"}}
	ex := mock.NewFixed(want)

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{{Text: "Acme is a company"}}, []entity.Type{"org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "Acme" {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNewError_ReturnsError(t *testing.T) {
	wantErr := errors.New("extraction sidecar unavailable")
	ex := mock.NewError(wantErr)

	_, err := ex.Extract(context.Background(), nil, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v, want %v", err, wantErr)
	}
}
