// Command entgen generates the Core Ent client from its local schema.
package main

import (
	"log"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	storage, err := gen.NewStorage("sql")
	if err != nil {
		log.Fatal(err)
	}
	// Core initializes SQLite with versioned SQL. Omit Ent migration APIs.
	sqlStorage := *storage
	sqlStorage.SchemaMode &^= gen.Migrate
	if err := entc.Generate("./schema", &gen.Config{Storage: &sqlStorage}); err != nil {
		log.Fatal(err)
	}
}
