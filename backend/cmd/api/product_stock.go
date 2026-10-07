package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func productStockFailure(w http.ResponseWriter, err error) {
	log.Printf("product stock query failed: %v", err)
	writeErrorResponse(w, http.StatusInternalServerError, "failed to read product stock")
}

func encodeProductStock(w http.ResponseWriter, response any) {
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode product stock response: %v", err)
	}
}
func productStockHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	sku := strings.ToUpper(strings.TrimSpace(r.PathValue("sku")))
	response := productStockResponse{Cells: make([]productStockCell, 0)}
	err := db.QueryRow(r.Context(), `SELECT sku, name FROM products WHERE sku = $1 AND NOT archived`, sku).Scan(&response.SKU, &response.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorResponse(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		productStockFailure(w, err)
		return
	}
	rows, err := db.Query(r.Context(), `SELECT cells.id, cells.code, warehouses.name, stock.quantity
		FROM cell_stock AS stock
		JOIN cells ON cells.id = stock.cell_id
		JOIN warehouses ON warehouses.id = cells.warehouse_id
		JOIN products ON products.id = stock.product_id
		WHERE products.sku = $1 AND NOT products.archived AND stock.quantity > 0
		ORDER BY warehouses.name, cells.code, cells.id`, sku)
	if err != nil {
		productStockFailure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cell productStockCell
		if err := rows.Scan(&cell.CellID, &cell.CellCode, &cell.Warehouse, &cell.Quantity); err != nil {
			productStockFailure(w, err)
			return
		}
		response.Cells = append(response.Cells, cell)
	}
	if err := rows.Err(); err != nil {
		productStockFailure(w, err)
		return
	}
	encodeProductStock(w, response)
}
