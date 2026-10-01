package main

import (
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
)

type rackResponse struct {
	ID   int64      `json:"id"`
	Name string     `json:"name"`
	Rows [][]string `json:"rows"`
}

func racksHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	rows, err := db.Query(r.Context(), `SELECT categories.id, categories.name,
 cells.code, cells.row_number FROM categories
 LEFT JOIN cells ON cells.category_id = categories.id
 ORDER BY categories.id, cells.row_number, cells.position_number`)
	if err != nil {
		writeErrorResponse(w, 500, "Не удалось загрузить стеллажи")
		return
	}
	defer rows.Close()
	racks := make([]rackResponse, 0)
	previousRow := 0
	for rows.Next() {
		var id int64
		var name string
		var code *string
		var row *int
		if err := rows.Scan(&id, &name, &code, &row); err != nil {
			writeErrorResponse(w, 500, "Не удалось загрузить стеллажи")
			return
		}
		if len(racks) == 0 || racks[len(racks)-1].ID != id {
			racks = append(racks, rackResponse{ID: id, Name: name, Rows: make([][]string, 0)})
			previousRow = 0
		}
		if code != nil && row != nil {
			rack := &racks[len(racks)-1]
			if previousRow != *row {
				rack.Rows = append(rack.Rows, []string{})
				previousRow = *row
			}
			rack.Rows[len(rack.Rows)-1] = append(rack.Rows[len(rack.Rows)-1], *code)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("load racks: %v", err)
		writeErrorResponse(w, 500, "Не удалось загрузить стеллажи")
		return
	}
	if err := json.NewEncoder(w).Encode(racks); err != nil {
		log.Printf("encode racks: %v", err)
	}
}
