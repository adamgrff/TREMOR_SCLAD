// Applies only the receiving-history deletion migration using the API's .env settings.
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
		log.Fatal("failed to connect to database using API settings")
	}
	defer conn.Close(context.Background())
	var applied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'receiving_sessions' AND column_name = 'deleted_at'
	)`).Scan(&applied); err != nil {
		log.Fatal(err)
	}
	if applied {
		log.Println("receiving history deletion migration already applied")
		return
	}
	migration, err := os.ReadFile("migrations/007_receiving_history_deletion.sql")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(migration)); err != nil {
		log.Fatal(err)
	}
	log.Println("receiving history deletion migration applied; stock and placements unchanged")
}
