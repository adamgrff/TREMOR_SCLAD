package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	Archived   bool   `json:"archived"`
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
	rows, err := db.Query(r.Context(), `SELECT products.id, products.category_id, products.name, products.sku, products.archived,
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
		if err := rows.Scan(&product.ID, &product.CategoryID, &product.Name, &product.SKU, &product.Archived, &product.Quantity); err != nil {
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

func saveCatalogProductHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name              string `json:"name"`
		SKU               string `json:"sku"`
		CategoryID        int64  `json:"categoryId"`
		Archived          bool   `json:"archived"`
		ReceivingQuantity int    `json:"receivingQuantity"`
		SessionID         int64  `json:"sessionId"`
		RequestID         string `json:"requestId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeErrorResponse(w, 400, "Некорректные данные товара")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SKU = strings.ToUpper(strings.TrimSpace(input.SKU))
	if input.ReceivingQuantity < 0 || input.ReceivingQuantity > 1000000000 || (input.ReceivingQuantity > 0 && (input.SessionID <= 0 || input.Archived || r.Method != http.MethodPost)) {
		writeErrorResponse(w, 400, "Некорректное количество или приёмка")
		return
	}
	if input.Name == "" || len([]rune(input.Name)) > 200 || input.SKU == "" || len([]rune(input.SKU)) > 128 || input.CategoryID <= 0 {
		writeErrorResponse(w, 400, "Укажите название, код и стеллаж")
		return
	}
	var id int64
	var err error
	if r.Method == http.MethodPut {
		id, err = strconv.ParseInt(r.PathValue("productID"), 10, 64)
		if err != nil || id <= 0 {
			writeErrorResponse(w, 400, "Некорректный товар")
			return
		}
	}
	tx, err := db.Begin(r.Context())
	if err != nil {
		writeErrorResponse(w, 500, "Не удалось сохранить товар")
		return
	}
	defer tx.Rollback(r.Context())
	var quantityPayload string
	if input.ReceivingQuantity > 0 {
		data, _ := json.Marshal(input)
		quantityPayload = string(data)
		prior, retryErr := receivingQuantityRetry(r.Context(), tx, input.RequestID, quantityPayload)
		if retryErr != nil {
			writeErrorResponse(w, 409, retryErr.Error())
			return
		}
		if prior != 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int64{"id": prior})
			return
		}
		if err := lockActiveReceivingSession(r.Context(), tx, input.SessionID); err != nil {
			writeErrorResponse(w, 409, "Начните новую приёмку перед добавлением количества")
			return
		}
	}
	if id != 0 {
		var previousCategory int64
		if err = tx.QueryRow(r.Context(), `SELECT category_id FROM products WHERE id = $1 FOR UPDATE`, id).Scan(&previousCategory); errors.Is(err, pgx.ErrNoRows) {
			writeErrorResponse(w, 404, "Товар не найден")
			return
		} else if err != nil {
			writeErrorResponse(w, 500, "Не удалось проверить товар")
			return
		}
		var busy bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM cell_stock WHERE product_id = $1 AND quantity > 0)
   OR EXISTS(SELECT 1 FROM receiving_session_items WHERE product_id = $1)`, id).Scan(&busy)
		if err != nil {
			writeErrorResponse(w, 500, "Не удалось проверить остатки")
			return
		}
		if busy && input.CategoryID != previousCategory {
			writeErrorResponse(w, 409, "Нельзя менять стеллаж: товар размещён либо ожидает ячейку")
			return
		}
		if input.Archived {
			var pending bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM receiving_session_items WHERE product_id=$1)`, id).Scan(&pending); err != nil {
				writeErrorResponse(w, 500, "Не удалось проверить приёмку")
				return
			}
			if pending {
				writeErrorResponse(w, 409, "Сначала разместите или удалите товар из ожидающих ячейку в текущей приёмке")
				return
			}
		}
		_, err = tx.Exec(r.Context(), `UPDATE products SET name=$2, sku=$3, category_id=$4, archived=$5 WHERE id=$1`, id, input.Name, input.SKU, input.CategoryID, input.Archived)
	} else {
		err = tx.QueryRow(r.Context(), `INSERT INTO products (name, sku, category_id) VALUES ($1,$2,$3) RETURNING id`, input.Name, input.SKU, input.CategoryID).Scan(&id)
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			writeErrorResponse(w, 409, "Этот код уже существует, в том числе в архиве")
			return
		}
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			writeErrorResponse(w, 400, "Стеллаж не найден")
			return
		}
		log.Printf("save catalog product: %v", err)
		writeErrorResponse(w, 500, "Не удалось сохранить товар")
		return
	}
	if input.ReceivingQuantity > 0 {
		if _, err = tx.Exec(r.Context(), `INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,$2,$3)`, input.SessionID, id, input.ReceivingQuantity); err != nil {
			writeErrorResponse(w, 500, "Не удалось добавить товар в приёмку")
			return
		}
		if err = saveReceivingQuantityRequest(r.Context(), tx, input.RequestID, quantityPayload, id); err != nil {
			writeErrorResponse(w, 500, "Не удалось сохранить операцию")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeErrorResponse(w, 500, "Не удалось сохранить товар")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	}
	if err := json.NewEncoder(w).Encode(map[string]int64{"id": id}); err != nil {
		log.Printf("encode saved catalog product: %v", err)
	}
}
