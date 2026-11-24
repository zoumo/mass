package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Submission describes a row in the DWS input queue.
type Submission struct {
	ent.Schema
}

func (Submission) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "dws_submissions"}}
}

func (Submission) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("sequence"),
		field.String("account_id"),
		field.String("native_message_id"),
		field.String("observed_at"),
		field.Bytes("payload").StructTag(`json:"-"`),
		field.String("failure").Optional().Nillable(),
	}
}

func (Submission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "native_message_id").Unique(),
		index.Fields("id").
			StorageKey("dws_pending_sequence").
			Annotations(entsql.IndexWhere("failure IS NULL")),
	}
}
