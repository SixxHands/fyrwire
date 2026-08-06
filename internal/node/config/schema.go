package config

import (
	"reflect"
	"sort"
	"strings"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

type fieldRule struct {
	kind string
	sub  map[string]fieldRule
}

var humanKindNames = map[string]string{
	"int":   "integer",
	"bool":  "true or false",
	"table": "table",
	"array": "array of tables",
}

var reflectKindTable = map[reflect.Kind]string{
	reflect.String:  "string",
	reflect.Bool:    "bool",
	reflect.Int:     "int",
	reflect.Int64:   "int",
	reflect.Float64: "float",
	reflect.Slice:   "array",
	reflect.Map:     "table",
	reflect.Struct:  "table",
}

func schemaOf(structType reflect.Type) map[string]fieldRule {
	rules := map[string]fieldRule{}
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		tag := strings.Split(field.Tag.Get("toml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}

		rules[tag] = buildFieldRule(field.Type)
	}

	return rules
}

func buildFieldRule(fieldType reflect.Type) fieldRule {
	for fieldType.Kind() == reflect.Ptr {
		fieldType = fieldType.Elem()
	}

	kind := reflectKindTable[fieldType.Kind()]
	if fieldType.Kind() == reflect.Slice {
		return fieldRule{kind: kind, sub: schemaOf(fieldType.Elem())}
	}

	if fieldType.Kind() == reflect.Struct {
		return fieldRule{kind: kind, sub: schemaOf(fieldType)}
	}

	return fieldRule{kind: kind}
}

func tomlKind(value any) string {
	if value == nil {
		return "value"
	}

	kind, ok := reflectKindTable[reflect.TypeOf(value).Kind()]
	if !ok {
		return "value"
	}

	return kind
}

func humanKind(kind string) string {
	if name, ok := humanKindNames[kind]; ok {
		return name
	}

	return kind
}

func checkTable(where string, table map[string]any, rules map[string]fieldRule) error {
	names := make([]string, 0, len(rules))
	for key := range rules {
		names = append(names, key)
	}
	sort.Strings(names)

	for key, value := range table {
		expected, ok := rules[key]
		if !ok {
			return model.Errorf("unknown key %s.%s%s", where, key, suggest(key, names))
		}

		if err := checkFieldValue(where, key, value, expected); err != nil {
			return err
		}
	}

	return nil
}

func checkFieldValue(where, key string, value any, expected fieldRule) error {
	actual := tomlKind(value)
	if expected.kind != "" && actual != expected.kind && !(expected.kind == "float" && actual == "int") {
		if expected.kind == "bool" {
			return model.Errorf("%s.%s must be true or false, got %v", where, key, value)
		}

		return model.Errorf("%s.%s must be a %s, got %s",
			where, key, humanKind(expected.kind), humanKind(actual))
	}

	if expected.kind == "table" && expected.sub != nil {
		nested, _ := value.(map[string]any)
		return checkTable(where+"."+key, nested, expected.sub)
	}

	return nil
}
