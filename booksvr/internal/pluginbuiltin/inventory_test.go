package pluginbuiltin

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestNativeSpecificationInventoryMatchesRuntimeContract(t *testing.T) {
	raw, err := os.ReadFile("builtin_ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, expectedBuiltinIDs) {
		t.Fatal("spec inventory differs from tested runtime registration")
	}
}
