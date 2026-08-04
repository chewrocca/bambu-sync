package bambu

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// dev_access_code is the printer's LAN credential. The API returns it on every
// device; this exporter must never carry it, because a metric label would put
// it into Grafana Cloud in plaintext, indexed, effectively forever.
//
// Not mapping the field is stronger than mapping-and-not-using it: the value
// is discarded at unmarshal and never reaches the metrics layer at all. This
// test fails if anyone ever adds it back.
func TestDeviceNeverCarriesAccessCode(t *testing.T) {
	const secret = "SUPER-SECRET-LAN-CODE"
	payload := `{"devices":[{
		"dev_id":"20P6AJ641300919",
		"name":"3DP-20P-919",
		"dev_product_name":"X2D",
		"dev_model_name":"N6-V2",
		"dev_structure":"CoreXY",
		"nozzle_diameter":0.4,
		"online":true,
		"dev_access_code":"` + secret + `"
	}]}`

	var out BindResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(out.Devices))
	}

	// Nothing on the struct may hold it, whatever its field name.
	v := reflect.ValueOf(out.Devices[0])
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.String && strings.Contains(f.String(), secret) {
			t.Fatalf("field %q carries the access code -- it must not be mapped at all",
				v.Type().Field(i).Name)
		}
	}

	// And the fields we DO want survived.
	d := out.Devices[0]
	if d.ProductName != "X2D" {
		t.Errorf("ProductName = %q, want X2D -- this is the only accurate model source", d.ProductName)
	}
	if !d.Online || d.DevID == "" || d.Structure != "CoreXY" {
		t.Errorf("unexpected device: %+v", d)
	}
}
