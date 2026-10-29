package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type healthResponse struct {
	Status string `json:"status"`
}

type cellItem struct {
	ID       string `json:"id"`
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type productItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SKU  string `json:"sku"`
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
	mux.HandleFunc("GET /api/assembly/orders", func(w http.ResponseWriter, r *http.Request) { assemblyOrdersHandler(database, w, r) })
	mux.HandleFunc("GET /api/assembly/mapping-products", func(w http.ResponseWriter, r *http.Request) { assemblyMappingProductsHandler(database, w, r) })
	mux.HandleFunc("POST /api/assembly/problems/{problemID}/map-product", func(w http.ResponseWriter, r *http.Request) { assemblyMappingHandler(database, w, r) })
	for _, action := range []string{"start", "scan", "finish", "pause", "undo-return"} {
		mux.HandleFunc("POST /api/assembly/disassembly/{sessionID}/"+action, func(w http.ResponseWriter, r *http.Request) {
			disassemblyHandler(database, w, r, "disassembly-"+action)
		})
	}
	mux.HandleFunc("GET /api/ozon/status", ozonConnectionHandler)
	for _, action := range []string{"start", "scan", "undo", "finish"} {
		mux.HandleFunc("POST /api/assembly/"+action, func(w http.ResponseWriter, r *http.Request) { warehouseAssemblyHandler(database, w, r, action) })
	}
	mux.HandleFunc("GET /api/assembly-preview/draft", func(w http.ResponseWriter, r *http.Request) { getAssemblyDraftHandler(database, w, r) })
	mux.HandleFunc("GET /api/assembly/problems", func(w http.ResponseWriter, r *http.Request) { assemblyProblemsHandler(database, w, r) })
	mux.HandleFunc("GET /api/assembly/problems/archive", func(w http.ResponseWriter, r *http.Request) { assemblyProblemArchiveHandler(database, w, r) })
	mux.HandleFunc("GET /api/assembly/problems/{problemID}/history", func(w http.ResponseWriter, r *http.Request) { assemblyProblemHistoryHandler(database, w, r) })
	mux.HandleFunc("POST /api/assembly/problems", func(w http.ResponseWriter, r *http.Request) { createAssemblyProblemHandler(database, w, r) })
	mux.HandleFunc("POST /api/assembly/problems/{problemID}/check-stock", func(w http.ResponseWriter, r *http.Request) {
		assemblyProblemOperationHandler(database, w, r, "check-stock")
	})
	mux.HandleFunc("POST /api/assembly/problems/{problemID}/return", func(w http.ResponseWriter, r *http.Request) {
		assemblyProblemOperationHandler(database, w, r, "return")
	})
	for _, action := range []string{"retry-ozon", "close-cancelled", "transfer-disassembly"} {
		mux.HandleFunc("POST /api/assembly/problems/{problemID}/"+action, func(w http.ResponseWriter, r *http.Request) { assemblyProblemOperationHandler(database, w, r, action) })
	}
	mux.HandleFunc("GET /api/assembly/problems/{problemID}", func(w http.ResponseWriter, r *http.Request) { assemblyProblemHandler(database, w, r) })
	mux.HandleFunc("PUT /api/assembly-preview/draft", func(w http.ResponseWriter, r *http.Request) { saveAssemblyDraftHandler(database, w, r) })
	mux.HandleFunc("POST /api/racks", func(w http.ResponseWriter, r *http.Request) { createRackHandler(database, w, r) })
	mux.HandleFunc("GET /api/racks", func(w http.ResponseWriter, r *http.Request) { racksHandler(database, w, r) })
	mux.HandleFunc("GET /api/home/statistics", func(w http.ResponseWriter, r *http.Request) {
		homeStatisticsHandler(database, w, r)
	})
	mux.HandleFunc("GET /api/receiving/pending-summary", func(w http.ResponseWriter, r *http.Request) {
		receivingPendingSummaryHandler(database, w, r)
	})
	mux.HandleFunc("POST /api/receiving/sessions/{sessionID}/quantities/{productID}", func(w http.ResponseWriter, r *http.Request) { receivingQuantityHandler(database, w, r) })
	mux.HandleFunc("PUT /api/receiving/sessions/{sessionID}/quantities/{productID}", func(w http.ResponseWriter, r *http.Request) { receivingQuantityHandler(database, w, r) })
	mux.HandleFunc("POST /api/product-catalog", func(w http.ResponseWriter, r *http.Request) { saveCatalogProductHandler(database, w, r) })
	mux.HandleFunc("PUT /api/product-catalog/{productID}", func(w http.ResponseWriter, r *http.Request) { saveCatalogProductHandler(database, w, r) })
	mux.HandleFunc("GET /api/product-catalog", func(w http.ResponseWriter, r *http.Request) {
		productCatalogHandler(database, w, r)
	})

	mux.HandleFunc(
		"GET /api/cells/{cellName}",
		func(w http.ResponseWriter, r *http.Request) {
			cellHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"GET /api/products/{sku}",
		func(w http.ResponseWriter, r *http.Request) {
			productHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"POST /api/receiving/placements",
		func(w http.ResponseWriter, r *http.Request) {
			createReceivingPlacementHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"POST /api/receiving/placements/{placementID}/undo",
		func(w http.ResponseWriter, r *http.Request) {
			undoReceivingPlacementHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"POST /api/receiving/finish",
		func(w http.ResponseWriter, r *http.Request) {
			finishReceivingHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"GET /api/receiving/sessions/latest",
		func(w http.ResponseWriter, r *http.Request) {
			latestReceivingHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"GET /api/receiving/sessions",
		func(w http.ResponseWriter, r *http.Request) {
			completedReceivingHistoryHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"POST /api/receiving/sessions",
		func(w http.ResponseWriter, r *http.Request) {
			createReceivingSessionHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"GET /api/receiving/sessions/{sessionID}",
		func(w http.ResponseWriter, r *http.Request) {
			getReceivingSessionHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"DELETE /api/receiving/sessions/{sessionID}",
		func(w http.ResponseWriter, r *http.Request) {
			deleteReceivingHistoryHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"POST /api/receiving/sessions/{sessionID}/items",
		func(w http.ResponseWriter, r *http.Request) {
			addReceivingSessionItemHandler(database, w, r)
		},
	)

	mux.HandleFunc(
		"DELETE /api/receiving/sessions/{sessionID}/items/{sku}",
		func(w http.ResponseWriter, r *http.Request) {
			deleteReceivingSessionItemHandler(database, w, r)
		},
	)

	mux.HandleFunc("GET /api/products/{sku}/stock", func(w http.ResponseWriter, r *http.Request) {
		productStockHandler(database, w, r)
	})

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
	categoryID := r.URL.Query().Get("categoryId")

	var cellExists bool

	err := database.QueryRow(
		r.Context(),
		`
			SELECT EXISTS (
				SELECT 1
				FROM cells
				WHERE code = $1 AND ($2 = '' OR category_id::text = $2)
			)
		`,
		cellName,
		categoryID,
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
				products.sku,
				cell_stock.quantity
			FROM cells
			JOIN cell_stock
				ON cell_stock.cell_id = cells.id
			JOIN products
				ON products.id = cell_stock.product_id
			WHERE cells.code = $1 AND ($2 = '' OR cells.category_id::text = $2)
			AND NOT products.archived AND cell_stock.quantity > 0
			ORDER BY products.name
		`,
		cellName,
		categoryID,
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
			&item.SKU,
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

func productHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	sku := r.PathValue("sku")

	var item productItem

	err := database.QueryRow(
		r.Context(),
		`
			SELECT
				id::text,
				name,
				sku
			FROM products
			WHERE sku = $1 AND NOT archived
		`,
		sku,
	).Scan(
		&item.ID,
		&item.Name,
		&item.SKU,
	)

	if err == pgx.ErrNoRows {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"product not found",
		)
		return
	}

	if err != nil {
		log.Printf("failed to load product %s: %v", sku, err)

		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load product",
		)
		return
	}

	if err := json.NewEncoder(w).Encode(item); err != nil {
		log.Printf("failed to encode product response: %v", err)
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
