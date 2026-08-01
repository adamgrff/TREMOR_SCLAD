package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
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

var cellContents = map[string][]cellItem{
	"A1": {
		{
			ID:       "a1-product-1",
			Name:     "Ручка TREMOR Classic",
			Quantity: 12,
		},
		{
			ID:       "a1-product-2",
			Name:     "Ручка TREMOR Pro",
			Quantity: 7,
		},
		{
			ID:       "a1-product-3",
			Name:     "Комплект креплений",
			Quantity: 24,
		},
	},

	"A2": {
		{
			ID:       "a2-product-1",
			Name:     "Ручка TREMOR Mini",
			Quantity: 5,
		},
	},

	"A3": {},

	"B1": {
		{
			ID:       "b1-product-1",
			Name:     "Запасные болты",
			Quantity: 42,
		},
	},

	"B2": {},
	"B3": {},
	"C1": {},
	"C2": {},
	"C3": {},
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /api/cells/{cellName}", cellHandler)

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

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	response := healthResponse{
		Status: "ok",
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode health response: %v", err)
	}
}

func cellHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	cellName := r.PathValue("cellName")

	items, exists := cellContents[cellName]
	if !exists {
		w.WriteHeader(http.StatusNotFound)

		response := errorResponse{
			Error: "cell not found",
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("failed to encode error response: %v", err)
		}

		return
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		log.Printf("failed to encode cell response: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin == "http://127.0.0.1:5173" ||
			origin == "http://localhost:5173" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
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
	})
}
