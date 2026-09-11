package database

import (
	"context"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"io/fs"
)

func Provider(url string) (*goose.Provider, *sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, nil, err
	}
	sub, err := fs.Sub(Migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return p, db, nil
}
func Up(ctx context.Context, url string) error {
	p, db, e := Provider(url)
	if e != nil {
		return e
	}
	defer db.Close()
	_, e = p.Up(ctx)
	return e
}
