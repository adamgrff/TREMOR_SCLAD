package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
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
	PendingRevision string                        `json:"pendingRevision"`
	ID              int64                         `json:"id"`
	Status          string                        `json:"status"`
	StartedAt       time.Time                     `json:"startedAt"`
	CompletedAt     *time.Time                    `json:"completedAt"`
	PendingItems    []receivingSessionPendingItem `json:"pendingItems"`
	PlacedItems     []receivingSessionPlacedItem  `json:"placedItems"`
}

var receivingScanRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type addReceivingSessionItemRequest struct {
	RequestID string `json:"requestId"`
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
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

func createReceivingSessionHandler(database *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var request receivingCommandRequest
	if !decodeReceivingCommand(w, r, &request) {
		return
	}
	tx := beginReceivingOperation(database, w, r, request.RequestID, "create-session", struct{}{})
	if tx == nil {
		return
	}
	defer tx.Rollback(r.Context())
	var response createReceivingSessionResponse
	err := tx.QueryRow(r.Context(), `INSERT INTO receiving_sessions(status,completed_at) VALUES('active',NULL) RETURNING id,status,started_at`).Scan(&response.ID, &response.Status, &response.StartedAt)
	if err != nil {
		writeErrorResponse(w, 500, "failed to create receiving session")
		return
	}
	tx.respond(w, r, http.StatusCreated, response)
}

func getReceivingSessionHandler(
	database *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	sessionID, err := parseReceivingSessionID(r)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := database.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeErrorResponse(w, 500, "failed to read receiving session")
		return
	}
	defer tx.Rollback(r.Context())
	var response receivingSessionDetailsResponse
	var completedAt sql.NullTime

	err = tx.QueryRow(
		r.Context(),
		`
			SELECT id, status, started_at, completed_at, pending_revision::text
			FROM receiving_sessions
			WHERE id = $1 AND deleted_at IS NULL
		`,
		sessionID,
	).Scan(
		&response.ID,
		&response.Status,
		&response.StartedAt,
		&completedAt,
		&response.PendingRevision,
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
	pendingRows, err := tx.Query(
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
	placedRows, err := tx.Query(
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

	if err := tx.Commit(r.Context()); err != nil {
		writeErrorResponse(w, 500, "failed to read receiving session")
		return
	}
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

	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	request.RequestID = strings.ToLower(strings.TrimSpace(request.RequestID))
	if !receivingScanRequestID.MatchString(request.RequestID) {
		writeErrorResponse(w, http.StatusBadRequest, "requestId must be a UUID")
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

	// Match the quantity API: request lock/receipt first, then session and product.
	// Check the receipt before session status so a committed scan can be replayed
	// after completion, placement or product archival without touching quantities.
	payload := fmt.Sprintf("receiving-scan:%d:%q:%d", sessionID, request.SKU, request.Quantity)
	prior, err := receivingQuantityRetry(r.Context(), tx, request.RequestID, payload)
	if err != nil {
		if errors.Is(err, errReceivingQuantityRequestConflict) {
			writeErrorResponse(w, http.StatusConflict, err.Error())
		} else {
			writeErrorResponse(w, http.StatusInternalServerError, "failed to check scan receipt")
		}
		return
	}
	if prior != 0 {
		_ = json.NewEncoder(w).Encode(receivingSessionStatusResponse{Status: "saved"})
		return
	}
	// Like the shared operation receipt API, distinguish a proven fresh
	// business rejection from an identity conflict or uncertain COMMIT.
	w.Header().Set("Access-Control-Expose-Headers", "X-Receiving-Command-Rejected")
	w.Header().Set("X-Receiving-Command-Rejected", "true")

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
		`SELECT id FROM products WHERE sku = $1 AND NOT archived FOR SHARE`,
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

	err = addReceivingSessionQuantity(r.Context(), tx, sessionID, productID, request.Quantity)
	if err != nil {
		log.Printf("failed to save pending SKU %s: %v", request.SKU, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to save pending item",
		)
		return
	}

	if err := saveReceivingQuantityRequest(r.Context(), tx, request.RequestID, payload, productID); err != nil {
		log.Printf("failed to save scan receipt: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to save scan receipt")
		return
	}

	w.Header().Del("X-Receiving-Command-Rejected")
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

func deleteReceivingSessionItemHandler(
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

	var input struct {
		receivingCommandRequest
		ExpectedRevision *string `json:"expectedRevision"`
	}
	if !decodeReceivingCommand(w, r, &input) {
		return
	}
	if input.ExpectedRevision == nil {
		writeErrorResponse(w, 400, "expectedRevision is required")
		return
	}
	revision, err := strconv.ParseInt(*input.ExpectedRevision, 10, 64)
	if err != nil || revision < 0 {
		writeErrorResponse(w, 400, "invalid expectedRevision")
		return
	}
	sku := strings.ToUpper(strings.TrimSpace(r.PathValue("sku")))
	if sku == "" {
		writeErrorResponse(w, 400, "SKU is required")
		return
	}
	tx := beginReceivingOperation(database, w, r, input.RequestID, "delete-item", map[string]any{"sessionId": sessionID, "sku": sku, "expectedRevision": revision})
	if tx == nil {
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
			"failed to delete pending item",
		)
		return
	}

	var currentRevision int64
	if err = tx.QueryRow(r.Context(), `SELECT pending_revision FROM receiving_sessions WHERE id=$1`, sessionID).Scan(&currentRevision); err != nil {
		writeErrorResponse(w, 500, "failed to read pending revision")
		return
	}
	if currentRevision != revision {
		writeErrorResponse(w, 409, "pending group changed; reload before deleting")
		return
	}
	_, err = tx.Exec(
		r.Context(),
		`DELETE FROM receiving_session_items WHERE session_id = $1
		 AND product_id = (SELECT id FROM products WHERE sku = $2)`,
		sessionID,
		sku,
	)
	if err != nil {
		log.Printf("failed to delete pending item for session %d: %v", sessionID, err)
		writeErrorResponse(
			w,
			http.StatusInternalServerError,
			"failed to delete pending item",
		)
		return
	}

	tx.respond(w, r, http.StatusOK, receivingSessionStatusResponse{Status: "deleted"})
}
