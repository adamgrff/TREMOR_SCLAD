package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReceivingHistoryCompletionTimeSort(t *testing.T) {
	db := receivingTestDB(t)
	// Completion dates deliberately run opposite to session numbers.
	_, err := db.Exec(context.Background(), `
		INSERT INTO receiving_sessions (status, completed_at)
		SELECT 'completed', NOW() - n * INTERVAL '1 hour' FROM generate_series(1, 24) AS n;
		UPDATE receiving_sessions SET deleted_at = NOW() WHERE id = 2;
		INSERT INTO receiving_sessions (status, completed_at) VALUES ('active', NULL);
	`)
	if err != nil {
		t.Fatal(err)
	}
	load := func(url string) completedReceivingHistoryResponse {
		t.Helper()
		w := httptest.NewRecorder()
		completedReceivingHistoryHandler(db, w, httptest.NewRequest(http.MethodGet, url, nil))
		requireReceivingStatus(t, w, http.StatusOK)
		var response completedReceivingHistoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	asc := load("/?order=asc")
	if len(asc.Sessions) != 5 || asc.Sessions[0].ID != 24 || asc.Sessions[1].ID != 23 || asc.NextCursor == nil || *asc.NextCursor != 20 {
		t.Fatalf("ascending first page: %+v", asc)
	}
	for i := 1; i < len(asc.Sessions); i++ {
		if asc.Sessions[i].CompletedAt.Before(asc.Sessions[i-1].CompletedAt) {
			t.Fatal("ascending order is incorrect")
		}
	}
	asc = load("/?order=asc&before=20")
	if len(asc.Sessions) != 5 || asc.Sessions[0].ID != 19 || asc.Sessions[4].ID != 15 || asc.NextCursor == nil || *asc.NextCursor != 15 {
		t.Fatalf("ascending next page: %+v", asc)
	}
	desc := load("/?order=desc")
	if len(desc.Sessions) != 5 || desc.Sessions[0].ID != 1 || desc.NextCursor == nil || *desc.NextCursor != 6 {
		t.Fatalf("descending first page: %+v", desc)
	}
	for i := 1; i < len(desc.Sessions); i++ {
		if desc.Sessions[i].CompletedAt.After(desc.Sessions[i-1].CompletedAt) {
			t.Fatal("descending order is incorrect")
		}
	}
	desc = load("/?order=desc&before=6")
	if len(desc.Sessions) != 5 || desc.Sessions[0].ID != 7 || desc.Sessions[4].ID != 11 || desc.NextCursor == nil || *desc.NextCursor != 11 {
		t.Fatalf("descending next page: %+v", desc)
	}
	// Walk every page, including the partial final page, in both directions.
	for _, order := range []string{"asc", "desc"} {
		seen := make(map[int64]bool)
		url := "/?order=" + order
		for pageNumber := 0; ; pageNumber++ {
			if pageNumber > 5 {
				t.Fatal("pagination did not terminate")
			}
			page := load(url)
			if len(page.Sessions) > receivingHistoryPageSize {
				t.Fatal("page exceeds receiving history limit")
			}
			for _, session := range page.Sessions {
				if seen[session.ID] || session.ID == 2 || session.ID == 25 {
					t.Fatalf("duplicate, hidden or active receiving: %d", session.ID)
				}
				seen[session.ID] = true
			}
			if page.NextCursor == nil {
				break
			}
			url = fmt.Sprintf("/?order=%s&before=%d", order, *page.NextCursor)
		}
		if len(seen) != 23 {
			t.Fatalf("%s pagination omitted sessions: got %d", order, len(seen))
		}
	}
}

func TestReceivingHistoryEqualCompletionTimesAscending(t *testing.T) {
	db := receivingTestDB(t)
	if _, err := db.Exec(context.Background(), `INSERT INTO receiving_sessions (status, completed_at)
		SELECT 'completed', '2026-10-01T00:00:00Z'::timestamptz FROM generate_series(1, 7)`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url string
		ids []int64
	}{
		{"/?order=asc", []int64{1, 2, 3, 4, 5}},
		{"/?order=asc&before=5", []int64{6, 7}},
	} {
		w := httptest.NewRecorder()
		completedReceivingHistoryHandler(db, w, httptest.NewRequest(http.MethodGet, tc.url, nil))
		requireReceivingStatus(t, w, http.StatusOK)
		var page completedReceivingHistoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) != len(tc.ids) {
			t.Fatalf("unexpected page: %+v", page)
		}
		for i, id := range tc.ids {
			if page.Sessions[i].ID != id {
				t.Fatalf("unstable equal-time sort: %+v", page)
			}
		}
	}
}

func TestReceivingHistoryRejectsInvalidSort(t *testing.T) {
	w := httptest.NewRecorder()
	completedReceivingHistoryHandler(nil, w, httptest.NewRequest(http.MethodGet, "/?order=invalid", nil))
	requireReceivingStatus(t, w, http.StatusBadRequest)
}
