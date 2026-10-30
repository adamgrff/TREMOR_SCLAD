package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReceivingActiveDiscovery(t *testing.T) {
	db := receivingTestDB(t)
	list := func() []activeReceivingSession {
		t.Helper()
		before := receivingOperationFingerprint(t, db)
		w := httptest.NewRecorder()
		activeReceivingSessionsHandler(db, w, httptest.NewRequest("GET", "/api/receiving/sessions/active", nil))
		requireReceivingStatus(t, w, 200)
		var result []activeReceivingSession
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatal("null list")
		}
		if before != receivingOperationFingerprint(t, db) {
			t.Fatal("GET changed data")
		}
		return result
	}
	if len(list()) != 0 {
		t.Fatal("expected no active sessions")
	}
	first := receivingSession(t, db)
	receivingAdd(t, db, first, 3)
	result := list()
	if len(result) != 1 || result[0].ID != first || result[0].PendingQuantity != 3 {
		t.Fatal(result)
	}
	receivingPlace(t, db, first, 3)
	receivingAdd(t, db, first, 1)
	second := receivingSession(t, db)
	result = list()
	if len(result) != 2 || result[0].ID != first || result[1].ID != second || result[0].PendingQuantity != 1 || result[0].PlacementCount != 1 {
		t.Fatal(result)
	}
	if _, err := db.Exec(context.Background(), `UPDATE receiving_sessions SET started_at='2026-01-01' WHERE id=$1 OR id=$2`, first, second); err != nil {
		t.Fatal(err)
	}
	result = list()
	if result[0].ID != first || result[1].ID != second {
		t.Fatal("unstable tie order")
	}
	if _, err := db.Exec(context.Background(), `UPDATE receiving_sessions SET status='completed',completed_at=now(),deleted_at=now() WHERE id=$1`, second); err != nil {
		t.Fatal(err)
	}
	if len(list()) != 1 {
		t.Fatal("deleted session visible")
	}
	if _, err := db.Exec(context.Background(), `UPDATE receiving_sessions SET status='completed',completed_at=now() WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	if len(list()) != 0 {
		t.Fatal("completed session visible")
	}
}

// Pause the actual GET after its header SELECT established a snapshot, then
// commit a placement on another connection before GET reads pending/placed.
type receivingSnapshotPause struct {
	reached, resume chan struct{}
	once            sync.Once
}
type receivingSnapshotKey struct{}

func (p *receivingSnapshotPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, receivingSnapshotKey{}, strings.Contains(data.SQL, "pending_revision::text"))
}
func (p *receivingSnapshotPause) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(receivingSnapshotKey{}) == true {
		p.once.Do(func() {
			close(p.reached)
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
}
func TestReceivingSnapshotConcurrentPlacement(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 2)
	before := receivingDetails(t, db, session)
	pause := &receivingSnapshotPause{reached: make(chan struct{}), resume: make(chan struct{})}
	config := db.Config()
	config.ConnConfig.Tracer = pause
	reader, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		r.SetPathValue("sessionID", strconv.FormatInt(session, 10))
		w := httptest.NewRecorder()
		getReceivingSessionHandler(reader, w, r)
		done <- w
	}()
	select {
	case <-pause.reached:
	case <-ctx.Done():
		t.Fatal("GET did not reach snapshot barrier")
	}
	receivingPlace(t, db, session, 2)
	close(pause.resume)
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-ctx.Done():
		t.Fatal("GET stalled")
	}
	requireReceivingStatus(t, w, 200)
	var snapshot receivingSessionDetailsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PendingRevision != before.PendingRevision || len(snapshot.PendingItems) != 1 || snapshot.PendingItems[0].Quantity != 2 || len(snapshot.PlacedItems) != 0 {
		t.Fatalf("mixed snapshot: %+v", snapshot)
	}
	after := receivingDetails(t, db, session)
	if after.PendingRevision == before.PendingRevision || len(after.PendingItems) != 0 || len(after.PlacedItems) != 1 {
		t.Fatalf("new snapshot: %+v", after)
	}
}
