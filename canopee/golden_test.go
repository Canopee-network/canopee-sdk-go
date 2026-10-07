package canopee

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type goldenVector struct {
	Name  string          `json:"name"`
	Hex   string          `json:"hex"`
	Value json.RawMessage `json:"value"`
}

func loadGolden(t *testing.T, out *[]goldenVector) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("fixtures", "golden.json"))
	if err != nil {
		t.Skipf("fixtures/golden.json not found: %v (run canopee-sdk's golden_vectors test)", err)
	}
	var doc struct {
		Vectors []goldenVector `json:"vectors"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	*out = doc.Vectors
}

func unwrapValue(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	return v
}

func scalarKind(name string) (*WireKind, bool) {
	switch name {
	case "ObjectId", "CapabilityId":
		return Str(), true
	}
	return nil, false
}

func kindFor(name string) *WireKind {
	if k, ok := scalarKind(name); ok {
		return k
	}
	if _, ok := WireEnums[name]; ok {
		return EnumOf(name)
	}
	return StructOf(name)
}

func TestGoldenCoversEveryCommandResponseAndType(t *testing.T) {
	var vv []goldenVector
	loadGolden(t, &vv)
	names := make(map[string]bool, len(vv))
	for _, v := range vv {
		names[v.Name] = true
	}
	for _, v := range NodeCommand {
		if !names["cmd_"+v.Name] {
			t.Errorf("missing command vector cmd_%s", v.Name)
		}
	}
	for _, v := range NodeResponse {
		if !names["res_"+v.Name] {
			t.Errorf("missing response vector res_%s", v.Name)
		}
	}
	for name := range WireStructs {
		if !names[name] {
			t.Errorf("missing struct vector %s", name)
		}
	}
	for name := range WireEnums {
		if !names[name] {
			t.Errorf("missing enum vector %s", name)
		}
	}
}

func TestGoldenEncode(t *testing.T) {
	var vv []goldenVector
	loadGolden(t, &vv)
	for _, v := range vv {
		want, err := hex.DecodeString(v.Hex)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		var payload []byte
		switch {
		case len(v.Name) >= 4 && v.Name[:4] == "cmd_":
			payload, err = EncodeCommand(unwrapValue(v.Value))
		case len(v.Name) >= 4 && v.Name[:4] == "res_":
			payload, err = EncodeResponse(unwrapValue(v.Value))
		default:
			w := NewWriter()
			err = EncodeValue(w, kindFor(v.Name), unwrapValue(v.Value), v.Name)
			payload = w.Result()
		}
		if err != nil {
			t.Errorf("%s: encode: %v", v.Name, err)
			continue
		}
		if string(payload) != string(want) {
			t.Errorf("%s: encode mismatch\n got %s\nwant %s", v.Name, hex.EncodeToString(payload), v.Hex)
		}
	}
}

func TestGoldenDecode(t *testing.T) {
	var vv []goldenVector
	loadGolden(t, &vv)
	for _, v := range vv {
		b, err := hex.DecodeString(v.Hex)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		var got, want any
		switch {
		case len(v.Name) >= 4 && v.Name[:4] == "cmd_":
			got, err = DecodeCommand(b)
			want = unwrapValue(v.Value)
		case len(v.Name) >= 4 && v.Name[:4] == "res_":
			got, err = DecodeResponse(b)
			want = unwrapValue(v.Value)
		default:
			r := NewReader(b)
			got, err = DecodeValue(r, kindFor(v.Name), v.Name)
			want = unwrapValue(v.Value)
		}
		if err != nil {
			t.Errorf("%s: decode: %v", v.Name, err)
			continue
		}
		if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
			t.Errorf("%s: decode mismatch\n got %s\nwant %s", v.Name, mustJSON(t, got), mustJSON(t, want))
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
