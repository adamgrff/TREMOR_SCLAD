package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type finishReceivingRequest struct {
	SessionID    int64   `json:"sessionId"`
	PlacementIDs []int64 `json:"placementIds"`
}

type finishReceivingResponse struct {
	ID             int64 `json:"id"`
	PlacementCount int   `json:"placementCount"`
}

func finishReceivingHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var request finishReceivingRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if request.SessionID <= 0 || len(request.PlacementIDs) == 0 {
		writeErrorResponse(
			w,
			http.StatusBadRequest,
			"sessionId and at least one placement are required",
		)
		return
	}

	seenIDs := make(map[int64]struct{}, len(request.PlacementIDs))
	for _, id := range request.PlacementIDs {
		if id <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, "placement IDs must be positive and unique")
			return
		}
		if _, exists := seenIDs[id]; exists {
			writeErrorResponse(w, http.StatusBadRequest, "placement IDs must be positive and unique")
			return
		}
		seenIDs[id] = struct{}{}
	}
	sort.Slice(request.PlacementIDs, func(i, j int) bool {
		return request.PlacementIDs[i] < request.PlacementIDs[j]
	})

	tx, err := database.Begin(r.Context())
	if err != nil {
		log.Printf("failed to start receiving completion: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}
	defer tx.Rollback(r.Context())

	err = lockActiveReceivingSession(r.Context(), tx, request.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "receiving session not found")
		return
	}
	if errors.Is(err, errReceivingSessionNotActive) {
		writeErrorResponse(w, http.StatusConflict, "receiving session is already completed")
		return
	}
	if err != nil {
		log.Printf("failed to lock receiving session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}

	var hasPendingItems bool
	err = tx.QueryRow(
		r.Context(),
		`SELECT EXISTS (SELECT 1 FROM receiving_session_items WHERE session_id = $1)`,
		request.SessionID,
	).Scan(&hasPendingItems)
	if err != nil {
		log.Printf("failed to check pending items for session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}
	if hasPendingItems {
		writeErrorResponse(w, http.StatusConflict, "place or clear pending items before finishing")
		return
	}

	rows, err := tx.Query(
		r.Context(),
		`
			SELECT id
			FROM receiving_placements
			WHERE receiving_session_id = $1
				AND status = 'active'
			ORDER BY id
			FOR UPDATE
		`,
		request.SessionID,
	)
	if err != nil {
		log.Printf("failed to lock placements for session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}

	activePlacementIDs := make([]int64, 0, len(request.PlacementIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("failed to read placement ID: %v", err)
			writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
			return
		}
		activePlacementIDs = append(activePlacementIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("failed to read placements for session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}
	rows.Close()

	if len(activePlacementIDs) != len(request.PlacementIDs) {
		writeErrorResponse(w, http.StatusConflict, "placements changed; reload the receiving session")
		return
	}
	for index, id := range activePlacementIDs {
		if id != request.PlacementIDs[index] {
			writeErrorResponse(w, http.StatusConflict, "placements changed; reload the receiving session")
			return
		}
	}

	result, err := tx.Exec(
		r.Context(),
		`
			UPDATE receiving_sessions
			SET status = 'completed', completed_at = NOW()
			WHERE id = $1 AND status = 'active'
		`,
		request.SessionID,
	)
	if err != nil {
		log.Printf("failed to complete receiving session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}
	if result.RowsAffected() != 1 {
		writeErrorResponse(w, http.StatusConflict, "receiving session changed while finishing")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("failed to commit receiving completion: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to finish receiving")
		return
	}

	response := finishReceivingResponse{
		ID:             request.SessionID,
		PlacementCount: len(activePlacementIDs),
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode receiving completion response: %v", err)
	}
}
