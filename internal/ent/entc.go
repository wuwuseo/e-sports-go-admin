//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"log"
	"reflect"

	"entgo.io/ent/dialect/entsql"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"
)

func main() {
	config.InitConfig()
	dbConfig := orm.GetDatabaseConfig([]string{"default"}, config.NewConfig())
	err := entc.Generate("./schema", &gen.Config{
		Hooks: []gen.Hook{
			EnsureStructTag("json"),
			AddTablePrefix(dbConfig.TablePrefix),
		},
	})
	if err != nil {
		log.Fatalf("running ent codegen: %v", err)
	}
}

// EnsureStructTag 字段确保所有的字段都有标签
func EnsureStructTag(name string) gen.Hook {
	return func(next gen.Generator) gen.Generator {
		return gen.GenerateFunc(func(g *gen.Graph) error {
			for _, node := range g.Nodes {
				for _, field := range node.Fields {
					tag := reflect.StructTag(field.StructTag)
					if _, ok := tag.Lookup(name); !ok {
						return fmt.Errorf("struct tag %q is missing for field %s.%s", name, node.Name, field.Name)
					}
				}
			}
			return next.Generate(g)
		})
	}
}


func AddTablePrefix(prefix string) func(next gen.Generator) gen.Generator {
	return func(next gen.Generator) gen.Generator {
		return gen.GenerateFunc(func(g *gen.Graph) error {
			for _, n := range g.Nodes {
				// 获取现有 entsql 注解（如果有）
				var sqlAnnot *entsql.Annotation
				annotName := (&entsql.Annotation{}).Name() // 先获取注解名称
				
				if n.Annotations != nil {
					if annot, ok := n.Annotations[annotName]; ok {
						if a, ok := annot.(*entsql.Annotation); ok {
							sqlAnnot = a
						}
					}
				}
				
				if sqlAnnot == nil {
					sqlAnnot = &entsql.Annotation{}
				}

				sqlAnnot.Table = prefix + n.Table()

				if n.Annotations == nil {
					n.Annotations = gen.Annotations{}
				}
				n.Annotations[sqlAnnot.Name()] = sqlAnnot
			}
			return next.Generate(g)
		})
	}
}
