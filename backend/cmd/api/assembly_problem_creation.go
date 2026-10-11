package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type assemblyProblemDetails struct {
	Type        string `json:"type"`
	Comment     string `json:"comment"`
	PositionSKU string `json:"positionSku,omitempty"`
}
type assemblyProblemCreationRequest struct {
	RequestID   string                 `json:"requestId"`
	Version     int64                  `json:"version"`
	Source      string                 `json:"source"`
	Order       json.RawMessage        `json:"order,omitempty"`
	OrderNumber string                 `json:"orderNumber"`
	Details     assemblyProblemDetails `json:"details"`
}

var manualProblemLabels = map[string]string{
	"insufficient_stock": "Недостаточно товара", "missing_in_cell": "Товар отсутствует в ячейке",
	"unknown_sku": "Неизвестный товар / SKU", "damaged_product": "Повреждённый товар",
	"invalid_order": "Ошибка данных заказа", "other": "Другая проблема",
}

func createAssemblyProblemHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request assemblyProblemCreationRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Version < 0 || !assemblyDraftRequestID.MatchString(request.RequestID) || request.OrderNumber == "" || (request.Source != "queue" && request.Source != "assembly") || manualProblemLabels[request.Details.Type] == "" || len(request.Details.Comment) > 8000 || (request.Details.Type == "other" && strings.TrimSpace(request.Details.Comment) == "") || (request.Source == "assembly" && len(request.Order) > 0) {
		assemblyDraftError(w, 400, "invalid_problem_request", "Укажите корректные источник, причину, позицию и комментарий проблемы.")
		return
	}
	// Hash the intent, not server-generated timestamps or state; a retry replays exactly one creation.
	payload, _ := json.Marshal(request)
	saveAssemblyWorkspace(db, w, r, assemblyDraftRequest{RequestID: request.RequestID, Version: request.Version, State: payload}, &request)
}

type contextOrder struct {
	Number string `json:"number"`
	Items  []struct {
		SKU        string         `json:"sku"`
		Name       string         `json:"name"`
		Quantity   int            `json:"quantity"`
		Stock      *int           `json:"stock"`
		Cells      string         `json:"cells"`
		CellStocks map[string]int `json:"cellStocks"`
	} `json:"items"`
}
type contextPick struct {
	SKU  string `json:"sku"`
	Cell string `json:"cell"`
}

var contextCellPlan = regexp.MustCompile(`^(.+?)\s*×\s*(\d+)$`)

