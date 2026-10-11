// Package assemblyorders is the input boundary for the existing assembly domain.
// A future Ozon transport converts its officially verified response into Order;
// it does not own separate orders, reservations, picks or problem states.
package assemblyorders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"regexp"
	"strings"
	"time"
)

type Item struct {
	Name       string         `json:"name"`
	SKU        string         `json:"sku"`
	OzonSKU    string         `json:"ozonSku,omitempty"`
	Quantity   int            `json:"quantity"`
	Stock      int            `json:"stock"`
	Cells      string         `json:"cells"`
	CellStocks map[string]int `json:"cellStocks"`
}
type Order struct {
	Source    string `json:"source"`
	Number    string `json:"number"`
	CreatedAt string `json:"createdAt"`
	Status    string `json:"status"`
	Items     []Item `json:"items"`
}
type Import struct {
	RequestID string `json:"requestId"`
	Order     Order  `json:"order"`
}
type OzonClient interface {
	FetchAssemblyOrders(context.Context) ([]Order, error)
}
type OzonConfig struct {
	ClientID string
	APIKey   string
}

func LoadOzonConfig() OzonConfig {
	return OzonConfig{os.Getenv("OZON_CLIENT_ID"), os.Getenv("OZON_API_KEY")}
}
func (c OzonConfig) Configured() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.APIKey) != ""
}

// Prevent accidentally printing credentials through ordinary diagnostic formatting.
func (c OzonConfig) String() string   { return "OzonConfig{credentials:redacted}" }
func (c OzonConfig) GoString() string { return c.String() }

type UnavailableOzonClient struct{}

func (UnavailableOzonClient) FetchAssemblyOrders(context.Context) ([]Order, error) {
	return nil, errors.New("Ozon не подключён: официальный контракт внешнего клиента ещё не подключён")
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (o Order) Validate() error {
	if o.Source != "ozon" || strings.TrimSpace(o.Number) == "" || len(o.Number) > 128 || len(o.Items) == 0 || len(o.Items) > 1000 {
		return errors.New("required: source=ozon, shipment number and items")
	}
	if _, err := time.Parse(time.RFC3339Nano, o.CreatedAt); err != nil {
		return errors.New("createdAt must be RFC3339")
	}
	seen := map[string]bool{}
	total := 0
	for _, p := range o.Items {
		key := strings.ToUpper(strings.TrimSpace(p.SKU))
		if key == "" || len(key) > 128 || seen[key] || p.Quantity <= 0 || p.Quantity > 10000 || strings.TrimSpace(p.Name) == "" {
			return errors.New("invalid or duplicate item")
		}
		seen[key] = true
		total += p.Quantity
	}
	if total > 10000 {
		return errors.New("too many units")
	}
	return nil
}

// ImportToDB is used by the local trusted importer and later by OzonClient.
// It publishes only queued orders; an in-progress composition cannot be replaced.
func ImportToDB(ctx context.Context, db *pgxpool.Pool, input Import) error {
	if !uuid.MatchString(input.RequestID) {
		return errors.New("requestId must be UUID")
	}
	if err := input.Order.Validate(); err != nil {
		return err
	}
	input.Order.Status = "Готов к сборке"
	for i := range input.Order.Items {
		p := &input.Order.Items[i]
		p.SKU = strings.ToUpper(strings.TrimSpace(p.SKU))
		p.Stock = 0
		p.Cells = ""
		p.CellStocks = map[string]int{}
	}
	payload, err := json.Marshal(input.Order)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_preview_drafts(id) VALUES(true) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	var lock int64
	if err = tx.QueryRow(ctx, `SELECT version FROM assembly_preview_drafts WHERE id=true FOR UPDATE`).Scan(&lock); err != nil {
		return err
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT payload=$2::jsonb FROM assembly_order_imports WHERE request_id=$1::uuid`, input.RequestID, payload).Scan(&same)
	if err == nil {
		if !same {
			return errors.New("requestId already used for different order")
		}
		return tx.Commit(ctx)
	}
	// No-row is expected; all other errors must abort rather than masking a DB failure.
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT status FROM assembly_order_records WHERE source='ozon' AND order_number=$1 FOR UPDATE`, input.Order.Number).Scan(&state)
	if err == nil && state != "queued" {
		return fmt.Errorf("order is %s; composition update requires its dedicated workflow", state)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var vid, oid string
	if err = tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, payload).Scan(&vid); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT order_id::text FROM assembly_order_versions WHERE id=$1::uuid`, vid).Scan(&oid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_order_heads(order_id,version_id) VALUES($1::uuid,$2::uuid) ON CONFLICT(order_id) DO UPDATE SET version_id=EXCLUDED.version_id,imported_at=clock_timestamp()`, oid, vid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_order_imports(request_id,order_id,version_id,payload) VALUES($1::uuid,$2::uuid,$3::uuid,$4::jsonb)`, input.RequestID, oid, vid, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
