package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type homeReceivingStatistics struct {
	Sessions         int64  `json:"sessions"`
	ReceivedQuantity *int64 `json:"receivedQuantity"`
	PlacedQuantity   int64  `json:"placedQuantity"`
	PendingQuantity  int64  `json:"pendingQuantity"`
}

type homeLowStockItem struct {
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

type homeStockStatistics struct {
	TotalQuantity int64              `json:"totalQuantity"`
	OccupiedCells int64              `json:"occupiedCells"`
	TotalCells    int64              `json:"totalCells"`
	SKUCount      int64              `json:"skuCount"`
	LowStockCount int64              `json:"lowStockCount"`
	LowStockItems []homeLowStockItem `json:"lowStockItems"`
}

type homeAssemblyStatistics struct {
	CompletedOrders        *int64   `json:"completedOrders"`
	QueuedOrders           int64    `json:"queuedOrders"`
	ActiveOrders           int64    `json:"activeOrders"`
	ProblemOrders          int64    `json:"problemOrders"`
	AverageAssemblySeconds *float64 `json:"averageAssemblySeconds"`
}

type homeStatisticsResponse struct {
	Receiving homeReceivingStatistics `json:"receiving"`
	Stock     homeStockStatistics     `json:"stock"`
	Assembly  homeAssemblyStatistics  `json:"assembly"`
}

// Calendar periods include today in the site's timezone. The upper bound is
// the request time, so future-dated records cannot enter a current statistic.
func homeStatisticsPeriod(period string, now time.Time) (time.Time, bool) {
	days := 0
	switch period {
	case "", "today":
	case "7d":
		days = 6
	case "30d":
		days = 29
	default:
		return time.Time{}, false
	}
	local := now.In(time.FixedZone("Asia/Krasnoyarsk", 7*60*60))
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).AddDate(0, 0, -days), true
}

const homeSKUStockSQL = `WITH sku_stock AS (
	SELECT p.sku, COALESCE(SUM(s.quantity),0)::bigint AS quantity
	FROM products p LEFT JOIN cell_stock s ON s.product_id=p.id GROUP BY p.sku
) `

func readHomeStatistics(ctx context.Context, db *pgxpool.Pool, start, end time.Time) (homeStatisticsResponse, error) {
	var result homeStatisticsResponse
	result.Stock.LowStockItems = make([]homeLowStockItem, 0)
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM receiving_sessions WHERE status='completed' AND completed_at >= $1 AND completed_at < $2),
		(SELECT COALESCE(SUM(i.quantity),0)::bigint FROM receiving_placement_items i
		 JOIN receiving_placements p ON p.id=i.placement_id WHERE p.status='active' AND p.created_at >= $1 AND p.created_at < $2),
		(SELECT COALESCE(SUM(i.quantity),0)::bigint FROM receiving_session_items i
		 JOIN receiving_sessions s ON s.id=i.session_id WHERE s.status='active')`, start, end).
		Scan(&result.Receiving.Sessions, &result.Receiving.PlacedQuantity, &result.Receiving.PendingQuantity)
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, homeSKUStockSQL+`SELECT
		(SELECT COALESCE(SUM(quantity),0)::bigint FROM sku_stock),
		(SELECT COUNT(DISTINCT cell_id) FROM cell_stock WHERE quantity>0),
		(SELECT COUNT(*) FROM cells),
		(SELECT COUNT(*) FROM sku_stock WHERE quantity>0)`).
		Scan(&result.Stock.TotalQuantity, &result.Stock.OccupiedCells, &result.Stock.TotalCells, &result.Stock.SKUCount)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, homeSKUStockSQL+`SELECT sku,quantity FROM sku_stock WHERE quantity BETWEEN 1 AND 5 ORDER BY quantity,sku`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item homeLowStockItem
		if err = rows.Scan(&item.SKU, &item.Quantity); err != nil {
			rows.Close()
			return result, err
		}
		result.Stock.LowStockItems = append(result.Stock.LowStockItems, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Stock.LowStockCount = int64(len(result.Stock.LowStockItems))
	err = tx.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM assembly_order_records o JOIN assembly_order_heads h ON h.order_id=o.id WHERE o.source='ozon' AND o.status='queued'),
		(SELECT COUNT(*) FROM assembly_order_records WHERE source='ozon' AND status='in_progress'),
		(SELECT COUNT(DISTINCT p.order_id) FROM assembly_problem_records p
		 JOIN assembly_order_records o ON o.id=p.order_id WHERE o.source='ozon' AND p.created_at >= $1 AND p.created_at < $2)`, start, end).
		Scan(&result.Assembly.QueuedOrders, &result.Assembly.ActiveOrders, &result.Assembly.ProblemOrders)
	if err != nil {
		return result, err
	}
	// There is no complete intake ledger or authoritative assembly start/finish
	// timestamps. Keep these fields null rather than infer them from imports,
	// mutable statuses, or the first/last pick (see docs/home-statistics.md).
	return result, tx.Commit(ctx)
}

func homeStatisticsHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	now := time.Now()
	periods, exists := r.URL.Query()["period"]
	if exists && len(periods) != 1 {
		writeErrorResponse(w, http.StatusBadRequest, "period must be today, 7d or 30d")
		return
	}
	start, valid := homeStatisticsPeriod(r.URL.Query().Get("period"), now)
	if !valid {
		writeErrorResponse(w, http.StatusBadRequest, "period must be today, 7d or 30d")
		return
	}
	response, err := readHomeStatistics(r.Context(), db, start, now)
	if err != nil {
		log.Printf("failed to load home statistics: %v", err)
		writeErrorResponse(w, http.StatusInternalServerError, "failed to load home statistics")
		return
	}
	if err = json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to encode home statistics: %v", err)
	}
}