func buildAssemblyProblem(current json.RawMessage, request assemblyProblemCreationRequest, timestamp string) (json.RawMessage, error) {
	state := map[string]json.RawMessage{}
	if len(current) > 0 && string(current) != "null" {
		if err := json.Unmarshal(current, &state); err != nil {
			return nil, err
		}
	}
	empty := json.RawMessage(`{"picks":[],"confirmedCell":null,"shipmentConfirmed":false,"lastScan":"—","log":"","error":false}`)
	if len(state) == 0 {
		state = map[string]json.RawMessage{"schemaVersion": json.RawMessage(`1`), "activeOrder": json.RawMessage(`null`), "activeState": empty, "problemOrders": json.RawMessage(`[]`), "completedOrders": json.RawMessage(`[]`)}
	}
	orderRaw := request.Order
	active := len(state["activeOrder"]) > 0 && string(state["activeOrder"]) != "null"
	if request.Source == "assembly" {
		if !active {
			return nil, errors.New("Активная сборка уже изменилась. Загрузите актуальное состояние.")
		}
		orderRaw = state["activeOrder"]
	}
	var order contextOrder
	if json.Unmarshal(orderRaw, &order) != nil || order.Number != request.OrderNumber || len(order.Items) == 0 {
		return nil, errors.New("Некорректный состав заказа.")
	}
	var previous []map[string]json.RawMessage
	json.Unmarshal(state["problemOrders"], &previous)
	if request.Source == "queue" {
		var activeOrder contextOrder
		json.Unmarshal(state["activeOrder"], &activeOrder)
		if activeOrder.Number == order.Number {
			return nil, errors.New("Заказ уже находится в сборке.")
		}
		var completed []contextOrder
		json.Unmarshal(state["completedOrders"], &completed)
		for _, entry := range completed {
			if entry.Number == order.Number {
				return nil, errors.New("Заказ уже завершён.")
			}
		}
		for _, entry := range previous {
			var existing contextOrder
			json.Unmarshal(entry["order"], &existing)
			var issue struct {
				Status string `json:"resolutionStatus"`
			}
			json.Unmarshal(entry["problem"], &issue)
			if existing.Number == order.Number && issue.Status != "returned" {
				return nil, errors.New("Заказ уже находится в проблемных.")
			}
		}
	}
	scanState := empty
	if request.Source == "assembly" {
		scanState = state["activeState"]
	}
	var scan struct {
		Picks             []contextPick `json:"picks"`
		ConfirmedCell     *string       `json:"confirmedCell"`
		ShipmentConfirmed bool          `json:"shipmentConfirmed"`
	}
	if json.Unmarshal(scanState, &scan) != nil {
		return nil, errors.New("Некорректный прогресс сборки.")
	}
	if scan.Picks == nil {
		scan.Picks = []contextPick{}
	}
	counts := map[string]int{}
	cellCounts := map[string]int{}
	for _, pick := range scan.Picks {
		if pick.Cell == "" {
			return nil, errors.New("У отбора отсутствует исходная ячейка.")
		}
		counts[pick.SKU]++
		cellCounts[pick.SKU+"\x00"+pick.Cell]++
	}
	selectedFound := false
	total := 0
	positions := []map[string]any{}
	shortages := []map[string]any{}
	affected := []map[string]any{}
	type candidate struct {
		cell  string
		stock int
	}
	candidates := []candidate{}
	seen := map[string]bool{}
	for _, item := range order.Items {
		if item.SKU == "" || seen[item.SKU] || item.Quantity <= 0 || item.Quantity > 10000 || item.Stock == nil || *item.Stock < 0 || counts[item.SKU] > item.Quantity {
			return nil, errors.New("Некорректные позиции или количества заказа.")
		}
		seen[item.SKU] = true
		total += item.Quantity
		if item.SKU == request.Details.PositionSKU {
			selectedFound = true
		}
		picked := counts[item.SKU]
		remaining := item.Quantity - picked
		// Preview cell stocks include the saved picks even after a stock refresh/replan.
		// The scalar stock can then already exclude picks; never subtract from it twice.
		basis := *item.Stock
		if request.Source == "assembly" && len(item.CellStocks) > 0 {
			basis = 0
			for cell, quantity := range item.CellStocks {
				if cell == "" || quantity < 0 {
					return nil, errors.New("Некорректные остатки ячеек в сохранённом плане.")
				}
				basis += quantity
			}
		}
		available := max(0, basis-picked)
		missing := max(0, remaining-available)
		position := map[string]any{"sku": item.SKU, "name": item.Name, "required": item.Quantity, "picked": picked, "remaining": remaining, "available": available, "missing": missing, "cells": item.Cells, "cellStocks": item.CellStocks}
		positions = append(positions, position)
		isAffected := item.SKU == request.Details.PositionSKU || missing > 0 || (request.Details.PositionSKU == "" && request.Details.Type != "insufficient_stock")
		if isAffected {
			affected = append(affected, position)
		}
		if missing > 0 {
			shortages = append(shortages, map[string]any{"sku": item.SKU, "name": item.Name, "required": remaining, "available": available, "missing": missing, "cells": item.Cells})
		}
		for _, part := range strings.Split(item.Cells, "·") {
			match := contextCellPlan.FindStringSubmatch(strings.TrimSpace(part))
			if match == nil {
				continue
			}
			cell := strings.TrimSpace(match[1])
			needed, _ := strconv.Atoi(match[2])
			taken := cellCounts[item.SKU+"\x00"+cell]
			stock := item.CellStocks[cell] - taken
			if needed > taken && stock > 0 {
				candidates = append(candidates, candidate{cell, stock})
			}
		}
	}
	for sku := range counts {
		if !seen[sku] {
			return nil, errors.New("Отбор не соответствует составу заказа.")
		}
	}
	positional := request.Details.Type == "insufficient_stock" || request.Details.Type == "missing_in_cell" || request.Details.Type == "unknown_sku" || request.Details.Type == "damaged_product"
	if (request.Details.PositionSKU != "" && !selectedFound) || (positional && !selectedFound) || len(affected) == 0 {
		return nil, errors.New("Выберите затронутую позицию из состава заказа.")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].stock != candidates[j].stock {
			return candidates[i].stock < candidates[j].stock
		}
		return candidates[i].cell < candidates[j].cell
	})
	var suggested any
	if len(candidates) > 0 {
		suggested = candidates[0].cell
	}
	step := "cell"
	if len(scan.Picks) == total {
		step = "shipment"
	} else if scan.ConfirmedCell != nil {
		step = "product"
	}
	context := map[string]any{"schemaVersion": 1, "complete": true, "source": request.Source, "availabilitySource": "preview_snapshot", "createdAt": timestamp, "positions": positions, "affectedPositions": affected, "required": total, "picked": len(scan.Picks), "remaining": total - len(scan.Picks), "scanStep": step, "confirmedCell": scan.ConfirmedCell, "suggestedCell": suggested, "shipmentConfirmed": scan.ShipmentConfirmed, "picks": scan.Picks, "activeState": json.RawMessage(scanState)}
	title := "Обнаружена до начала сборки"
	message := "Отбор товара ещё не выполнялся."
	var assembly any
	if request.Source == "assembly" {
		title = "Проблема создана во время сборки"
		cell := "не подтверждена"
		if scan.ConfirmedCell != nil {
			cell = *scan.ConfirmedCell
		} else if len(scan.Picks) > 0 {
			cell = scan.Picks[len(scan.Picks)-1].Cell
		}
		position := "заказ целиком"
		picked := len(scan.Picks)
		required := total
		for _, item := range order.Items {
			if item.SKU == request.Details.PositionSKU {
				position = item.Name + " · " + item.SKU
				picked = counts[item.SKU]
				required = item.Quantity
			}
		}
		message = fmt.Sprintf("Ячейка: %s\nПозиция: %s\nНа момент проблемы собрано: %d / %d", cell, position, picked, required)
		assembly = map[string]any{"details": request.Details, "picks": scan.Picks, "cell": scan.ConfirmedCell, "shipmentConfirmed": scan.ShipmentConfirmed}
	}
	history := []map[string]any{{"createdAt": timestamp, "kind": "creation", "title": title, "message": message, "outcome": "neutral"}, {"createdAt": timestamp, "kind": "transfer", "title": "Заказ передан в проблемные", "message": manualProblemLabels[request.Details.Type], "outcome": "neutral"}}
	problem := map[string]any{"type": request.Details.Type, "comment": strings.TrimSpace(request.Details.Comment), "createdAt": timestamp, "resolutionStatus": "awaiting", "shortages": shortages, "history": history, "creationContext": context}
	if request.Details.PositionSKU != "" {
		problem["positionSku"] = request.Details.PositionSKU
	}
	entry := map[string]any{"order": json.RawMessage(orderRaw), "problem": problem, "status": "problem"}
	if assembly != nil {
		entry["assembly"] = assembly
		state["activeOrder"] = json.RawMessage(`null`)
		state["activeState"] = empty
	}
	entryRaw, _ := json.Marshal(entry)
	var entries []json.RawMessage
	json.Unmarshal(state["problemOrders"], &entries)
	entries = append(entries, entryRaw)
	state["problemOrders"], _ = json.Marshal(entries)
	return json.Marshal(state)
}
