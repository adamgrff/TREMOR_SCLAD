package main

import (
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
)

type catalogCategory struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type catalogProduct struct {
	ID         int64  `json:"id"`
	CategoryID int64  `json:"categoryId"`
	Name       string `json:"name"`
	SKU        string `json:"sku"`
	Quantity   int64  `json:"quantity"`
}

// This read-only catalog also includes products with no remaining stock.
func productCatalogHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	response := struct {
		Categories []catalogCategory `json:"categories"`
		Products   []catalogProduct  `json:"products"`
	}{Categories: []catalogCategory{}, Products: []catalogProduct{}}
	categories, err := db.Query(r.Context(), `SELECT id, name FROM categories ORDER BY id`)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
		return
	}
	defer categories.Close()
	for categories.Next() {
		var category catalogCategory
		if err := categories.Scan(&category.ID, &category.Name); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
			return
		}
		response.Categories = append(response.Categories, category)
	}
	if categories.Err() != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
		return
	}
	rows, err := db.Query(r.Context(), `SELECT products.id, products.category_id, products.name, products.sku,
 COALESCE(SUM(cell_stock.quantity), 0)::bigint FROM products
 LEFT JOIN cell_stock ON cell_stock.product_id = products.id
 GROUP BY products.id ORDER BY products.name, products.id`)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var product catalogProduct
		if err := rows.Scan(&product.ID, &product.CategoryID, &product.Name, &product.SKU, &product.Quantity); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
			return
		}
		response.Products = append(response.Products, product)
	}
	if rows.Err() != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load catalog")
		return
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode product catalog: %v", err)
	}
}
