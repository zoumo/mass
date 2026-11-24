package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SessionBinding maps session_bindings in core/schema/0028.sql.
type SessionBinding struct{ ent.Schema }

func (SessionBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("seq"),
		field.String("business_id").StorageKey("id").Unique().Immutable(),
		field.String("role").Immutable(),
		field.String("owner_id").Immutable(),
		field.Int("generation").Immutable(),
		field.String("created_at").Immutable(),
		field.String("token").Unique().Immutable().Sensitive(),
		field.Bytes("spec"),
		field.String("state"),
		field.String("next_attempt_at").Default(""),
		field.Bytes("body"),
	}
}

func (SessionBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("session_bindings"),
		// SQLite installs the session-state and JSON stop indexes.
		entsql.Skip(),
	}
}

func (SessionBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("role", "owner_id", "generation").Unique(),
		index.Fields("role").Unique().StorageKey("sole_router").
			Annotations(entsql.IndexWhere("role='router' AND state<>'revoked'")),
		index.Fields("owner_id").Unique().StorageKey("current_issue_session_binding").
			Annotations(entsql.IndexWhere("role='issue' AND state<>'revoked'")),
		index.Fields("business_id").StorageKey("session_bindings_stop").
			Annotations(entsql.IndexWhere("json_extract(body,'$.stop.status')='requested'")),
		index.Fields("next_attempt_at", "business_id").StorageKey("session_bindings_pending").
			Annotations(entsql.IndexWhere("state='pending'")),
		index.Fields("role", "owner_id", "business_id").StorageKey("session_bindings_ready_owner").
			Annotations(entsql.IndexWhere("state='ready'")),
	}
}
