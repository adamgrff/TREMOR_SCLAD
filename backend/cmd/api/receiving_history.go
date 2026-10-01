package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
