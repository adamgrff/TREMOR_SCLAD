package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errReceivingSessionNotActive = errors.New("receiving session is not active")

type createReceivingSessionResponse struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

type receivingSessionPendingItem struct {
	Name     string `json:"name"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type receivingSessionPlacedItem struct {
	PlacementID int64     `json:"placementId"`
	CellCode    string    `json:"cellCode"`
	CreatedAt   time.Time `json:"createdAt"`
	Name        string    `json:"name"`
	SKU         string    `json:"sku"`
	Quantity    int       `json:"quantity"`
}

type receivingSessionDetailsResponse struct {
	ID           int64                         `json:"id"`
	Status       string                        `json:"status"`
	StartedAt    time.Time                     `json:"startedAt"`
	CompletedAt  *time.Time                    `json:"completedAt"`
	PendingItems []receivingSessionPendingItem `json:"pendingItems"`
	PlacedItems  []receivingSessionPlacedItem  `json:"placedItems"`
}

type addReceivingSessionItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type receivingSessionStatusResponse struct {
	Status string `json:"status"`
}

func parseReceivingSessionID(r *http.Request) (int64, error) {
	sessionID, err := strconv.ParseInt(r.PathValue("sessionID"), 10, 64)
	if err != nil || sessionID <= 0 {
		return 0, errors.New("invalid receiving session ID")
	}

	return sessionID, nil
}

func lockActiveReceivingSession(
	ctx context.Context,
	tx pgx.Tx,
	sessionID int64,
) error {
	var status string

	err := tx.QueryRow(
		ctx,
		`
			SELECT status
			FROM receiving_sessions
			WHERE id = $1
			FOR UPDATE
		`,
		sessionID,
	).Scan(&status)
	if err != nil {
		return err
	}

	if status != "active" {
		return errReceivingSessionNotActive
	}

	return nil
}

func createReceivingSessionHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var response createReceivingSessionResponse

	err := database.QueryRow(
		r.Context(),
		`
			INSERT INTO receiving_sessions (status, completed_at)
			VALUES ('active', NULL)
			RETURNING id, status, started_at
		`,
	).Scan(&response.ID, &response.Status, &response.StartedAt)
	if err != nil {
		log.Printf("failed to create receiving session: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to create receiving session",
		)
		return
	}

	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode receiving session response: %v", err)
	}
}

func getReceivingSessionHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	sessionID, err := parseReceivingSessionID(r)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var response receivingSessionDetailsResponse
	var completedAt sql.NullTime

	err = database.QueryRow(
		r.Context(),
		`
			SELECT id, status, started_at, completed_at
			FROM receiving_sessions
			WHERE id = $1 AND deleted_at IS NULL
		`,
		sessionID,
	).Scan(
		&response.ID,
		&response.Status,
		&response.StartedAt,
		&completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"receiving session not found",
		)
		return
	}
	if err != nil {
		log.Printf("failed to load receiving session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load receiving session",
		)
		return
	}

	if completedAt.Valid {
		response.CompletedAt = &completedAt.Time
	}

	response.PendingItems = make([]receivingSessionPendingItem, 0)
	pendingRows, err := database.Query(
		r.Context(),
		`
			SELECT products.name, products.sku, session_items.quantity
			FROM receiving_session_items AS session_items
			JOIN products
				ON products.id = session_items.product_id
			WHERE session_items.session_id = $1
			ORDER BY products.name
		`,
		sessionID,
	)
	if err != nil {
		log.Printf("failed to load pending items for session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load receiving session",
		)
		return
	}

	for pendingRows.Next() {
		var item receivingSessionPendingItem

		if err := pendingRows.Scan(&item.Name, &item.SKU, &item.Quantity); err != nil {
			pendingRows.Close()
			log.Printf("failed to read pending item: %v", err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to load receiving session",
			)
			return
		}

		response.PendingItems = append(response.PendingItems, item)
	}

	if err := pendingRows.Err(); err != nil {
		pendingRows.Close()
		log.Printf("failed to read pending items: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load receiving session",
		)
		return
	}
	pendingRows.Close()

	response.PlacedItems = make([]receivingSessionPlacedItem, 0)
	placedRows, err := database.Query(
		r.Context(),
		`
			SELECT
				placements.id,
				cells.code,
				placements.created_at,
				products.name,
				products.sku,
				placement_items.quantity
			FROM receiving_placements AS placements
			JOIN cells
				ON cells.id = placements.cell_id
			JOIN receiving_placement_items AS placement_items
				ON placement_items.placement_id = placements.id
			JOIN products
				ON products.id = placement_items.product_id
			WHERE placements.receiving_session_id = $1
				AND placements.status = 'active'
			ORDER BY placements.created_at, placements.id, products.name
		`,
		sessionID,
	)
	if err != nil {
		log.Printf("failed to load placed items for session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load receiving session",
		)
		return
	}

	for placedRows.Next() {
		var item receivingSessionPlacedItem

		if err := placedRows.Scan(
			&item.PlacementID,
			&item.CellCode,
			&item.CreatedAt,
			&item.Name,
			&item.SKU,
			&item.Quantity,
		); err != nil {
			placedRows.Close()
			log.Printf("failed to read placed item: %v", err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to load receiving session",
			)
			return
		}

		response.PlacedItems = append(response.PlacedItems, item)
	}

	if err := placedRows.Err(); err != nil {
		placedRows.Close()
		log.Printf("failed to read placed items: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to load receiving session",
		)
		return
	}
	placedRows.Close()

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode receiving session details: %v", err)
	}
}

func addReceivingSessionItemHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	sessionID, err := parseReceivingSessionID(r)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var request addReceivingSessionItemRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	request.SKU = strings.ToUpper(strings.TrimSpace(request.SKU))
	if request.SKU == "" || request.Quantity <= 0 {
		writeErrorResponse(
			w,
			http.StatusBadRequest,
			"SKU and a positive quantity are required",
		)
		return
	}

	tx, err := database.Begin(r.Context())
	if err != nil {
		log.Printf("failed to start add-item transaction: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}
	defer tx.Rollback(r.Context())

	err = lockActiveReceivingSession(r.Context(), tx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"receiving session not found",
		)
		return
	}
	if errors.Is(err, errReceivingSessionNotActive) {
		writeErrorResponse(
			w,
			http.StatusConflict,
			"receiving session is already completed",
		)
		return
	}
	if err != nil {
		log.Printf("failed to lock receiving session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}

	var productID int64

	err = tx.QueryRow(
		r.Context(),
		`SELECT id FROM products WHERE sku = $1`,
		request.SKU,
	).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		log.Printf("failed to find product %s: %v", request.SKU, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}

	_, err = tx.Exec(
		r.Context(),
		`
			INSERT INTO receiving_session_items
				(session_id, product_id, quantity)
			VALUES ($1, $2, $3)
			ON CONFLICT (session_id, product_id)
			DO UPDATE SET
				quantity = receiving_session_items.quantity + EXCLUDED.quantity
		`,
		sessionID,
		productID,
		request.Quantity,
	)
	if err != nil {
		log.Printf("failed to save pending SKU %s: %v", request.SKU, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("failed to commit pending item: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}

	response := receivingSessionStatusResponse{Status: "saved"}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode pending item response: %v", err)
	}
}

func clearReceivingSessionItemsHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	sessionID, err := parseReceivingSessionID(r)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := database.Begin(r.Context())
	if err != nil {
		log.Printf("failed to start clear-items transaction: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to clear pending items",
		)
		return
	}
	defer tx.Rollback(r.Context())

	err = lockActiveReceivingSession(r.Context(), tx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(
			w,
			http.StatusNotFound,
			"receiving session not found",
		)
		return
	}
	if errors.Is(err, errReceivingSessionNotActive) {
		writeErrorResponse(
			w,
			http.StatusConflict,
			"receiving session is already completed",
		)
		return
	}
	if err != nil {
		log.Printf("failed to lock receiving session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to clear pending items",
		)
		return
	}

	_, err = tx.Exec(
		r.Context(),
		`DELETE FROM receiving_session_items WHERE session_id = $1`,
		sessionID,
	)
	if err != nil {
		log.Printf("failed to clear pending items for session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to clear pending items",
		)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("failed to commit clear-items transaction: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to clear pending items",
		)
		return
	}

	if err := json.NewEncoder(w).Encode(
		receivingSessionStatusResponse{Status: "cleared"},
	); err != nil {
		log.Printf("failed to encode clear-items response: %v", err)
	}
}
