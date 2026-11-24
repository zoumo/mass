// Command entgen generates an Ent client from the local SQLite schema.
package main

import (
	"flag"
	"log"
	"slices"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	upsert := flag.Bool("upsert", false, "generate upsert builders")
	flag.Parse()

	storage, err := gen.NewStorage("sql")
	if err != nil {
		log.Fatal(err)
	}
	// Stores initialize SQLite with versioned SQL. Omit Ent migration APIs.
	sqlStorage := *storage
	sqlStorage.SchemaMode &^= gen.Migrate
	// Store tests open the same databases as the application.
	//nolint:reassign // Ent exposes graph template selection through this variable.
	gen.GraphTemplates = slices.DeleteFunc(gen.GraphTemplates, func(t gen.GraphTemplate) bool {
		return t.Name == "enttest"
	})
	config := &gen.Config{Storage: &sqlStorage}
	if *upsert {
		config.Features = []gen.Feature{gen.FeatureUpsert}
	}
	if err := entc.Generate("./schema", config); err != nil {
		log.Fatal(err)
	}
}
