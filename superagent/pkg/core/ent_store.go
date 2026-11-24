package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/zoumo/mass/massd/pkg/resource"
	coreent "github.com/zoumo/mass/superagent/pkg/core/internal/ent"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/issue"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/predicate"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/sessionbinding"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/thread"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/topic"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/triggermessage"
)

func newCoreEntClient(db *sql.DB) *coreent.Client {
	return coreent.NewClient(coreent.Driver(entsql.OpenDB(dialect.SQLite, db)))
}

// entClientForTx binds generated operations to the same connection as raw SQL.
// The caller owns the transaction outcome and must propagate any Ent write error.
func entClientForTx(tx *sql.Tx) *coreent.Client {
	conn := entsql.NewDriver(dialect.SQLite, entsql.Conn{ExecQuerier: tx})
	return coreent.NewClient(coreent.Driver(entTxDriver{Conn: conn.Conn}))
}

// Ent can request a transaction for a multi-statement operation. The caller's
// transaction already covers those statements and owns the connection lifecycle.
type entTxDriver struct{ entsql.Conn }

func (entTxDriver) Dialect() string { return dialect.SQLite }
func (entTxDriver) Close() error    { return nil }
func (d entTxDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}

func decodeEntityJSON[T any](body []byte, err error) (T, error) {
	var value T
	if coreent.IsNotFound(err) {
		return value, problem("not_found", "resource not found")
	}
	if err != nil || json.Unmarshal(body, &value) != nil {
		return value, storageError()
	}
	return value, nil
}

func readThreadRecord(ctx context.Context, tx *sql.Tx, id string) (json.RawMessage, error) {
	row, err := entClientForTx(tx).Thread.Query().Where(thread.BusinessIDEQ(id)).Only(ctx)
	if coreent.IsNotFound(err) {
		return nil, problem("not_found", "resource not found in current scope")
	}
	if err != nil {
		return nil, storageError()
	}
	var lastMessageAt *string
	if row.LastMessageAt != "" {
		lastMessageAt = &row.LastMessageAt
	}
	raw, err := json.Marshal(struct {
		ID            string          `json:"id"`
		CreatedAt     string          `json:"createdAt"`
		LastMessageAt *string         `json:"lastMessageAt"`
		Subscriptions json.RawMessage `json:"subscriptions"`
	}{row.BusinessID, row.CreatedAt, lastMessageAt, json.RawMessage(row.Subscriptions)})
	if err != nil {
		return nil, storageError()
	}
	return raw, nil
}

func readTopic(ctx context.Context, tx *sql.Tx, id string) (Topic, error) {
	row, err := entClientForTx(tx).Topic.Query().Where(topic.BusinessIDEQ(id)).Select(topic.FieldBody).Only(ctx)
	if err != nil {
		return decodeEntityJSON[Topic](nil, err)
	}
	return decodeEntityJSON[Topic](row.Body, nil)
}

func saveTopic(ctx context.Context, tx *sql.Tx, value Topic) error {
	body, err := json.Marshal(value)
	if err != nil {
		return storageError()
	}
	if err := entClientForTx(tx).Topic.Update().Where(topic.BusinessIDEQ(value.ID)).SetBody(body).Exec(ctx); err != nil {
		return storageError()
	}
	return nil
}

func readIssue(ctx context.Context, tx *sql.Tx, id string, filters ...predicate.Issue) (Issue, error) {
	row, err := entClientForTx(tx).Issue.Query().Where(issue.BusinessIDEQ(id)).Where(filters...).Select(issue.FieldBody).Only(ctx)
	if err != nil {
		return decodeEntityJSON[Issue](nil, err)
	}
	return decodeEntityJSON[Issue](row.Body, nil)
}

func readBinding(ctx context.Context, bindings *coreent.SessionBindingClient, filters ...predicate.SessionBinding) (SessionBinding, error) {
	row, err := bindings.Query().Where(filters...).Select(sessionbinding.FieldBody).Only(ctx)
	if err != nil {
		return decodeEntityJSON[SessionBinding](nil, err)
	}
	return decodeEntityJSON[SessionBinding](row.Body, nil)
}

func readBindingByID(ctx context.Context, tx *sql.Tx, id string) (SessionBinding, error) {
	return readBinding(ctx, entClientForTx(tx).SessionBinding, sessionbinding.BusinessIDEQ(id))
}

