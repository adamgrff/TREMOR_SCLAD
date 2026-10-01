package main

import (
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
	"unicode/utf8"
)

func createRackHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input struct {
		Name   string `json:"name"`
		Counts []int  `json:"counts"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeErrorResponse(w, 400, "Некорректные параметры стеллажа")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 50 || len(input.Counts) != 3 {
		writeErrorResponse(w, 400, "Укажите название до 50 символов и количество ячеек для A, B, C")
		return
	}
	total := 0
	for _, count := range input.Counts {
		if count < 0 || count > 100 {
			writeErrorResponse(w, 400, "В каждой строке допустимо от 0 до 100 ячеек")
			return
		}
		total += count
	}
	if total == 0 {
		writeErrorResponse(w, 400, "Добавьте хотя бы одну ячейку")
		return
	}
	tx, err := db.Begin(r.Context())
	if err != nil {
		writeErrorResponse(w, 500, "Не удалось создать стеллаж")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))`, input.Name); err != nil {
		writeErrorResponse(w, 500, "Не удалось создать стеллаж")
		return
	}
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM categories WHERE lower(name)=lower($1))`, input.Name).Scan(&exists); err != nil {
		writeErrorResponse(w, 500, "Не удалось создать стеллаж")
		return
	}
	if exists {
		writeErrorResponse(w, 409, "Стеллаж с таким названием уже существует")
		return
	}
	var warehouseID int64
	var warehouses int
	if err = tx.QueryRow(r.Context(), `SELECT count(*),coalesce(min(id),0) FROM warehouses`).Scan(&warehouses, &warehouseID); err != nil || warehouses != 1 {
		writeErrorResponse(w, 409, "Для создания стеллажа должен быть настроен один склад")
		return
	}
	var id int64
	if err = tx.QueryRow(r.Context(), `INSERT INTO categories(code,name) VALUES($1,$2) RETURNING id`, "rack-"+strings.ToLower(input.Name), input.Name).Scan(&id); err != nil {
		writeErrorResponse(w, 500, "Не удалось создать стеллаж")
		return
	}
	rack := rackResponse{ID: id, Name: input.Name, Rows: make([][]string, 0)}
	for row, count := range input.Counts {
		if count == 0 {
			continue
		}
		codes := make([]string, 0, count)
		for position := 1; position <= count; position++ {
			code := fmt.Sprintf("%c%d", 'A'+row, position)
			_, err = tx.Exec(r.Context(), `INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) VALUES($1,$2,$3,$4,$5)`, warehouseID, id, code, row+1, position)
			if err != nil {
				writeErrorResponse(w, 500, "Не удалось создать ячейки стеллажа")
				return
			}
			codes = append(codes, code)
		}
		rack.Rows = append(rack.Rows, codes)
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeErrorResponse(w, 500, "Не удалось сохранить стеллаж")
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rack)
}
