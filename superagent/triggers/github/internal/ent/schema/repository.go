package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Repository struct {
	ent.Schema
}

func (Repository) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("name"),
		field.Int("backlog").Default(0),
		field.Uint64("accepted").Default(0),
		field.Uint64("duplicates").Default(0),
		field.Bytes("last_receipt").Default([]byte("{}")).Annotations(entsql.Default("{}")),
		field.String("last_delivery_error").Default(""),
		field.Int64("next_delivery_at").Default(0),
		field.Int("delivery_failures").Default(0),
	}
}

func (Repository) Edges() []ent.Edge {
	return []ent.Edge{edge.To("pending", Pending.Type)}
}

func (Repository) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "github_repositories"},
		entsql.Check("backlog >= 0 AND accepted >= 0 AND duplicates >= 0 AND delivery_failures >= 0"),
	}
}
