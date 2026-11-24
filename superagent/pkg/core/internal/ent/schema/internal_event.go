package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// InternalEvent maps internal_events in core/schema/0028.sql.
type InternalEvent struct{ ent.Schema }

func (InternalEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("event_id").Immutable(),
		field.String("kind").Immutable(),
		field.String("sender").Immutable(),
		field.String("recipient").Immutable(),
		field.String("content").Optional().Immutable(),
		field.String("body").Optional().Immutable(),
		field.String("refs").Default("[]").Immutable(),
		field.String("created_at").Immutable(),
		field.Enum("status").Values("pending", "dispatching", "sent", "superseded"),
		field.String("sent_at").Optional(),
	}
}

func (InternalEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("internal_events"),
		// SQLite owns the JSON checks, expression indexes and routing rowid.
		entsql.Skip(),
	}
}
