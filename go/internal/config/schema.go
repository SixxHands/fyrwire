package config

import (
	"reflect"
	"sort"
	"strings"

	"github.com/sixxhands/fyrwire/internal/model"
)

type fieldRule struct {
	kind string
	sub  map[string]fieldRule
}

func schemaOf(t reflect.Type) map[string]fieldRule {
	out := map[string]fieldRule{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("toml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		out[tag] = specOf(f.Type)
	}
	return out
}

func specOf(t reflect.Type) fieldRule {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return fieldRule{kind: "string"}
	case reflect.Bool:
		return fieldRule{kind: "bool"}
	case reflect.Int, reflect.Int64:
		return fieldRule{kind: "int"}
	case reflect.Float64:
		return fieldRule{kind: "float"}
	case reflect.Slice:
		return fieldRule{kind: "array", sub: schemaOf(t.Elem())}
	case reflect.Map:
		return fieldRule{kind: "table"}
	case reflect.Struct:
		return fieldRule{kind: "table", sub: schemaOf(t)}
	}
	return fieldRule{kind: ""}
}

func tomlKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case map[string]any:
		return "table"
	case []any, []map[string]any:
		return "array"
	}
	return "value"
}

func humanKind(k string) string {
	switch k {
	case "int":
		return "integer"
	case "bool":
		return "true or false"
	case "table":
		return "table"
	case "array":
		return "array of tables"
	}
	return k
}

func checkTable(where string, table map[string]any, spec map[string]fieldRule) error {
	names := make([]string, 0, len(spec))
	for k := range spec {
		names = append(names, k)
	}
	sort.Strings(names)

	for key, value := range table {
		want, ok := spec[key]
		if !ok {

			return model.Errorf("unknown key %s.%s%s", where, key,
				suggest(key, names))
		}
		got := tomlKind(value)
		if want.kind == "" || got == want.kind {

		} else if want.kind == "float" && got == "int" {

		} else if want.kind == "bool" {
			return model.Errorf("%s.%s must be true or false, got %v",
				where, key, value)
		} else {
			return model.Errorf("%s.%s must be a %s, got %s",
				where, key, humanKind(want.kind), humanKind(got))
		}
		if want.kind == "table" && want.sub != nil {
			nested, _ := value.(map[string]any)
			if err := checkTable(where+"."+key, nested, want.sub); err != nil {
				return err
			}
		}
	}
	return nil
}
