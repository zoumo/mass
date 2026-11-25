package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Issue maps issues in core/schema/0028.sql.
type Issue struct{ ent.Schema }

func (Issue) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("seq"),
		field.String("topic_id").Immutable(),
		field.Int64("number").Positive().Immutable(),
		field.String("business_id").StorageKey("id").Unique().Optional().Immutable(),
		field.String("created_at").Immutable(),
		field.Bytes("body"),
		field.String("creation_key").Unique().Immutable(),
		field.String("creation_digest").Immutable(),
		field.String("report_creations").Default("{}"),
	}
}

func (Issue) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("issues"),
		// SQLite generates the business ID, enforces the Topic foreign key, and indexes JSON status.
		entsql.Skip(),
	}
}

func (Issue) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("topic_id", "number").Unique(),
		index.Fields("topic_id", "created_at", "business_id").StorageKey("issues_topic"),
		index.Fields("topic_id").StorageKey("issues_unfinished").
			Annotations(entsql.IndexWhere("json_extract(body,'$.status') NOT IN ('completed','canceled')")),
	}
}
