package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Outbox maps outbox in core/schema/0028.sql.
type Outbox struct{ ent.Schema }

func (Outbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable(),
		field.String("message_id").Immutable(),
		field.String("content").Immutable(),
		field.Enum("status").Values("pending", "sent", "failed"),
		field.String("receipt").Optional(),
		field.String("blocker_ref").Optional().Immutable(),
	}
}

func (Outbox) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("outbox"),
		// SQLite owns the JSON check, Message foreign key and delivery rowid.
		entsql.Skip(),
	}
}

func (Outbox) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("message_id").StorageKey("outbox_message"),
	}
}
