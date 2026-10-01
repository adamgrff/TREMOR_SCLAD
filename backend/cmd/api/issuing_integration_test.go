package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func issuingTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := receivingTestDB(t)
	if _, err := db.Exec(context.Background(), `INSERT INTO cell_stock (cell_id, product_id, quantity) VALUES (1, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func issueBody(request int, quantity int) string {
	return fmt.Sprintf(`{"requestId":"%08x-0000-4000-8000-000000000001","sku":"TEST-SKU","cellId":1,"quantity":%d}`, request, quantity)
}

func readTestIssue(t *testing.T, response *httptest.ResponseRecorder) issueResponse {
	t.Helper()
	var issue issueResponse
	if err := json.Unmarshal(response.Body.Bytes(), &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}

func issueCount(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM stock_issues`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestIssuingLifecycleAndRetries(t *testing.T) {
	db := issuingTestDB(t)
	w := receivingRequest(db, productStockHandler, "", "sku", 0)
	// Use the actual SKU path value rather than a session ID.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("sku", " test-sku ")
	w = httptest.NewRecorder()
	productStockHandler(db, w, r)
	requireReceivingStatus(t, w, http.StatusOK)
	var stock productStockResponse
	if err := json.Unmarshal(w.Body.Bytes(), &stock); err != nil {
		t.Fatal(err)
	}
	if stock.SKU != "TEST-SKU" || len(stock.Cells) != 1 || stock.Cells[0].CellID != 1 || stock.Cells[0].Quantity != 5 {
		t.Fatalf("product stock: %+v", stock)
	}
	w = receivingRequest(db, createIssueHandler, issueBody(1, 3), "", 0)
	requireReceivingStatus(t, w, http.StatusCreated)
	first := readTestIssue(t, w)
	w = receivingRequest(db, createIssueHandler, issueBody(1, 3), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	if retry := readTestIssue(t, w); retry.ID != first.ID || receivingStock(t, db) != 2 || issueCount(t, db) != 1 {
		t.Fatalf("retry deducted twice: %+v", retry)
	}
	w = receivingRequest(db, createIssueHandler, issueBody(1, 1), "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	w = receivingRequest(db, createIssueHandler, issueBody(2, 3), "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	if receivingStock(t, db) != 2 || issueCount(t, db) != 1 {
		t.Fatal("rejected issue changed stock/history")
	}
	w = receivingRequest(db, createIssueHandler, issueBody(2, 2), "", 0)
	requireReceivingStatus(t, w, http.StatusCreated)
	second := readTestIssue(t, w)
	w = receivingRequest(db, undoIssueHandler, "", "issueID", first.ID)
	requireReceivingStatus(t, w, http.StatusConflict)
	if receivingStock(t, db) != 0 {
		t.Fatal("undo of older issue restored stock")
	}
	w = receivingRequest(db, undoIssueHandler, "", "issueID", second.ID)
	requireReceivingStatus(t, w, http.StatusOK)
	undone := readTestIssue(t, w)
	if undone.Status != "undone" || undone.UndoneAt == nil {
		t.Fatalf("undo response: %+v", undone)
	}
	w = receivingRequest(db, undoIssueHandler, "", "issueID", second.ID)
	requireReceivingStatus(t, w, http.StatusOK)
	if receivingStock(t, db) != 2 {
		t.Fatal("undo retry restored twice")
	}
	w = receivingRequest(db, createIssueHandler, issueBody(2, 2), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	if readTestIssue(t, w).Status != "undone" || receivingStock(t, db) != 2 {
		t.Fatal("create retry reissued an undone operation")
	}
	w = receivingRequest(db, issueHistoryHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var history issueHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Issues) != 2 || history.Issues[0].ID != second.ID || history.UndoableID == nil || *history.UndoableID != first.ID {
		t.Fatalf("issue history: %+v", history)
	}
	w = receivingRequest(db, undoIssueHandler, "", "issueID", first.ID)
	requireReceivingStatus(t, w, http.StatusOK)
	if receivingStock(t, db) != 5 {
		t.Fatal("undo did not restore original quantity")
	}
}

func TestIssuingConcurrentRequests(t *testing.T) {
	for _, sameRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(sameRequest), func(t *testing.T) {
			db := issuingTestDB(t)
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			var workers sync.WaitGroup
			for index := range 2 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					request := index + 1
					if sameRequest {
						request = 1
					}
					results <- receivingRequest(db, createIssueHandler, issueBody(request, 4), "", 0)
				}()
			}
			close(start)
			workers.Wait()
			close(results)
			counts := map[int]int{}
			for response := range results {
				counts[response.Code]++
			}
			otherStatus := http.StatusConflict
			if sameRequest {
				otherStatus = http.StatusOK
			}
			if counts[http.StatusCreated] != 1 || counts[otherStatus] != 1 || receivingStock(t, db) != 1 || issueCount(t, db) != 1 {
				t.Fatalf("concurrent issue: %v, stock=%d, operations=%d", counts, receivingStock(t, db), issueCount(t, db))
			}
		})
	}
}

func TestIssuingRollsBackWhenHistoryInsertFails(t *testing.T) {
	db := issuingTestDB(t)
	if _, err := db.Exec(context.Background(), `ALTER TABLE stock_issues ADD CONSTRAINT test_reject_issue CHECK (quantity < 4)`); err != nil {
		t.Fatal(err)
	}
	w := receivingRequest(db, createIssueHandler, issueBody(1, 4), "", 0)
	requireReceivingStatus(t, w, http.StatusInternalServerError)
	if receivingStock(t, db) != 5 || issueCount(t, db) != 0 {
		t.Fatal("failed history insert left stock deducted")
	}
}

func TestIssuingUndoOverflowDoesNotChangeOperation(t *testing.T) {
	db := issuingTestDB(t)
	w := receivingRequest(db, createIssueHandler, issueBody(1, 1), "", 0)
	requireReceivingStatus(t, w, http.StatusCreated)
	issue := readTestIssue(t, w)
	if _, err := db.Exec(context.Background(), `UPDATE cell_stock SET quantity = 2147483647`); err != nil {
		t.Fatal(err)
	}
	w = receivingRequest(db, undoIssueHandler, "", "issueID", issue.ID)
	requireReceivingStatus(t, w, http.StatusConflict)
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM stock_issues WHERE id = $1`, issue.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if receivingStock(t, db) != 2147483647 || status != "active" {
		t.Fatal("failed undo partially changed stock or operation")
	}
}

func TestIssuingRejectsInvalidRequests(t *testing.T) {
	for _, body := range []string{`{}`, issueBody(1, 0), issueBody(1, -1), issueBody(1, 2147483648), issueBody(1, 1) + `{}`, `{"requestId":"bad","sku":"TEST-SKU","cellId":1,"quantity":1}`} {
		w := receivingRequest(nil, createIssueHandler, body, "", 0)
		requireReceivingStatus(t, w, http.StatusBadRequest)
	}
}
