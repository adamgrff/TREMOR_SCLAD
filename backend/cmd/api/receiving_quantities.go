package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lock the request token before checking it, so concurrent retries cannot add twice.
func receivingQuantityRetry(ctx context.Context, tx pgx.Tx, token, payload string) (int64, error) {
	if len(token) != 36 {
		return 0, errors.New("Некорректный идентификатор операции")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, token); err != nil {
		return 0, err
	}
	var previous string
	var id int64
	err := tx.QueryRow(ctx, `SELECT payload, product_id FROM receiving_quantity_requests WHERE request_id=$1`, token).Scan(&previous, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if previous != payload {
		return 0, errors.New("Операция уже использована с другими данными")
	}
	return id, nil
}

func saveReceivingQuantityRequest(ctx context.Context, tx pgx.Tx, token, payload string, productID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO receiving_quantity_requests (request_id,payload,product_id) VALUES ($1,$2,$3)`, token, payload, productID)
	return err
}

func receivingQuantityHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseReceivingSessionID(r)
	if err != nil {
		writeErrorResponse(w, 400, "Некорректная приёмка")
		return
	}
	productID, err := strconv.ParseInt(r.PathValue("productID"), 10, 64)
	if err != nil || productID <= 0 {
		writeErrorResponse(w, 400, "Некорректный товар")
		return
	}
	var input struct {
		Quantity         int    `json:"quantity"`
		ExpectedQuantity int    `json:"expectedQuantity"`
		RequestID        string `json:"requestId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Quantity <= 0 || input.Quantity > 1000000000 {
		writeErrorResponse(w, 400, "Количество должно быть целым числом от 1 до 1000000000")
		return
	}
	payload := fmt.Sprintf("%s:%d:%d:%d:%d", r.Method, sessionID, productID, input.Quantity, input.ExpectedQuantity)
	tx, err := db.Begin(r.Context())
	if err != nil {
		writeErrorResponse(w, 500, "Не удалось изменить количество")
		return
	}
	defer tx.Rollback(r.Context())
	prior, err := receivingQuantityRetry(r.Context(), tx, input.RequestID, payload)
	if err != nil {
		writeErrorResponse(w, 409, err.Error())
		return
	}
	if prior != 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err = lockActiveReceivingSession(r.Context(), tx, sessionID); err != nil {
		writeErrorResponse(w, 409, "Приёмка недоступна или уже завершена")
		return
	}
	var archived bool
	if err = tx.QueryRow(r.Context(), `SELECT archived FROM products WHERE id=$1 FOR SHARE`, productID).Scan(&archived); err != nil || archived {
		writeErrorResponse(w, 409, "Товар не найден или находится в архиве")
		return
	}
	if r.Method == http.MethodPut {
		result, updateErr := tx.Exec(r.Context(), `UPDATE receiving_session_items SET quantity=$3 WHERE session_id=$1 AND product_id=$2 AND quantity=$4`, sessionID, productID, input.Quantity, input.ExpectedQuantity)
		if updateErr != nil {
			writeErrorResponse(w, 500, "Не удалось изменить количество")
			return
		}
		if result.RowsAffected() != 1 {
			writeErrorResponse(w, 409, "Количество изменилось или товар уже размещён. Обновите приёмку.")
			return
		}
	} else {
		_, err = tx.Exec(r.Context(), `INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,$2,$3)
   ON CONFLICT(session_id,product_id) DO UPDATE SET quantity=receiving_session_items.quantity+EXCLUDED.quantity`, sessionID, productID, input.Quantity)
		if err != nil {
			writeErrorResponse(w, 409, "Не удалось добавить количество")
			return
		}
	}
	if err = saveReceivingQuantityRequest(r.Context(), tx, input.RequestID, payload, productID); err != nil {
		writeErrorResponse(w, 500, "Не удалось сохранить операцию")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeErrorResponse(w, 500, "Не удалось сохранить количество")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
