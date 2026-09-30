package openapi

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/contractgen/runtimeinventorygen"
	"gopkg.in/yaml.v3"
)

func TestRuntimeInventoryOpenAPIExactlyMatchesCanonicalSchema(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "contracts", "runtimeinventory", "v1", "schema.json")
	contract, _, err := runtimeinventorygen.Load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	components := inventoryContractMap(t, document, "components")
	schemas := inventoryContractMap(t, components, "schemas")
	expectedNames := make(map[string]bool, len(contract.Types))
	for _, typ := range contract.Types {
		openAPIName := "Inventory" + typ.Name
		expectedNames[openAPIName] = true
		published := inventoryContractMap(t, schemas, openAPIName)
		properties := inventoryContractMap(t, published, "properties")
		if len(properties) != len(typ.Fields) {
			t.Errorf("%s publishes %d properties, canonical schema has %d", openAPIName, len(properties), len(typ.Fields))
		}
		expectedRequired := map[string]bool{}
		for _, field := range typ.Fields {
			if !field.OmitEmpty {
				expectedRequired[field.JSON] = true
			}
			property, ok := properties[field.JSON].(map[string]any)
			if !ok {
				t.Errorf("%s is missing canonical JSON field %q", openAPIName, field.JSON)
				continue
			}
			assertRuntimeInventoryOpenAPIType(t, openAPIName+"."+field.JSON, field.GoType, field.OmitEmpty, property)
		}
		actualRequired := inventoryOptionalStringSet(t, published["required"])
		if !reflect.DeepEqual(actualRequired, expectedRequired) {
			t.Errorf("%s required fields = %v, canonical schema requires %v", openAPIName, actualRequired, expectedRequired)
		}
	}
	for name := range schemas {
		if strings.HasPrefix(name, "Inventory") && !expectedNames[name] {
			t.Errorf("OpenAPI publishes non-canonical Runtime Inventory schema %q", name)
		}
	}
}

func assertRuntimeInventoryOpenAPIType(t *testing.T, name, goType string, omitempty bool, property map[string]any) {
	t.Helper()
	base := goType
	pointer := strings.HasPrefix(base, "*")
	array := strings.HasPrefix(base, "[]")
	if pointer {
		base = strings.TrimPrefix(base, "*")
	} else if array {
		base = strings.TrimPrefix(base, "[]")
	}
	nullable := pointer && !omitempty
	shape := property
	if oneOf, exists := property["oneOf"]; exists {
		variants, ok := oneOf.([]any)
		if !ok || len(variants) != 2 {
			t.Errorf("%s oneOf = %#v, want canonical value plus null", name, oneOf)
			return
		}
		var value map[string]any
		seenNull := false
		for _, raw := range variants {
			variant, _ := raw.(map[string]any)
			if variant["type"] == "null" {
				seenNull = true
			} else {
				value = variant
			}
		}
		if !nullable || !seenNull || value == nil {
			t.Errorf("%s nullability does not match canonical Go field %s", name, goType)
			return
		}
		shape = value
	} else if nullable {
		t.Errorf("%s must allow null for required pointer field %s", name, goType)
	}
	if array {
		if shape["type"] != "array" {
			t.Errorf("%s type = %#v, want array", name, shape)
			return
		}
		items, _ := shape["items"].(map[string]any)
		assertRuntimeInventoryOpenAPIBase(t, name+"[]", base, items)
		return
	}
	assertRuntimeInventoryOpenAPIBase(t, name, base, shape)
}

func assertRuntimeInventoryOpenAPIBase(t *testing.T, name, base string, shape map[string]any) {
	t.Helper()
	switch base {
	case "string":
		if shape["type"] != "string" {
			t.Errorf("%s type = %#v, want string", name, shape)
		}
	case "int":
		if shape["type"] != "integer" || shape["format"] != nil {
			t.Errorf("%s type = %#v, want integer without format", name, shape)
		}
	case "int64":
		if shape["type"] != "integer" || shape["format"] != "int64" {
			t.Errorf("%s type = %#v, want integer/int64", name, shape)
		}
	case "bool":
		if shape["type"] != "boolean" {
			t.Errorf("%s type = %#v, want boolean", name, shape)
		}
	case "time.Time":
		if shape["type"] != "string" || shape["format"] != "date-time" {
			t.Errorf("%s type = %#v, want string/date-time", name, shape)
		}
	default:
		want := "#/components/schemas/Inventory" + base
		if shape["$ref"] != want {
			t.Errorf("%s reference = %#v, want %s", name, shape, want)
		}
	}
}

func inventoryOptionalStringSet(t *testing.T, raw any) map[string]bool {
	t.Helper()
	if raw == nil {
		return map[string]bool{}
	}
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("required = %#v, want array", raw)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[fmt.Sprint(value)] = true
	}
	return result
}
func inventoryContractMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %#v, want object", key, parent[key])
	}
	return value
}

func inventoryContractStringSet(t *testing.T, parent map[string]any, key string) map[string]bool {
	t.Helper()
	values, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%q = %#v, want array", key, parent[key])
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[fmt.Sprint(value)] = true
	}
	return result
}
