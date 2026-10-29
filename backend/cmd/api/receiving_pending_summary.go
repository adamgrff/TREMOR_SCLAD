package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

func receivingPendingSummaryHandler(database *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var response struct {
		PendingQuantity int64 `json:"pendingQuantity"`
	}
	err := database.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(items.quantity), 0)::bigint
		FROM receiving_session_items AS items
		JOIN receiving_sessions AS sessions ON sessions.id = items.session_id
		WHERE sessions.status = 'active'
	`).Scan(&response.PendingQuantity)
	if err != nil {
		log.Printf("failed to load receiving pending summary: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load receiving pending summary")
		return
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode receiving pending summary: %v", err)
	}
}
