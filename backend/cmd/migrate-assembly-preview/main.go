package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"log"
	"os"
	"time"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("failed to load API settings")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("failed to connect using API settings")
	}
	defer conn.Close(context.Background())
	for _, migration := range []struct{ table, file string }{
		{"assembly_preview_drafts", "migrations/011_assembly_preview_drafts.sql"},
		{"assembly_problem_records", "migrations/012_assembly_problem_storage.sql"},
		{"assembly_problem_contexts", "migrations/013_assembly_problem_context.sql"},
	} {
		var applied bool
		if err = conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, migration.table).Scan(&applied); err != nil {
			log.Fatal(err)
		}
		if applied {
			continue
		}
		data, err := os.ReadFile(migration.file)
		if err != nil {
			log.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(data)); err != nil {
			log.Fatal(err)
		}
	}
	log.Println("normalized assembly problem storage ready; warehouse stock unchanged")
}
