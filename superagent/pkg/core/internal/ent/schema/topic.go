package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Topic maps topics in core/schema/0028.sql.
type Topic struct{ ent.Schema }

func (Topic) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("seq"),
		field.String("business_id").StorageKey("id").Unique().Optional().Immutable(),
		field.String("created_at").Immutable(),
		field.Bytes("body"),
		field.String("creation_key").Unique().Immutable(),
		field.String("creation_digest").Immutable(),
		field.Int64("issue_number").Default(0),
	}
}

func (Topic) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("topics"),
		// SQLite generates the business ID and increments the Issue number in place.
		entsql.Skip(),
	}
}

func (Topic) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at", "business_id").StorageKey("topics_created_at"),
	}
}
