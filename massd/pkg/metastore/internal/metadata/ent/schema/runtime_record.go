package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type RuntimeRecord struct {
	ent.Schema
}

func (RuntimeRecord) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "runtime"}}
}

func (RuntimeRecord) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("name"),
		field.String("type"),
		field.String("state"),
		field.Bytes("payload"),
	}
}
