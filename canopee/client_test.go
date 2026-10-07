package canopee

import (
	"bytes"
	"reflect"
	"testing"
)

func TestFrameRoundTripFrameDecoder(t *testing.T) {
	payload := Frame(EncodeCommandStub(t, "Status"))
	var buf bytes.Buffer
	buf.Write(payload)
	got, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := EncodeCommand("Status")
	if !bytes.Equal(got, want) {
		t.Fatalf("frame mismatch: %x != %x", got, want)
	}
	if _, err := readFrame(&buf); err == nil {
		t.Fatal("expected EOF on empty stream")
	}
}

func TestFrameReaderPartial(t *testing.T) {
	cmd, _ := EncodeCommand("Status")
	var buf bytes.Buffer
	buf.Write(Frame(cmd))
	buf.Write(Frame(mustResponse(t, "Dialed")))
	f1, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f1, cmd) {
		t.Fatalf("first frame mismatch: %x", f1)
	}
	r, err := DecodeResponse(f2)
	if err != nil {
		t.Fatal(err)
	}
	if r != "Dialed" {
		t.Fatalf("second frame: %v", r)
	}
}

func TestToShapeRoundTripsTyped(t *testing.T) {
	now := uint64(1_700_000_000)
	samples := []any{
		Profile{DisplayName: "alice", DhPublicKey: [32]byte{7}, Avatar: ptr("cccc"), Version: 3},
		StoredObject{ID: "aa", PublicKey: []byte{1, 2, 3}, Signature: []byte{9}},
		ContactList{Contacts: []Contact{{Name: "bob", PeerID: "12D3KooWb", DhPublicKey: [32]byte{9}}}, Version: 2},
		HomeIndex{Profile: ptr("eeee"), Entries: []HomeEntry{{Name: "pic.png", Object: "dddd", ObjectType: ObjectTypeBlob, Shared: true}}, Version: 5},
		ExportBundle{Version: 1, Object: StoredObject{ID: "aa", Payload: ObjectPayload{Owner: "alice", Metadata: ObjectMetadata{CreatedAt: now, Size: 5}, ObjectType: ObjectTypeBlob, Data: []byte("hi")}}},
		Capability{ID: "cafebabe", Resource: ResourceChannel("team-news"), Permissions: []Permission{PermissionRead}, IssuedAt: now},
		SyncResult{ProfileUpdated: true, DevicesUpdated: true},
	}
	for _, s := range samples {
		shape, err := toShape(s)
		if err != nil {
			t.Fatalf("%T: toShape: %v", s, err)
		}
		back := reflect.New(reflect.TypeOf(s)).Interface()
		if err := fromShape(shape, back); err != nil {
			t.Fatalf("%T: fromShape: %v", s, err)
		}
		// typed → shape → typed must reproduce the same wire shape. (nil vs
		// empty []byte are wire-equivalent but marshal differently, so we
		// assert on the shape, not the Go struct.)
		shapeBack, err := toShape(reflect.ValueOf(back).Elem().Interface())
		if err != nil {
			t.Fatalf("%T: toShape(back): %v", s, err)
		}
		if string(mustJSON(t, shapeBack)) != string(mustJSON(t, shape)) {
			t.Errorf("%T shape round-trip mismatch:\n got %s\nwant %s", s, mustJSON(t, shapeBack), mustJSON(t, shape))
		}
	}
}

func TestUnwrapError(t *testing.T) {
	_, err := unwrap(map[string]any{"Error": "boom"}, "Status")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "boom" {
		t.Fatalf("message: %v", err)
	}
}

func EncodeCommandStub(t *testing.T, variant string) []byte {
	t.Helper()
	b, err := EncodeCommand(variant)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustResponse(t *testing.T, variant string) []byte {
	t.Helper()
	b, err := EncodeResponse(variant)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ptr[T any](v T) *T { return &v }
