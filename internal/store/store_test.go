package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCRUDAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	created, err := database.Create(ctx, Input{Name: "First", Description: "Persistent item"})
	if err != nil || created.ID <= 0 || created.CreatedAt == "" {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	found, err := database.Get(ctx, created.ID)
	if err != nil || found != created {
		t.Fatalf("persistent get: %+v, %v", found, err)
	}
	updated, err := database.Update(ctx, created.ID, Input{Name: "Updated", Completed: true})
	if err != nil || !updated.Completed || updated.Name != "Updated" || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("update: %+v, %v", updated, err)
	}
	items, err := database.List(ctx, 10, 0)
	if err != nil || len(items) != 1 || items[0] != updated {
		t.Fatalf("list: %+v, %v", items, err)
	}
	if err := database.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]func() error{
		"get":    func() error { _, err := database.Get(ctx, created.ID); return err },
		"update": func() error { _, err := database.Update(ctx, created.ID, Input{Name: "Missing"}); return err },
		"delete": func() error { return database.Delete(ctx, created.ID) },
	} {
		t.Run(name+" missing", func(t *testing.T) {
			if err := operation(); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("expected sql.ErrNoRows, got %v", err)
			}
		})
	}
	items, err = database.List(ctx, 10, 0)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list: %+v, %v", items, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.Create(canceled, Input{Name: "Canceled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
