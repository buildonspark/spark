package ent

import (
	"context"
	"encoding/json"
	"fmt"
)

// contextKey is a type for context keys.
type (
	dbSessionContextKey  string
	dbNotifierContextKey string
)

// dbSessionKey is the context key for the transaction provider.
const (
	dbSessionKey  dbSessionContextKey  = "dbsession"
	dbNotifierKey dbNotifierContextKey = "dbnotifier"
)

// A TxProvider is an interface that provides a method to either get an existing transaction,
// or begin a new transaction if none exists.
type TxProvider interface {
	// Get the current transaction from the context, or begin a new one if none exists.
	GetOrBeginTx(context.Context) (*Tx, error)
	// Get a client that may be backed by a transaction
	GetClient(context.Context) (*Client, error)
}

type Session interface {
	TxProvider
	MarkTxDirty(context.Context)
}

// ClientTxProvider is a TxProvider that uses an underlying ent.Client to create new transactions. This always
// returns a new transaction.
type ClientTxProvider struct {
	dbClient *Client
}

func NewEntClientTxProvider(dbClient *Client) *ClientTxProvider {
	return &ClientTxProvider{dbClient: dbClient}
}

func (e *ClientTxProvider) GetOrBeginTx(ctx context.Context) (*Tx, error) {
	tx, err := e.dbClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	return tx, nil
}

func (e *ClientTxProvider) GetClient(ctx context.Context) (*Client, error) {
	tx, err := e.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

// Inject the transaction provider into the context. This should ONLY be called from the start of
// a request or worker context (e.g. in a top-level gRPC interceptor).
func Inject(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, dbSessionKey, session)
}

// GetDbFromContext returns the database client from the context. The client may be backed by a transaction.
func GetDbFromContext(ctx context.Context) (*Client, error) {
	if txProvider, ok := ctx.Value(dbSessionKey).(TxProvider); ok {
		return txProvider.GetClient(ctx)
	}

	return nil, fmt.Errorf("no transaction provider found in context")
}

// GetTxFromContext returns the underlying database transaction from the context.
// This should only be used where explicit transaction commit/rollback is needed.
func GetTxFromContext(ctx context.Context) (*Tx, error) {
	if txProvider, ok := ctx.Value(dbSessionKey).(TxProvider); ok {
		return txProvider.GetOrBeginTx(ctx)
	}

	return nil, fmt.Errorf("no transaction provider found in context")
}

func MarkTxDirty(ctx context.Context) {
	if session, ok := ctx.Value(dbSessionKey).(Session); ok {
		session.MarkTxDirty(ctx)
	}
}

// DbCommit gets the transaction from the context and commits it.
func DbCommit(ctx context.Context) error {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get transaction from context: %w", err)
	}

	if tx == nil {
		return fmt.Errorf("no transaction found in context")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// DbRollback gets the transaction from the context and rolls it back.
func DbRollback(ctx context.Context) error {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get transaction from context: %w", err)
	}

	if tx == nil {
		return fmt.Errorf("no transaction found in context")
	}

	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("failed to rollback transaction: %w", err)
	}

	return nil
}

type Notification struct {
	Channel string
	Payload map[string]any
}

type Notifier interface {
	Notify(context.Context, Notification) error
}

func InjectNotifier(ctx context.Context, notifier Notifier) context.Context {
	return context.WithValue(ctx, dbNotifierKey, notifier)
}

func GetNotifierFromContext(ctx context.Context) (Notifier, error) {
	if notifier, ok := ctx.Value(dbNotifierKey).(Notifier); ok {
		return notifier, nil
	}

	return nil, fmt.Errorf("no notifier found in context")
}

type BufferedNotifier struct {
	dbClient      *Client
	notifications []Notification
}

func NewBufferedNotifier(dbClient *Client) BufferedNotifier {
	return BufferedNotifier{
		dbClient:      dbClient,
		notifications: make([]Notification, 0),
	}
}

func (b *BufferedNotifier) Notify(ctx context.Context, n Notification) error {
	b.notifications = append(b.notifications, n)
	return nil
}

func (b *BufferedNotifier) Flush(ctx context.Context) error {
	if len(b.notifications) == 0 {
		return nil
	}

	for _, n := range b.notifications {
		// Serialize as JSON before sending to Postgres
		jsonPayload, err := json.Marshal(n.Payload)
		if err != nil {
			return fmt.Errorf("failed to marshal notification payload: %w", err)
		}

		// nolint:forbidigo
		_, err = b.dbClient.ExecContext(ctx, fmt.Sprintf("NOTIFY %s, '%s'", n.Channel, string(jsonPayload)))
		if err != nil {
			return fmt.Errorf("failed to send notification: %w", err)
		}
	}

	return nil
}