func readBindingSpec(ctx context.Context, tx *sql.Tx, id string) (resource.SessionSpec, error) {
	row, err := entClientForTx(tx).SessionBinding.Query().Where(sessionbinding.BusinessIDEQ(id)).Select(sessionbinding.FieldSpec).Only(ctx)
	if err != nil {
		return decodeEntityJSON[resource.SessionSpec](nil, err)
	}
	return decodeEntityJSON[resource.SessionSpec](row.Spec, nil)
}

func (s *Store) bindingCredentials(ctx context.Context, id string) (SessionBinding, resource.SessionSpec, string, error) {
	var binding SessionBinding
	var spec resource.SessionSpec
	row, err := s.entities.SessionBinding.Query().Where(sessionbinding.BusinessIDEQ(id)).
		Select(sessionbinding.FieldBody, sessionbinding.FieldSpec, sessionbinding.FieldToken).Only(ctx)
	if coreent.IsNotFound(err) {
		return binding, spec, "", problem("not_found", "resource not found")
	}
	if err != nil || json.Unmarshal(row.Body, &binding) != nil || json.Unmarshal(row.Spec, &spec) != nil {
		return binding, spec, "", storageError()
	}
	return binding, spec, row.Token, nil
}

func (s *Store) bindingTokens(ctx context.Context) ([]string, error) {
	return s.entities.SessionBinding.Query().Select(sessionbinding.FieldToken).Strings(ctx)
}

func saveBinding(ctx context.Context, tx *sql.Tx, b SessionBinding) error {
	body, err := json.Marshal(b)
	if err != nil {
		return storageError()
	}
	if err := entClientForTx(tx).SessionBinding.Update().Where(sessionbinding.BusinessIDEQ(b.ID)).
		SetState(b.State).SetNextAttemptAt(b.NextAttemptAt).SetBody(body).Exec(ctx); err != nil {
		return storageError()
	}
	return nil
}

func saveBindingBody(ctx context.Context, tx *sql.Tx, b SessionBinding) error {
	body, err := json.Marshal(b)
	if err != nil {
		return storageError()
	}
	if err := entClientForTx(tx).SessionBinding.Update().Where(sessionbinding.BusinessIDEQ(b.ID)).SetBody(body).Exec(ctx); err != nil {
		return storageError()
	}
	return nil
}

func triggerMessageByNativeID(messages *coreent.TriggerMessageClient, source, accountID, nativeID string) *coreent.TriggerMessageQuery {
	return messages.Query().Where(triggermessage.SourceEQ(source), triggermessage.AccountIDEQ(accountID), triggermessage.NativeIDEQ(nativeID))
}

func readTriggerMessage(ctx context.Context, query *coreent.TriggerMessageQuery) (Message, error) {
	row, err := query.Select(triggermessage.FieldBody).Only(ctx)
	if err != nil {
		return decodeEntityJSON[Message](nil, err)
	}
	return decodeEntityJSON[Message](row.Body, nil)
}

func (s *Store) replacePendingBindingSpec(ctx context.Context, id string, spec, body []byte) error {
	return s.entities.SessionBinding.Update().Where(sessionbinding.BusinessIDEQ(id), sessionbinding.StateEQ("pending")).
		SetSpec(spec).SetBody(body).SetNextAttemptAt("").Exec(ctx)
}

func (s *Store) bindingAndSpec(ctx context.Context, id string) (SessionBinding, resource.SessionSpec, error) {
	row, err := s.entities.SessionBinding.Query().Where(sessionbinding.BusinessIDEQ(id)).
		Select(sessionbinding.FieldBody, sessionbinding.FieldSpec).Only(ctx)
	if err != nil {
		binding, err := decodeEntityJSON[SessionBinding](nil, err)
		return binding, resource.SessionSpec{}, err
	}
	binding, err := decodeEntityJSON[SessionBinding](row.Body, nil)
	if err != nil {
		return binding, resource.SessionSpec{}, err
	}
	spec, err := decodeEntityJSON[resource.SessionSpec](row.Spec, nil)
	return binding, spec, err
}

func (s *Store) readyIssueBinding(ctx context.Context, id string) (SessionBinding, error) {
	return readBinding(ctx, s.entities.SessionBinding, sessionbinding.BusinessIDEQ(id), sessionbinding.RoleEQ("issue"), sessionbinding.StateEQ("ready"))
}
