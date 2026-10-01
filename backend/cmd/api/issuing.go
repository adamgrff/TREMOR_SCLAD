package main

import (
	"encoding/json"
	"errors"
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

type issueRequest struct {
	RequestID string `json:"requestId"`
	SKU       string `json:"sku"`
	CellID    int64  `json:"cellId"`
	Quantity  int    `json:"quantity"`
}

type issueResponse struct {
	ID        int64      `json:"id"`
	RequestID string     `json:"requestId"`
	SKU       string     `json:"sku"`
	Name      string     `json:"name"`
	CellID    int64      `json:"cellId"`
	CellCode  string     `json:"cellCode"`
	Warehouse string     `json:"warehouse"`
	Quantity  int        `json:"quantity"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	UndoneAt  *time.Time `json:"undoneAt"`
}

type productStockCell struct {
	CellID    int64  `json:"cellId"`
	CellCode  string `json:"cellCode"`
	Warehouse string `json:"warehouse"`
	Quantity  int    `json:"quantity"`
}

type productStockResponse struct {
	SKU   string             `json:"sku"`
	Name  string             `json:"name"`
	Cells []productStockCell `json:"cells"`
}

type issueHistoryResponse struct {
	Issues     []issueResponse `json:"issues"`
	UndoableID *int64          `json:"undoableId"`
}

var issueRequestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const issueSelect = `SELECT issues.id, issues.request_id::text, products.sku, products.name,
	issues.cell_id, cells.code, warehouses.name, issues.quantity, issues.status,
	issues.created_at, issues.undone_at
	FROM stock_issues AS issues
	JOIN products ON products.id = issues.product_id
	JOIN cells ON cells.id = issues.cell_id
	JOIN warehouses ON warehouses.id = cells.warehouse_id `

func scanIssue(row pgx.Row) (issueResponse, error) {
	var issue issueResponse
	err := row.Scan(&issue.ID, &issue.RequestID, &issue.SKU, &issue.Name, &issue.CellID,
		&issue.CellCode, &issue.Warehouse, &issue.Quantity, &issue.Status, &issue.CreatedAt, &issue.UndoneAt)
	return issue, err
}

func issuingFailure(w http.ResponseWriter, err error) {
	log.Printf("issuing operation failed: %v", err)
	writeErrorResponse(w, http.StatusInternalServerError, "failed to process issuing operation")
}

func encodeIssue(w http.ResponseWriter, response any) {
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode issuing response: %v", err)
	}
}

func productStockHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	sku := strings.ToUpper(strings.TrimSpace(r.PathValue("sku")))
	response := productStockResponse{Cells: make([]productStockCell, 0)}
	err := db.QueryRow(r.Context(), `SELECT sku, name FROM products WHERE sku = $1`, sku).Scan(&response.SKU, &response.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		issuingFailure(w, err)
		return
	}
	rows, err := db.Query(r.Context(), `SELECT cells.id, cells.code, warehouses.name, stock.quantity
		FROM cell_stock AS stock
		JOIN cells ON cells.id = stock.cell_id
		JOIN warehouses ON warehouses.id = cells.warehouse_id
		JOIN products ON products.id = stock.product_id
		WHERE products.sku = $1 AND stock.quantity > 0
		ORDER BY warehouses.name, cells.code, cells.id`, sku)
	if err != nil {
		issuingFailure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cell productStockCell
		if err := rows.Scan(&cell.CellID, &cell.CellCode, &cell.Warehouse, &cell.Quantity); err != nil {
			issuingFailure(w, err)
			return
		}
		response.Cells = append(response.Cells, cell)
	}
	if err := rows.Err(); err != nil {
		issuingFailure(w, err)
		return
	}
	encodeIssue(w, response)
}

func createIssueHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request issueRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request.SKU = strings.ToUpper(strings.TrimSpace(request.SKU))
	request.RequestID = strings.ToLower(strings.TrimSpace(request.RequestID))
	if !issueRequestIDPattern.MatchString(request.RequestID) || request.SKU == "" || request.CellID <= 0 || request.Quantity <= 0 || request.Quantity > 2147483647 {
		writeErrorResponse(w, http.StatusBadRequest, "requestId, SKU, cellId and positive quantity are required")
		return
	}
	tx, err := db.Begin(r.Context())
	if err != nil {
		issuingFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize issue/undo commands so 'last action' and request retries stay consistent.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(20261001, 6)`); err != nil {
		issuingFailure(w, err)
		return
	}
	existing, err := scanIssue(tx.QueryRow(r.Context(), issueSelect+`WHERE issues.request_id = $1::uuid`, request.RequestID))
	if err == nil {
		if existing.SKU != request.SKU || existing.CellID != request.CellID || existing.Quantity != request.Quantity {
			writeErrorResponse(w, http.StatusConflict, "request ID was already used for a different operation")
			return
		}
		encodeIssue(w, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		issuingFailure(w, err)
		return
	}
	var productID int64
	err = tx.QueryRow(r.Context(), `SELECT id FROM products WHERE sku = $1`, request.SKU).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		issuingFailure(w, err)
		return
	}
	result, err := tx.Exec(r.Context(), `UPDATE cell_stock SET quantity = quantity - $1, updated_at = NOW()
		WHERE cell_id = $2 AND product_id = $3 AND quantity >= $1`, request.Quantity, request.CellID, productID)
	if err != nil {
		issuingFailure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		writeErrorResponse(w, http.StatusConflict, "insufficient stock; reload product stock")
		return
	}
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO stock_issues (request_id, cell_id, product_id, quantity)
		VALUES ($1::uuid, $2, $3, $4) RETURNING id`, request.RequestID, request.CellID, productID, request.Quantity).Scan(&id)
	if err != nil {
		issuingFailure(w, err)
		return
	}
	response, err := scanIssue(tx.QueryRow(r.Context(), issueSelect+`WHERE issues.id = $1`, id))
	if err != nil {
		issuingFailure(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		issuingFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	encodeIssue(w, response)
}

func undoIssueHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	id, err := strconv.ParseInt(r.PathValue("issueID"), 10, 64)
	if err != nil || id <= 0 {
		writeErrorResponse(w, http.StatusBadRequest, "invalid issue ID")
		return
	}
	tx, err := db.Begin(r.Context())
	if err != nil {
		issuingFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(20261001, 6)`); err != nil {
		issuingFailure(w, err)
		return
	}
	var cellID, productID int64
	var quantity int
	var status string
	err = tx.QueryRow(r.Context(), `SELECT cell_id, product_id, quantity, status FROM stock_issues WHERE id = $1 FOR UPDATE`, id).Scan(&cellID, &productID, &quantity, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		issuingFailure(w, err)
		return
	}
	// Retry after a lost undo response must not restore the quantity twice.
	if status == "undone" {
		response, err := scanIssue(tx.QueryRow(r.Context(), issueSelect+`WHERE issues.id = $1`, id))
		if err != nil {
			issuingFailure(w, err)
			return
		}
		encodeIssue(w, response)
		return
	}
	var newer bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM stock_issues WHERE id > $1 AND status = 'active')`, id).Scan(&newer); err != nil {
		issuingFailure(w, err)
		return
	}
	if newer {
		writeErrorResponse(w, http.StatusConflict, "only the last active issue can be undone; reload history")
		return
	}
	var restored int
	err = tx.QueryRow(r.Context(), `INSERT INTO cell_stock (cell_id, product_id, quantity) VALUES ($1, $2, $3)
		ON CONFLICT (cell_id, product_id) DO UPDATE SET quantity = cell_stock.quantity + EXCLUDED.quantity, updated_at = NOW()
		WHERE cell_stock.quantity::bigint + EXCLUDED.quantity <= 2147483647
		RETURNING quantity`, cellID, productID, quantity).Scan(&restored)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusConflict, "stock quantity limit exceeded")
		return
	}
	if err != nil {
		issuingFailure(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE stock_issues SET status = 'undone', undone_at = NOW() WHERE id = $1`, id); err != nil {
		issuingFailure(w, err)
		return
	}
	response, err := scanIssue(tx.QueryRow(r.Context(), issueSelect+`WHERE issues.id = $1`, id))
	if err != nil {
		issuingFailure(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		issuingFailure(w, err)
		return
	}
	encodeIssue(w, response)
}

func issueHistoryHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	rows, err := db.Query(r.Context(), issueSelect+`ORDER BY issues.id DESC LIMIT 20`)
	if err != nil {
		issuingFailure(w, err)
		return
	}
	defer rows.Close()
	response := issueHistoryResponse{Issues: make([]issueResponse, 0)}
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			issuingFailure(w, err)
			return
		}
		response.Issues = append(response.Issues, issue)
		if response.UndoableID == nil && issue.Status == "active" {
			id := issue.ID
			response.UndoableID = &id
		}
	}
	if err := rows.Err(); err != nil {
		issuingFailure(w, err)
		return
	}
	encodeIssue(w, response)
}
