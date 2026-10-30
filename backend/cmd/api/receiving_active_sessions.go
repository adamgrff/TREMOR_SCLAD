package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type activeReceivingSession struct {
	ID              int64     `json:"id"`
	StartedAt       time.Time `json:"startedAt"`
	PendingQuantity int64     `json:"pendingQuantity"`
	PlacementCount  int64     `json:"placementCount"`
}

// One SELECT gives the list and all summaries a single statement snapshot.
func activeReceivingSessionsHandler(database *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	rows, err := database.Query(r.Context(), `SELECT s.id,s.started_at,
 COALESCE((SELECT sum(i.quantity) FROM receiving_session_items i WHERE i.session_id=s.id),0),
 (SELECT count(*) FROM receiving_placements p WHERE p.receiving_session_id=s.id AND p.status='active')
 FROM receiving_sessions s WHERE s.status='active' AND s.deleted_at IS NULL ORDER BY s.started_at,s.id`)
	if err != nil {
		writeErrorResponse(w, 500, "failed to list active receiving sessions")
		return
	}
	defer rows.Close()
	result := make([]activeReceivingSession, 0)
	for rows.Next() {
		var item activeReceivingSession
		if err := rows.Scan(&item.ID, &item.StartedAt, &item.PendingQuantity, &item.PlacementCount); err != nil {
			writeErrorResponse(w, 500, "failed to read active receiving sessions")
			return
		}
		result = append(result, item)
	}
	if rows.Err() != nil {
		writeErrorResponse(w, 500, "failed to read active receiving sessions")
		return
	}
	json.NewEncoder(w).Encode(result)
}
