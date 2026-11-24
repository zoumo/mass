package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Session struct {
	ent.Schema
}

func (Session) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "session"}}
}

func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("key"),
		field.String("session_id").Unique(),
		field.String("namespace"),
		field.String("name"),
		field.String("runtime_ref"),
		field.String("state"),
		field.String("sort_key"),
		field.Bytes("payload"),
	}
}

func (Session) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("namespace", "name").Unique().StorageKey("session_name"),
	}
}
