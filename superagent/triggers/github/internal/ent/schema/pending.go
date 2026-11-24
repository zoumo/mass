package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Pending struct {
	ent.Schema
}

func (Pending) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("sequence"),
		field.String("repository"),
		field.String("message_id"),
		field.Bytes("payload"),
	}
}

func (Pending) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", Repository.Type).Ref("pending").Field("repository").Required().Unique().
			Annotations(entsql.OnDelete(entsql.NoAction)),
	}
}

func (Pending) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("repository", "message_id").Unique(),
		index.Fields("repository", "id").StorageKey("github_pending_fifo"),
	}
}

func (Pending) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "github_pending"}}
}
