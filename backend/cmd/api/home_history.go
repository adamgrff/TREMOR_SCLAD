package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type homeHistoryReference struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type homeHistoryEvent struct {
	ID          string                 `json:"id"`
	OccurredAt  time.Time              `json:"occurredAt"`
	Process     string                 `json:"process"`
	Event       string                 `json:"event"`
	Description string                 `json:"description"`
	References  []homeHistoryReference `json:"references"`
}

type homeHistoryResponse struct {
	Events []homeHistoryEvent `json:"events"`
}

// Each source records a fact, not an inferred current status. An undone
// placement remains a historical fact and is explicitly labelled as undone.
const homeHistorySQL = `WITH events AS (
 SELECT 'receiving-intake:'||i.id AS id, i.occurred_at, 'receiving' AS process,
 'Товар принят' AS event, 'Принято '||i.quantity||' ед. товара' AS description,
 jsonb_build_array(jsonb_build_object('type','sku','value',p.sku)) AS refs
 FROM receiving_intake_events i JOIN products p ON p.id=i.product_id
 UNION ALL
 SELECT 'receiving-placement:'||p.id, p.created_at, 'receiving', 'Товар размещён',
 'Размещено '||SUM(i.quantity)||' ед. в ячейке '||c.code||CASE WHEN p.status='undone' THEN ' (размещение отменено)' ELSE '' END,
 jsonb_agg(jsonb_build_object('type','sku','value',s.sku) ORDER BY s.sku)
 ||jsonb_build_array(jsonb_build_object('type','cell','value',c.code))
 FROM receiving_placements p JOIN receiving_placement_items i ON i.placement_id=p.id
 JOIN products s ON s.id=i.product_id JOIN cells c ON c.id=p.cell_id
 GROUP BY p.id,c.id
 UNION ALL
 SELECT 'assembly-lifecycle:'||e.id, e.occurred_at, 'assembly',
 CASE e.event_type WHEN 'started' THEN 'Начата сборка' ELSE 'Заказ собран' END,
 CASE e.event_type WHEN 'started' THEN 'Заказ передан в активную сборку' ELSE 'Сборка заказа завершена' END,
 jsonb_build_array(jsonb_build_object('type','order','value',o.order_number))
 FROM assembly_lifecycle_events e JOIN assembly_order_records o ON o.id=e.order_id WHERE o.source='ozon'
 UNION ALL
 SELECT 'assembly-problem:'||p.id, p.created_at, 'assembly', 'Заказ переведён в проблемные',
 CASE p.problem_type
 WHEN 'insufficient_stock' THEN 'Недостаточно товара для сборки'
 WHEN 'missing_in_cell' THEN 'Товар не найден в ячейке'
 WHEN 'unknown_sku' THEN 'Неизвестный SKU'
 WHEN 'damaged_product' THEN 'Повреждённый товар'
 WHEN 'invalid_order' THEN 'Ошибка данных заказа'
 WHEN 'cancelled_order' THEN 'Заказ отменён'
 WHEN 'ozon_error' THEN 'Требуется проверка операции Ozon'
 ELSE 'Заказ требует проверки' END,
 jsonb_build_array(jsonb_build_object('type','order','value',o.order_number))
 FROM assembly_problem_records p JOIN assembly_order_records o ON o.id=p.order_id WHERE o.source='ozon'
 UNION ALL
 SELECT 'ozon-observation:'||e.id, e.observed_at, 'ozon',
 CASE e.outcome WHEN 'applied' THEN 'Операция Ozon подтверждена'
 WHEN 'rejected' THEN 'Операция Ozon отклонена' ELSE 'Отмена заказа подтверждена' END,
 CASE o.purpose WHEN 'confirm_shipment' THEN 'Получен результат подтверждения отгрузки'
 ELSE 'Получен результат проверки заказа' END,
 jsonb_build_array(jsonb_build_object('type','order','value',r.order_number))
 FROM assembly_ozon_observations e JOIN assembly_ozon_operations o ON o.id=e.operation_id
 JOIN assembly_order_records r ON r.id=o.order_id
 WHERE r.source='ozon' AND e.outcome IN ('applied','rejected','cancelled')
 ) SELECT id,occurred_at,process,event,description,refs FROM events
 ORDER BY occurred_at DESC,id DESC LIMIT $1`

func readHomeHistory(ctx context.Context, db *pgxpool.Pool, limit int) (homeHistoryResponse, error) {
	result := homeHistoryResponse{Events: make([]homeHistoryEvent, 0)}
	// One statement gives all sources the same PostgreSQL snapshot.
	rows, err := db.Query(ctx, homeHistorySQL, limit)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var event homeHistoryEvent
		var refs []byte
		if err = rows.Scan(&event.ID, &event.OccurredAt, &event.Process, &event.Event, &event.Description, &refs); err != nil {
			return result, err
		}
		if err = json.Unmarshal(refs, &event.References); err != nil {
			return result, err
		}
		result.Events = append(result.Events, event)
	}
	return result, rows.Err()
}

func homeHistoryHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	limit := 20
	if values, exists := r.URL.Query()["limit"]; exists {
		var err error
		if len(values) == 1 {
			limit, err = strconv.Atoi(values[0])
		}
		if len(values) != 1 || err != nil || limit < 1 || limit > 100 {
			writeErrorResponse(w, http.StatusBadRequest, "limit must be an integer from 1 to 100")
			return
		}
	}
	result, err := readHomeHistory(r.Context(), db, limit)
	if err != nil {
		log.Printf("failed to load home history: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load home history")
		return
	}
	if err = json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("failed to encode home history: %v", err)
	}
}
