package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Thread maps threads in core/schema/0028.sql.
type Thread struct{ ent.Schema }

func (Thread) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("seq"),
		field.String("business_id").StorageKey("id").Unique().Immutable(),
		field.String("source").Immutable(),
		field.String("native_id").Immutable(),
		field.String("created_at").Immutable(),
		field.String("last_message_at"),
		field.String("subscriptions").Default("[]"),
	}
}

func (Thread) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("threads"),
		// SQLite owns the Thread upsert and the subscription view.
		entsql.Skip(),
	}
}

func (Thread) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source", "native_id").Unique(),
	}
}
