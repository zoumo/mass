package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type RateLimit struct {
	ent.Schema
}

func (RateLimit) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Annotations(entsql.Annotation{Incremental: new(false)}),
		field.Int64("pause_until"),
	}
}

func (RateLimit) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "github_rate_limit"}, entsql.Check("id = 1")}
}
