//go:build ignore

package main

import (
	"log"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"github.com/lightsparkdev/spark/so/entcomments"
	"github.com/lightsparkdev/spark/so/entexample"
)

func main() {
	exampleExt := &entexample.Extension{}
	commentsExt := &entcomments.Extension{}

	err := entc.Generate("./schema", &gen.Config{
		Features: []gen.Feature{
			gen.FeatureIntercept,
			gen.FeatureExecQuery,
			gen.FeatureLock,
			gen.FeatureModifier,
			gen.FeatureUpsert,
		},
	}, entc.Extensions(exampleExt, commentsExt))

	if err != nil {
		log.Fatalf("running ent codegen: %v", err)
	}
}
