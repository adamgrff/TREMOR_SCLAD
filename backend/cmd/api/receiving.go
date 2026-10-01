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

type receivingPlacementRequest struct {
	SessionID int64                           `json:"sessionId"`
	CellCode  string                          `json:"cellCode"`
	Items     []receivingPlacementRequestItem `json:"items"`
}

type receivingPlacementRequestItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type receivingPlacementResponse struct {
	ID        int64     `json:"id"`
	CellCode  string    `json:"cellCode"`
	CreatedAt time.Time `json:"createdAt"`
}

type undoPlacementResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type placementStockItem struct {
	productID int64
	quantity  int
}

func validatePendingItems(
	ctx context.Context,
	tx pgx.Tx,
	sessionID int64,
	requestedItems []receivingPlacementRequestItem,
) error {
	rows, err := tx.Query(
		ctx,
		`
			SELECT products.sku, session_items.quantity
			FROM receiving_session_items AS session_items
			JOIN products ON products.id = session_items.product_id
			WHERE session_items.session_id = $1
		`,
		sessionID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	pendingItems := make(map[string]int)
	for rows.Next() {
		var sku string
		var quantity int
		if err := rows.Scan(&sku, &quantity); err != nil {
			return err
		}
		pendingItems[sku] = quantity
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(pendingItems) != len(requestedItems) {
		return errPendingItemsMismatch
	}

	for _, item := range requestedItems {
		if pendingItems[item.SKU] != item.Quantity {
			return errPendingItemsMismatch
		}
	}

	return nil
}

var errPendingItemsMismatch = errors.New("pending items do not match placement request")

func createReceivingPlacementHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var request receivingPlacementRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	request.CellCode = strings.ToUpper(strings.TrimSpace(request.CellCode))

	if request.SessionID <= 0 || request.CellCode == "" || len(request.Items) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, "sessionId, cellCode, and items are required")
		return
	}

	seenSKU := make(map[string]bool)

	for i := range request.Items {
		request.Items[i].SKU = strings.ToUpper(
			strings.TrimSpace(request.Items[i].SKU),
		)

		if request.Items[i].SKU == "" || request.Items[i].Quantity <= 0 {
			writeErrorResponse(
				w,
				http.StatusBadRequest,
				"each item needs a SKU and a positive quantity",
			)
			return
		}

		if seenSKU[request.Items[i].SKU] {
			writeErrorResponse(
				w,
				http.StatusBadRequest,
				"duplicate SKUs are not allowed",
			)
			return
		}

		seenSKU[request.Items[i].SKU] = true
	}

	tx, err := database.Begin(r.Context())
	if err != nil {
		log.Printf("failed to start placement transaction: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to place items",
		)
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
		writeErrorResponse(w, http.StatusInternalServerError, "failed to place items")
		return
	}

	if err := validatePendingItems(
		r.Context(),
		tx,
		request.SessionID,
		request.Items,
	); err != nil {
		if errors.Is(err, errPendingItemsMismatch) {
			writeErrorResponse(w, http.StatusConflict, "pending items changed; reload the receiving session")
			return
		}
		log.Printf("failed to validate pending items for session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to place items")
		return
	}

	var cellID int64

	err = tx.QueryRow(
		r.Context(),
		`SELECT cells.id FROM cells
		 WHERE cells.code = $1
		 AND cells.category_id = (SELECT category_id FROM products WHERE sku = $2)
		 AND NOT EXISTS (
		   SELECT 1 FROM receiving_session_items pending
		   JOIN products ON products.id = pending.product_id
		   WHERE pending.session_id = $3 AND products.category_id <> cells.category_id
		 )`,
		request.CellCode,
		request.Items[0].SKU,
		request.SessionID,
	).Scan(&cellID)

	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "Ячейка не найдена в стеллаже товара. Все товары группы должны относиться к одному стеллажу.")
		return
	}

	if err != nil {
		log.Printf("failed to find cell %s: %v", request.CellCode, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to place items",
		)
		return
	}

	var placementID int64
	var createdAt time.Time

	err = tx.QueryRow(
		r.Context(),
		`
			INSERT INTO receiving_placements (cell_id, receiving_session_id)
			VALUES ($1, $2)
			RETURNING id, created_at
		`,
		cellID,
		request.SessionID,
	).Scan(&placementID, &createdAt)

	if err != nil {
		log.Printf("failed to create placement record: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to place items",
		)
		return
	}

	for _, item := range request.Items {
		var productID int64

		err := tx.QueryRow(
			r.Context(),
			`SELECT id FROM products WHERE sku = $1 AND NOT archived FOR SHARE`,
			item.SKU,
		).Scan(&productID)

		if errors.Is(err, pgx.ErrNoRows) {
			writeErrorResponse(w, http.StatusNotFound, "product not found")
			return
		}

		if err != nil {
			log.Printf("failed to find product %s: %v", item.SKU, err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to place items",
			)
			return
		}

		_, err = tx.Exec(
			r.Context(),
			`
				INSERT INTO cell_stock (cell_id, product_id, quantity)
				VALUES ($1, $2, $3)
				ON CONFLICT (cell_id, product_id)
				DO UPDATE SET
					quantity = cell_stock.quantity + EXCLUDED.quantity,
					updated_at = NOW()
			`,
			cellID,
			productID,
			item.Quantity,
		)

		if err != nil {
			log.Printf("failed to update stock for SKU %s: %v", item.SKU, err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to place items",
			)
			return
		}

		_, err = tx.Exec(
			r.Context(),
			`
				INSERT INTO receiving_placement_items
					(placement_id, product_id, quantity)
				VALUES ($1, $2, $3)
			`,
			placementID,
			productID,
			item.Quantity,
		)

		if err != nil {
			log.Printf("failed to save placement item for SKU %s: %v", item.SKU, err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to place items",
			)
			return
		}
	}

	_, err = tx.Exec(
		r.Context(),
		`DELETE FROM receiving_session_items WHERE session_id = $1`,
		request.SessionID,
	)
	if err != nil {
		log.Printf("failed to clear pending items for session %d: %v", request.SessionID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to place items")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("failed to commit placement: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to place items",
		)
		return
	}

	response := receivingPlacementResponse{
		ID:        placementID,
		CellCode:  request.CellCode,
		CreatedAt: createdAt,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode placement response: %v", err)
	}
}

func undoReceivingPlacementHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	placementID, err := strconv.ParseInt(r.PathValue("placementID"), 10, 64)
	if err != nil || placementID <= 0 {
		writeErrorResponse(w, http.StatusBadRequest, "invalid placement ID")
		return
	}

	tx, err := database.Begin(r.Context())
	if err != nil {
		log.Printf("failed to start undo transaction: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}
	defer tx.Rollback(r.Context())

	var initialSessionID sql.NullInt64

	err = tx.QueryRow(
		r.Context(),
		`
			SELECT receiving_session_id
			FROM receiving_placements
			WHERE id = $1
		`,
		placementID,
	).Scan(&initialSessionID)

	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "placement not found")
		return
	}

	if err != nil {
		log.Printf("failed to load placement %d: %v", placementID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}

	if initialSessionID.Valid {
		err = lockActiveReceivingSession(r.Context(), tx, initialSessionID.Int64)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErrorResponse(w, http.StatusNotFound, "receiving session not found")
			return
		}
		if errors.Is(err, errReceivingSessionNotActive) {
			writeErrorResponse(w, http.StatusConflict, "completed receiving cannot be undone")
			return
		}
		if err != nil {
			log.Printf("failed to lock receiving session %d: %v", initialSessionID.Int64, err)
			writeErrorResponse(w, http.StatusInternalServerError, "failed to undo placement")
			return
		}
	}

	var cellID int64
	var status string
	var currentSessionID sql.NullInt64
	err = tx.QueryRow(
		r.Context(),
		`
			SELECT cell_id, status, receiving_session_id
			FROM receiving_placements
			WHERE id = $1
			FOR UPDATE
		`,
		placementID,
	).Scan(&cellID, &status, &currentSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "placement not found")
		return
	}
	if err != nil {
		log.Printf("failed to lock placement %d: %v", placementID, err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to undo placement")
		return
	}
	if status != "active" {
		writeErrorResponse(w, http.StatusConflict, "placement is already undone")
		return
	}
	if currentSessionID.Valid != initialSessionID.Valid ||
		(currentSessionID.Valid && currentSessionID.Int64 != initialSessionID.Int64) {
		writeErrorResponse(w, http.StatusConflict, "placement changed; reload the receiving session")
		return
	}

	rows, err := tx.Query(
		r.Context(),
		`
			SELECT product_id, quantity
			FROM receiving_placement_items
			WHERE placement_id = $1
		`,
		placementID,
	)

	if err != nil {
		log.Printf("failed to load items for placement %d: %v", placementID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}

	items := make([]placementStockItem, 0)

	for rows.Next() {
		var item placementStockItem

		if err := rows.Scan(&item.productID, &item.quantity); err != nil {
			rows.Close()
			log.Printf("failed to read placement item: %v", err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to undo placement",
			)
			return
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("failed to read placement items: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}

	rows.Close()

	if len(items) == 0 {
		writeErrorResponse(w, http.StatusConflict, "placement has no items")
		return
	}

	for _, item := range items {
		result, err := tx.Exec(
			r.Context(),
			`
				UPDATE cell_stock
				SET quantity = quantity - $1,
					updated_at = NOW()
				WHERE cell_id = $2
					AND product_id = $3
					AND quantity >= $1
			`,
			item.quantity,
			cellID,
			item.productID,
		)

		if err != nil {
			log.Printf("failed to restore stock during undo: %v", err)
			writeErrorResponse(
				w,
				http.StatusInternalServerError,
				"failed to undo placement",
			)
			return
		}

		if result.RowsAffected() != 1 {
			writeErrorResponse(
				w,
				http.StatusConflict,
				"stock changed; placement cannot be undone",
			)
			return
		}
	}

	_, err = tx.Exec(
		r.Context(),
		`
			UPDATE receiving_placements
			SET status = 'undone',
				undone_at = NOW()
			WHERE id = $1
		`,
		placementID,
	)

	if err != nil {
		log.Printf("failed to mark placement %d as undone: %v", placementID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}

	if currentSessionID.Valid {
		for _, item := range items {
			_, err = tx.Exec(
				r.Context(),
				`
					INSERT INTO receiving_session_items (session_id, product_id, quantity)
					VALUES ($1, $2, $3)
					ON CONFLICT (session_id, product_id)
					DO UPDATE SET quantity = receiving_session_items.quantity + EXCLUDED.quantity
				`,
				currentSessionID.Int64,
				item.productID,
				item.quantity,
			)
			if err != nil {
				log.Printf("failed to restore pending item for placement %d: %v", placementID, err)
				writeErrorResponse(w, http.StatusInternalServerError, "failed to undo placement")
				return
			}
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("failed to commit undo: %v", err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to undo placement",
		)
		return
	}

	response := undoPlacementResponse{
		ID:     placementID,
		Status: "undone",
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode undo response: %v", err)
	}
}
