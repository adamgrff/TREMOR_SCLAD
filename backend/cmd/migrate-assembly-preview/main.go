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
	var applied bool
	if err = conn.QueryRow(ctx, `SELECT to_regclass('assembly_preview_drafts') IS NOT NULL`).Scan(&applied); err != nil {
		log.Fatal(err)
	}
	if !applied {
		data, err := os.ReadFile("migrations/011_assembly_preview_drafts.sql")
		if err != nil {
			log.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(data)); err != nil {
			log.Fatal(err)
		}
	}
	log.Println("assembly preview persistence ready; warehouse stock unchanged")
}
