package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TriggerMessage maps trigger_messages in core/schema/0028.sql.
type TriggerMessage struct{ ent.Schema }

func (TriggerMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("seq"),
		field.String("business_id").StorageKey("id").Unique().Optional().Immutable(),
		field.String("source").Immutable(),
		field.String("account_id").Immutable(),
		field.String("native_id").Immutable(),
		field.String("thread_id").Immutable(),
		field.String("accepted_at").Immutable(),
		field.Bytes("canonical").Immutable(),
		field.Bytes("body"),
		field.String("routing_state").Optional(),
		field.String("scheduled_task_id").Optional().Immutable(),
		field.Int("scheduled_task_revision").Optional().Immutable(),
		field.Enum("scheduled_trigger").Values("scheduled", "manual").Optional().Immutable(),
		field.String("scheduled_for").Optional().Immutable(),
	}
}

func (TriggerMessage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("trigger_messages"),
		// SQLite generates the business ID and enforces foreign keys to business IDs.
		entsql.Skip(),
	}
}

func (TriggerMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source", "account_id", "native_id").Unique(),
		index.Fields("thread_id", "accepted_at", "business_id").StorageKey("trigger_messages_thread"),
		index.Fields("scheduled_task_id", "accepted_at", "business_id").StorageKey("trigger_messages_scheduled_task").
			Annotations(entsql.IndexWhere("scheduled_task_id IS NOT NULL")),
		index.Fields("source", "accepted_at", "business_id").StorageKey("trigger_messages_source"),
	}
}
