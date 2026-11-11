package main

import (
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/readiness"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
)

func apiRoutes(database *pgxpool.Pool, mode string, gate *readiness.Client) http.Handler {
	mux := &modeMux{ServeMux: http.NewServeMux(), mode: mode, gate: gate}

	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /api/deployment", func(w http.ResponseWriter, r *http.Request) { deploymentIdentityHandler(database, w, r) })
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
	mux.HandleFunc("GET /api/home/history", func(w http.ResponseWriter, r *http.Request) {
		homeHistoryHandler(database, w, r)
	})
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
		"GET /api/receiving/sessions/active",
		func(w http.ResponseWriter, r *http.Request) { activeReceivingSessionsHandler(database, w, r) },
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

	return mux
}
