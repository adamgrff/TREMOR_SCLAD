package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("failed to load .env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("failed to connect using API settings")
	}
	defer conn.Close(context.Background())
	var applied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='products' AND column_name='archived')`).Scan(&applied); err != nil {
		log.Fatal(err)
	}
	if !applied {
		data, err := os.ReadFile("migrations/008_product_catalog.sql")
		if err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			log.Fatal(err)
		}
	}
	var quantitiesApplied bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('receiving_quantity_requests') IS NOT NULL`).Scan(&quantitiesApplied); err != nil {
		log.Fatal(err)
	}
	if !quantitiesApplied {
		data, err := os.ReadFile("migrations/009_receiving_quantity_requests.sql")
		if err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			log.Fatal(err)
		}
	}
	var racksApplied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='cells'::regclass AND conname='cells_rack_code_unique')`).Scan(&racksApplied); err != nil {
		log.Fatal(err)
	}
	if !racksApplied {
		data, err := os.ReadFile("migrations/010_rack_cell_codes.sql")
		if err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			log.Fatal(err)
		}
	}
	log.Println("catalog and rack migrations applied; existing stock unchanged")
}
