package gcadapter

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/deployment/gc"
)

type statOnlyPoolStore struct {
	object gc.Object
	err    error
}

func (s statOnlyPoolStore) Open(context.Context, string) (gc.CatalogObject, error) {
	return gc.CatalogObject{}, errors.New("not implemented")
}
func (s statOnlyPoolStore) ListPoolObjects(context.Context, string) ([]gc.Object, error) {
	return nil, errors.New("not implemented")
}
func (s statOnlyPoolStore) DeleteConditional(context.Context, gc.DeleteRequest) (gc.DeleteResponse, error) {
	return gc.DeleteResponse{}, errors.New("not implemented")
}
func (s statOnlyPoolStore) Stat(context.Context, string, string) (gc.Object, error) {
	return s.object, s.err
}

func TestVerifyReferencedObjectRequiresImmutableExistingDigest(t *testing.T) {
	if err := verifyReferencedObject(context.Background(), statOnlyPoolStore{err: os.ErrNotExist}, "pool", "data/file.parquet"); err == nil {
		t.Fatal("missing referenced object was accepted")
	}
	if err := verifyReferencedObject(context.Background(), statOnlyPoolStore{object: gc.Object{Key: "data/file.parquet"}}, "pool", "data/file.parquet"); err == nil {
		t.Fatal("referenced object without digest was accepted")
	}
	if err := verifyReferencedObject(context.Background(), statOnlyPoolStore{object: gc.Object{Key: "data/file.parquet", Digest: "sha256:test"}}, "pool", "data/file.parquet"); err != nil {
		t.Fatalf("immutable referenced object rejected: %v", err)
	}
}
