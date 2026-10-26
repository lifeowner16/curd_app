package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Item struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Completed   bool   `json:"completed"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Input struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Completed   bool   `json:"completed"`
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `
		PRAGMA busy_timeout = 5000;
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
			description TEXT NOT NULL DEFAULT '',
			completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0, 1)),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`)
	if err != nil {
		closeErr := db.Close()
		return nil, fmt.Errorf("initialize sqlite: %w (close: %v)", err, closeErr)
	}
	return &Store{db: db}, nil
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) Ping(ctx context.Context) error {
	return store.db.PingContext(ctx)
}

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var item Item
	err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Completed, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (store *Store) Create(ctx context.Context, input Input) (Item, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return scanItem(store.db.QueryRowContext(ctx, `
		INSERT INTO items (name, description, completed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, name, description, completed, created_at, updated_at`,
		input.Name, input.Description, input.Completed, now, now))
}

func (store *Store) Get(ctx context.Context, id int64) (Item, error) {
	return scanItem(store.db.QueryRowContext(ctx, `
		SELECT id, name, description, completed, created_at, updated_at FROM items WHERE id = ?`, id))
}

func (store *Store) List(ctx context.Context, limit, offset int) ([]Item, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, name, description, completed, created_at, updated_at
		FROM items ORDER BY id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]Item, 0)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *Store) Update(ctx context.Context, id int64, input Input) (Item, error) {
	return scanItem(store.db.QueryRowContext(ctx, `
		UPDATE items SET name = ?, description = ?, completed = ?, updated_at = ? WHERE id = ?
		RETURNING id, name, description, completed, created_at, updated_at`,
		input.Name, input.Description, input.Completed, time.Now().UTC().Format(time.RFC3339Nano), id))
}

func (store *Store) Delete(ctx context.Context, id int64) error {
	result, err := store.db.ExecContext(ctx, "DELETE FROM items WHERE id = ?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}
