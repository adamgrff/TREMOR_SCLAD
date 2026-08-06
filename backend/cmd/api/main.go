package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type healthResponse struct {
	Status string `json:"status"`
}

type cellItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatalf("failed to load .env: %v", err)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	database, err := pgxpool.New(
		context.Background(),
		databaseURL,
	)
	if err != nil {
		log.Fatalf("failed to create database pool: %v", err)
	}
	defer database.Close()

	pingContext, cancelPing := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelPing()

	if err := database.Ping(pingContext); err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	log.Println("Database connection established")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)

	mux.HandleFunc(
		"GET /api/cells/{cellName}",
		func(w http.ResponseWriter, r *http.Request) {
			cellHandler(database, w, r)
		},
	)

	server := &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           corsMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("API is running on http://127.0.0.1:8080")

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func healthHandler(
	w http.ResponseWriter,
	_ *http.Request,
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	response := healthResponse{
		Status: "ok",
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf(
			"failed to encode health response: %v",
			err,
		)
	}
}

func cellHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	cellName := r.PathValue("cellName")

	var cellExists bool

	err := database.QueryRow(
		r.Context(),
		`
			SELECT EXISTS (
				SELECT 1
				FROM cells
				WHERE code = $1
			)
		`,
		cellName,
	).Scan(&cellExists)

	if err != nil {
		log.Printf(
			"failed to check cell %s: %v",
			cellName,
			err,
		)

		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load cell",
		)
		return
	}

	if !cellExists {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"cell not found",
		)
		return
	}

	rows, err := database.Query(
		r.Context(),
		`
			SELECT
				products.id::text,
				products.name,
				cell_stock.quantity
			FROM cells
			JOIN cell_stock
				ON cell_stock.cell_id = cells.id
			JOIN products
				ON products.id = cell_stock.product_id
			WHERE cells.code = $1
			ORDER BY products.name
		`,
		cellName,
	)
	if err != nil {
		log.Printf(
			"failed to load cell %s: %v",
			cellName,
			err,
		)

		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load cell",
		)
		return
	}
	defer rows.Close()

	items := make([]cellItem, 0)

	for rows.Next() {
		var item cellItem

		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.Quantity,
		); err != nil {
			log.Printf(
				"failed to scan item for cell %s: %v",
				cellName,
				err,
			)

			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to load cell",
			)
			return
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		log.Printf(
			"failed to read items for cell %s: %v",
			cellName,
			err,
		)

		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load cell",
		)
		return
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		log.Printf(
			"failed to encode cell response: %v",
			err,
		)
	}
}

func writeErrorResponse(
	w http.ResponseWriter,
	statusCode int,
	message string,
) {
	w.WriteHeader(statusCode)

	response := errorResponse{
		Error: message,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf(
			"failed to encode error response: %v",
			err,
		)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if origin == "http://127.0.0.1:5173" ||
				origin == "http://localhost:5173" {
				w.Header().Set(
					"Access-Control-Allow-Origin",
					origin,
				)
				w.Header().Set("Vary", "Origin")
			}

			w.Header().Set(
				"Access-Control-Allow-Methods",
				"GET, POST, PUT, PATCH, DELETE, OPTIONS",
			)
			w.Header().Set(
				"Access-Control-Allow-Headers",
				"Content-Type, Authorization",
			)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		},
	)
}
