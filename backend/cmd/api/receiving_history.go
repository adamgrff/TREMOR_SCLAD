package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type completedReceivingSummary struct {
	ID             int64     `json:"id"`
	CompletedAt    time.Time `json:"completedAt"`
	PlacementCount int64     `json:"placementCount"`
	ProductCount   int64     `json:"productCount"`
	CellCount      int64     `json:"cellCount"`
	Quantity       int64     `json:"quantity"`
}

type completedReceivingHistoryResponse struct {
	Sessions   []completedReceivingSummary `json:"sessions"`
	NextCursor *int64                      `json:"nextCursor"`
}

func completedReceivingHistoryHandler(database *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var cursor int64
	if value := r.URL.Query().Get("before"); value != "" {
		var err error
		cursor, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cursor <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, "before must be a positive session ID")
			return
		}
	}
	rows, err := database.Query(r.Context(), `
		WITH page AS (
			SELECT id, completed_at FROM receiving_sessions
			WHERE status = 'completed'
				AND ($1::bigint = 0 OR (completed_at, id) < (
					SELECT completed_at, id FROM receiving_sessions
					WHERE id = $1 AND status = 'completed'
				))
			ORDER BY completed_at DESC, id DESC
			LIMIT 21
		)
		SELECT page.id, page.completed_at,
			COUNT(DISTINCT placements.id), COUNT(DISTINCT items.product_id),
			COUNT(DISTINCT placements.cell_id), COALESCE(SUM(items.quantity), 0)::bigint
		FROM page
		LEFT JOIN receiving_placements AS placements
			ON placements.receiving_session_id = page.id AND placements.status = 'active'
		LEFT JOIN receiving_placement_items AS items ON items.placement_id = placements.id
		GROUP BY page.id, page.completed_at
		ORDER BY page.completed_at DESC, page.id DESC
	`, cursor)
	if err != nil {
		log.Printf("failed to load receiving history: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load receiving history")
		return
	}
	defer rows.Close()
	response := completedReceivingHistoryResponse{Sessions: make([]completedReceivingSummary, 0)}
	for rows.Next() {
		var session completedReceivingSummary
		if err := rows.Scan(&session.ID, &session.CompletedAt, &session.PlacementCount, &session.ProductCount, &session.CellCount, &session.Quantity); err != nil {
			log.Printf("failed to read receiving history: %v", err)
			writeErrorResponse(w, http.StatusInternalServerError, "failed to load receiving history")
			return
		}
		response.Sessions = append(response.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		log.Printf("failed to read receiving history rows: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load receiving history")
		return
	}
	if len(response.Sessions) > 20 {
		response.Sessions = response.Sessions[:20]
		id := response.Sessions[19].ID
		response.NextCursor = &id
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode receiving history: %v", err)
	}
}

type latestReceivingItem struct {
	PlacementID int64     `json:"placementId"`
	CellCode    string    `json:"cellCode"`
	CreatedAt   time.Time `json:"createdAt"`
	Name        string    `json:"name"`
	SKU         string    `json:"sku"`
	Quantity    int       `json:"quantity"`
}

type latestReceivingResponse struct {
	ID          int64                 `json:"id"`
	CompletedAt time.Time             `json:"completedAt"`
	Items       []latestReceivingItem `json:"items"`
}

func latestReceivingHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	rows, err := database.Query(
		r.Context(),
		`
			SELECT
				sessions.id,
				sessions.completed_at,
				placements.id,
				cells.code,
				placements.created_at,
				products.name,
				products.sku,
				placement_items.quantity
			FROM receiving_sessions AS sessions
			JOIN receiving_placements AS placements
				ON placements.receiving_session_id = sessions.id
				AND placements.status = 'active'
			JOIN cells
				ON cells.id = placements.cell_id
			JOIN receiving_placement_items AS placement_items
				ON placement_items.placement_id = placements.id
			JOIN products
				ON products.id = placement_items.product_id
			WHERE sessions.id = (
				SELECT id
				FROM receiving_sessions
				WHERE status = 'completed'
				ORDER BY completed_at DESC, id DESC
				LIMIT 1
			)
			ORDER BY placements.created_at, placements.id, products.name
		`,
	)
	if err != nil {
		log.Printf("failed to load latest receiving: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load latest receiving",
		)
		return
	}
	defer rows.Close()

	var response *latestReceivingResponse

	for rows.Next() {
		if response == nil {
			response = &latestReceivingResponse{
				Items: make([]latestReceivingItem, 0),
			}
		}

		var item latestReceivingItem

		if err := rows.Scan(
			&response.ID,
			&response.CompletedAt,
			&item.PlacementID,
			&item.CellCode,
			&item.CreatedAt,
			&item.Name,
			&item.SKU,
			&item.Quantity,
		); err != nil {
			log.Printf("failed to read latest receiving item: %v", err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to load latest receiving",
			)
			return
		}

		response.Items = append(response.Items, item)
	}

	if err := rows.Err(); err != nil {
		log.Printf("failed to read latest receiving rows: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load latest receiving",
		)
		return
	}

	if response == nil {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"completed receiving not found",
		)
		return
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode latest receiving response: %v", err)
	}
}
