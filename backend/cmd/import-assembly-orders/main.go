package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"io"
	"log"
	"os"
	"time"
)

func main() {
	path := flag.String("file", "", "local normalized real orders JSON file")
	flag.Parse()
	if *path == "" {
		log.Fatal("use -file with a trusted normalized orders file")
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("cannot read local backend configuration")
	}
	file, err := os.Open(*path)
	if err != nil {
		log.Fatal("cannot read import file")
	}
	defer file.Close()
	var inputs []assemblyorders.Import
	dec := json.NewDecoder(io.LimitReader(file, 8<<20))
	dec.DisallowUnknownFields()
	if dec.Decode(&inputs) != nil || dec.Decode(new(any)) != io.EOF {
		log.Fatal("invalid normalized orders JSON")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("cannot open local database")
	}
	defer db.Close()
	for _, input := range inputs {
		if err = assemblyorders.ImportToDB(ctx, db, input); err != nil {
			log.Fatalf("import rejected: %v", err)
		}
	}
	log.Printf("Imported %d orders into existing assembly storage", len(inputs))
}
