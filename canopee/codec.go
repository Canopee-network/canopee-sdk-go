// Package canopee is a Go client SDK for a Canopee node's Unix socket.
//
// Wire contract (identical to the Rust canopee-sdk crate and the node):
// every NodeCommand/NodeResponse is serialized with bincode 1.x legacy
// (little-endian fixints, u32 enum tags, u64 length prefixes, one-byte
// Option tags, fixed-size arrays raw) and framed over the Unix socket as
// `u32 LE length` + payload. Map keys are sorted before encoding for stable,
// reproducible bytes (bincode emits HashMap entries in arbitrary order; the
// node reads any order).
//
// The golden fixture fixtures/golden.json pins every encoder/decoder to the
// exact bytes the node speaks.
package canopee

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ---------------------------------------------------------------------------
// Wire kind descriptors (schema-driven codec)
// ---------------------------------------------------------------------------

// WireKind describes how a value is encoded on the wire.
type WireKind struct {
	T    string // u32|u64|u8|bool|string|bytes|bytesN|struct|enum|vec|opt|map|any
	N    int    // fixed byte-array length for bytesN
	Name string // struct/enum name
	Of   *WireKind
}

func U32() *WireKind                 { return &WireKind{T: "u32"} }
func U64() *WireKind                 { return &WireKind{T: "u64"} }
func U8() *WireKind                  { return &WireKind{T: "u8"} }
func Bool() *WireKind                { return &WireKind{T: "bool"} }
func Str() *WireKind                 { return &WireKind{T: "string"} }
func Bytes() *WireKind               { return &WireKind{T: "bytes"} }
func BytesN(n int) *WireKind         { return &WireKind{T: "bytesN", N: n} }
func StructOf(name string) *WireKind { return &WireKind{T: "struct", Name: name} }
func EnumOf(name string) *WireKind   { return &WireKind{T: "enum", Name: name} }
func VecOf(of *WireKind) *WireKind   { return &WireKind{T: "vec", Of: of} }
func OptOf(of *WireKind) *WireKind   { return &WireKind{T: "opt", Of: of} }
func MapOf(val *WireKind) *WireKind  { return &WireKind{T: "map", Of: val} }

// ObjectId/IdentityId/CapabilityId are string newtypes on the wire.
var ObjectID = Str()
var DHKey = BytesN(32)

type Field struct {
	Name string
	Kind *WireKind
}

type Variant struct {
	Name   string
	Fields []Field
}

// IsNewtype reports whether the variant is a Rust newtype (single unnamed
// field) whose payload is encoded directly rather than as named fields.
func (v Variant) IsNewtype() bool {
	return len(v.Fields) == 1 && v.Fields[0].Name == "0"
}

// WireStructs is the catalog of shared wire structs.
var WireStructs = map[string][]Field{
	"PubSubMessage":        {{"topic", Str()}, {"source", OptOf(Str())}, {"data", Bytes()}},
	"PeerInfo":             {{"peer_id", Str()}, {"identity", OptOf(Str())}, {"username", OptOf(Str())}, {"display_name", OptOf(Str())}, {"addresses", VecOf(Str())}},
	"RelayReservationInfo": {{"relay_peer_id", Str()}, {"renewal", Bool()}, {"listen_addrs", VecOf(Str())}},
	"DeviceInfo":           {{"device_id", Str()}, {"device_name", Str()}},
	"PairingQrData":        {{"version", U8()}, {"device_id", Str()}, {"device_name", Str()}, {"lan_addr", Str()}, {"code", Str()}, {"session_id", Str()}},
	"PairingRecord":        {{"name", Str()}, {"object", StructOf("Object")}, {"pointer", StructOf("AppPointerRecord")}},
	"PairingData":          {{"version", U8()}, {"identity_key", Bytes()}, {"records", VecOf(StructOf("PairingRecord"))}},
	"SyncResult":           {{"profile_updated", Bool()}, {"contacts_updated", Bool()}, {"devices_updated", Bool()}},
	"AppManifest":          {{"name", Str()}, {"owner", Str()}, {"entrypoint", Str()}, {"assets", MapOf(Str())}},
	"ObjectMetadata":       {{"created_at", U64()}, {"size", U64()}, {"content_type", OptOf(Str())}},
	"ObjectPayload":        {{"owner", Str()}, {"metadata", StructOf("ObjectMetadata")}, {"object_type", EnumOf("ObjectType")}, {"data", Bytes()}},
	"ObjectInfo":           {{"id", Str()}, {"owner", Str()}, {"size", U64()}, {"verified", Bool()}, {"name", OptOf(Str())}, {"object_type", EnumOf("ObjectType")}},
	"Object":               {{"id", Str()}, {"payload", StructOf("ObjectPayload")}, {"public_key", Bytes()}, {"signature", Bytes()}},
	"ExportBundle":         {{"version", U32()}, {"object", StructOf("Object")}},
	"Profile":              {{"display_name", Str()}, {"dh_public_key", DHKey}, {"avatar", OptOf(Str())}, {"version", U64()}},
	"Contact":              {{"name", Str()}, {"peer_id", Str()}, {"dh_public_key", DHKey}, {"note", OptOf(Str())}},
	"ContactList":          {{"contacts", VecOf(StructOf("Contact"))}, {"version", U64()}},
	"HomeEntry":            {{"name", Str()}, {"object", Str()}, {"object_type", EnumOf("ObjectType")}, {"shared", Bool()}, {"app", OptOf(Str())}},
	"HomeIndex":            {{"profile", OptOf(Str())}, {"contacts", OptOf(Str())}, {"entries", VecOf(StructOf("HomeEntry"))}, {"version", U64()}},
	"AppPointerRecord":     {{"name", Str()}, {"owner", Str()}, {"manifest", Str()}, {"published_at", U64()}, {"public_key", Bytes()}, {"signature", Bytes()}},
	"Capability":           {{"id", Str()}, {"issuer", Str()}, {"subject", Str()}, {"resource", EnumOf("Resource")}, {"permissions", VecOf(EnumOf("Permission"))}, {"issued_at", U64()}, {"expires_at", OptOf(U64())}, {"public_key", Bytes()}, {"signature", Bytes()}},
	"ExportCapability":     {{"version", U8()}, {"capability", StructOf("Capability")}},
	"CapabilityEntry":      {{"capability", StructOf("Capability")}, {"revoked", Bool()}},
	"CapabilityIndex":      {{"entries", VecOf(StructOf("CapabilityEntry"))}, {"version", U64()}},
}

// WireEnums is the catalog of shared wire enums.
var WireEnums = map[string][]Variant{
	"ObjectType": {
		{Name: "Blob"}, {Name: "AppManifest"}, {Name: "AppPointer"},
		{Name: "Profile"}, {Name: "ContactList"}, {Name: "HomeIndex"},
		{Name: "Capability"}, {Name: "CapabilityIndex"},
	},
	"Permission": {
		{Name: "Read"}, {Name: "Write"}, {Name: "Publish"},
	},
	"Resource": {
		{Name: "Object", Fields: []Field{{Name: "0", Kind: Str()}}},
		{Name: "SharedName", Fields: []Field{{Name: "owner", Kind: Str()}, {Name: "name", Kind: Str()}}},
		{Name: "Channel", Fields: []Field{{Name: "0", Kind: Str()}}},
	},
}

// NodeCommand enumerates every command variant in order (u32 tags).
var NodeCommand = []Variant{
	{Name: "Put", Fields: []Field{{"data", Bytes()}, {"name", OptOf(Str())}}},
	{Name: "PutObject", Fields: []Field{{"data", Bytes()}, {"object_type", EnumOf("ObjectType")}, {"name", OptOf(Str())}}},
	{Name: "Concat", Fields: []Field{{"ids", VecOf(Str())}, {"name", OptOf(Str())}}},
	{Name: "Get", Fields: []Field{{"id", Str()}}},
	{Name: "List"},
	{Name: "DeleteObject", Fields: []Field{{"id", Str()}}},
	{Name: "SetName", Fields: []Field{{"id", Str()}, {"name", Str()}}},
	{Name: "Export", Fields: []Field{{"id", Str()}}},
	{Name: "Import", Fields: []Field{{"bundle", StructOf("ExportBundle")}}},
	{Name: "Status"},
	{Name: "Identity"},
	{Name: "Shutdown"},
	{Name: "Dial", Fields: []Field{{"addr", Str()}}},
	{Name: "ListenViaRelay", Fields: []Field{{"relay_addr", Str()}}},
	{Name: "Publish", Fields: []Field{{"topic", Str()}, {"data", Bytes()}}},
	{Name: "Subscribe", Fields: []Field{{"topic", Str()}}},
	{Name: "Peers"},
	{Name: "RelayReservations"},
	{Name: "FindProviders", Fields: []Field{{"id", Str()}}},
	{Name: "FetchObject", Fields: []Field{{"peer_id", Str()}, {"id", Str()}}},
	{Name: "Announce", Fields: []Field{{"id", Str()}}},
	{Name: "PublishAppPointer", Fields: []Field{{"name", Str()}, {"manifest", Str()}}},
	{Name: "ResolveAppPointer", Fields: []Field{{"owner", Str()}, {"name", Str()}}},
	{Name: "PublishPointer", Fields: []Field{{"name", Str()}, {"target", Str()}}},
	{Name: "ResolvePointer", Fields: []Field{{"owner", Str()}, {"name", Str()}}},
	{Name: "SaveProfile", Fields: []Field{{"profile", StructOf("Profile")}}},
	{Name: "LoadProfile"},
	{Name: "SaveContactList", Fields: []Field{{"list", StructOf("ContactList")}}},
	{Name: "LoadContactList"},
	{Name: "SaveHomeIndex", Fields: []Field{{"index", StructOf("HomeIndex")}}},
	{Name: "LoadHomeIndex"},
	{Name: "SetHomeEntryShared", Fields: []Field{{"name", Str()}, {"shared", Bool()}}},
	{Name: "DhPublicKey"},
	{Name: "PutObjectPublic", Fields: []Field{{"data", Bytes()}, {"object_type", EnumOf("ObjectType")}, {"name", OptOf(Str())}}},
	{Name: "ShareObject", Fields: []Field{{"name", Str()}, {"object", Str()}, {"app", OptOf(Str())}, {"recipients", VecOf(DHKey)}}},
	{Name: "ClaimUsername", Fields: []Field{{"username", Str()}}},
	{Name: "ResolveUsername", Fields: []Field{{"username", Str()}}},
	{Name: "ResolveProfile", Fields: []Field{{"owner", Str()}}},
	{Name: "ExportIdentity", Fields: []Field{{"passphrase", Str()}}},
	{Name: "ImportIdentity", Fields: []Field{{"bytes", Bytes()}, {"passphrase", Str()}, {"overwrite", Bool()}}},
	{Name: "ShowUsername"},
	{Name: "Device"},
	{Name: "DeviceList"},
	{Name: "ResolveOwnerDevice", Fields: []Field{{"owner", Str()}}},
	{Name: "AddDevice", Fields: []Field{{"device_id", Str()}, {"device_name", Str()}}},
	{Name: "RemoveDevice", Fields: []Field{{"device_id", Str()}}},
	{Name: "InitiatePairing"},
	{Name: "CompletePairing", Fields: []Field{{"qr", StructOf("PairingQrData")}, {"code", Str()}}},
	{Name: "SyncFromPeer", Fields: []Field{{"peer_id", Str()}}},
	{Name: "SyncDeviceList"},
	{Name: "GrantCapability", Fields: []Field{{"subject", Str()}, {"resource", EnumOf("Resource")}, {"permissions", VecOf(EnumOf("Permission"))}, {"expires_at", OptOf(U64())}}},
	{Name: "ListCapabilities"},
	{Name: "RevokeCapability", Fields: []Field{{"id", Str()}}},
	{Name: "CheckCapability", Fields: []Field{{"capability", StructOf("Capability")}}},
	{Name: "CheckAccess", Fields: []Field{{"subject", Str()}, {"permission", EnumOf("Permission")}, {"resource", EnumOf("Resource")}}},
	{Name: "StartServeSession", Fields: []Field{{"edge_addr", Str()}, {"app_id", Str()}}},
	{Name: "StopServeSession"},
}

// NodeResponse enumerates every response variant in order (u32 tags).
var NodeResponse = []Variant{
	{Name: "ObjectCreated", Fields: []Field{{"id", Str()}}},
	{Name: "DhPublicKey", Fields: []Field{{"key", DHKey}}},
	{Name: "Concatenated", Fields: []Field{{"id", Str()}}},
	{Name: "NameSet"},
	{Name: "Object", Fields: []Field{{"object", StructOf("Object")}}},
	{Name: "Objects", Fields: []Field{{"objects", VecOf(StructOf("ObjectInfo"))}}},
	{Name: "ObjectDeleted"},
	{Name: "Exported", Fields: []Field{{"bundle", StructOf("ExportBundle")}}},
	{Name: "Imported"},
	{Name: "Status", Fields: []Field{{"identity", Str()}, {"objects", U64()}, {"peers", U64()}}},
	{Name: "Error", Fields: []Field{{"message", Str()}}},
	{Name: "Identity", Fields: []Field{{"identity_id", Str()}}},
	{Name: "ShutdownAccepted"},
	{Name: "Dialed"},
	{Name: "ListeningViaRelay"},
	{Name: "Published"},
	{Name: "Subscribed"},
	{Name: "PubSub", Fields: []Field{{"0", StructOf("PubSubMessage")}}},
	{Name: "Peers", Fields: []Field{{"peers", VecOf(StructOf("PeerInfo"))}}},
	{Name: "RelayReservations", Fields: []Field{{"reservations", VecOf(StructOf("RelayReservationInfo"))}}},
	{Name: "Providers", Fields: []Field{{"peer_ids", VecOf(Str())}}},
	{Name: "Announced"},
	{Name: "AppPointerPublished"},
	{Name: "AppPointer", Fields: []Field{{"record", OptOf(StructOf("AppPointerRecord"))}}},
	{Name: "PointerPublished"},
	{Name: "Pointer", Fields: []Field{{"record", OptOf(StructOf("AppPointerRecord"))}}},
	{Name: "ProfileSaved", Fields: []Field{{"id", Str()}}},
	{Name: "Profile", Fields: []Field{{"profile", OptOf(StructOf("Profile"))}}},
	{Name: "ContactListSaved", Fields: []Field{{"id", Str()}}},
	{Name: "ContactList", Fields: []Field{{"list", OptOf(StructOf("ContactList"))}}},
	{Name: "HomeIndexSaved", Fields: []Field{{"id", Str()}}},
	{Name: "HomeIndex", Fields: []Field{{"index", OptOf(StructOf("HomeIndex"))}}},
	{Name: "UsernameClaimed"},
	{Name: "Username", Fields: []Field{{"username", OptOf(Str())}}},
	{Name: "UsernameOwner", Fields: []Field{{"owner", OptOf(Str())}}},
	{Name: "IdentityExported", Fields: []Field{{"bytes", Bytes()}}},
	{Name: "IdentityImported", Fields: []Field{{"identity_id", Str()}}},
	{Name: "Device", Fields: []Field{{"peer_id", Str()}, {"device_name", Str()}}},
	{Name: "DeviceList", Fields: []Field{{"devices", VecOf(StructOf("DeviceInfo"))}}},
	{Name: "OwnerDevice", Fields: []Field{{"peer_id", OptOf(Str())}}},
	{Name: "DeviceAdded"},
	{Name: "DeviceRemoved"},
	{Name: "PairingQr", Fields: []Field{{"qr", StructOf("PairingQrData")}}},
	{Name: "PairingComplete", Fields: []Field{{"message", Str()}}},
	{Name: "SyncComplete", Fields: []Field{{"result", StructOf("SyncResult")}}},
	{Name: "CapabilityGranted", Fields: []Field{{"capability", StructOf("Capability")}}},
	{Name: "Capabilities", Fields: []Field{{"index", OptOf(StructOf("CapabilityIndex"))}}},
	{Name: "CapabilityRevoked"},
	{Name: "CapabilityCheck", Fields: []Field{{"valid", Bool()}, {"reason", Str()}}},
	{Name: "AccessAllowed", Fields: []Field{{"allowed", Bool()}}},
	{Name: "ServeSessionStarted", Fields: []Field{{"app_id", Str()}, {"root_url", Str()}}},
	{Name: "ServeSessionStopped"},
}

// ---------------------------------------------------------------------------
// Writer / Reader
// ---------------------------------------------------------------------------

// Writer accumulates little-endian bincode bytes.
type Writer struct {
	buf []byte
}

func NewWriter() *Writer { return &Writer{buf: make([]byte, 0, 256)} }

func (w *Writer) Raw(b []byte) { w.buf = append(w.buf, b...) }

func (w *Writer) U8(v int) { w.buf = append(w.buf, byte(v)) }

func (w *Writer) Bool(b bool) {
	if b {
		w.U8(1)
	} else {
		w.U8(0)
	}
}

func (w *Writer) U32(v uint32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, v)
}

func (w *Writer) U64(v uint64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, v)
}

func (w *Writer) Str(s string) {
	w.U64(uint64(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *Writer) Bytes(b []byte) {
	w.U64(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *Writer) BytesN(b []byte) {
	w.buf = append(w.buf, b...)
}

func (w *Writer) Result() []byte { return w.buf }

// Reader reads little-endian bincode bytes.
type Reader struct {
	buf []byte
	pos int
	err error
}

func NewReader(b []byte) *Reader { return &Reader{buf: b} }

func (r *Reader) Err() error { return r.err }

func (r *Reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.pos+n > len(r.buf) {
		r.err = fmt.Errorf("bincode: unexpected end of input (need %d bytes, have %d)", n, len(r.buf)-r.pos)
		return false
	}
	return true
}

func (r *Reader) U8() byte {
	if !r.need(1) {
		return 0
	}
	v := r.buf[r.pos]
	r.pos++
	return v
}

func (r *Reader) Bool() bool { return r.U8() != 0 }

func (r *Reader) U32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v
}

func (r *Reader) U64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v
}

func (r *Reader) Str() string {
	n := r.U64()
	if r.err != nil || !r.need(int(n)) {
		return ""
	}
	s := string(r.buf[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s
}

func (r *Reader) Bytes() []byte {
	n := r.U64()
	if r.err != nil || !r.need(int(n)) {
		return nil
	}
	b := append([]byte(nil), r.buf[r.pos:r.pos+int(n)]...)
	r.pos += int(n)
	return b
}

func (r *Reader) BytesN(n int) []byte {
	if !r.need(n) {
		return nil
	}
	b := append([]byte(nil), r.buf[r.pos:r.pos+n]...)
	r.pos += n
	return b
}

// ---------------------------------------------------------------------------
// Value shapes
// ---------------------------------------------------------------------------

// Shapes mirror serde_json output for the Rust types:
// struct → map[string]any, newtype id → string, unit variant → string,
// data variant → map[string]any{"Variant": ...}, bytes → []any (numbers),
// option → nil or value, map → map[string]any (keys sorted on encode).

// ---------------------------------------------------------------------------
// Encode
// ---------------------------------------------------------------------------

func codecFor(kind *WireKind) ([]Field, []Variant, error) {
	if kind.T == "struct" {
		f, ok := WireStructs[kind.Name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown wire struct %q", kind.Name)
		}
		return f, nil, nil
	}
	if kind.T == "enum" {
		v, ok := WireEnums[kind.Name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown wire enum %q", kind.Name)
		}
		return nil, v, nil
	}
	return nil, nil, fmt.Errorf("not a struct/enum kind: %q", kind.T)
}

func variantIndex(variants []Variant, v any, path string) (int, error) {
	if s, ok := asString(v); ok {
		for i, variant := range variants {
			if variant.Name == s {
				return i, nil
			}
		}
		return 0, fmt.Errorf("%s: unknown variant %q", path, s)
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for k := range m {
			for i, variant := range variants {
				if variant.Name == k {
					return i, nil
				}
			}
			return 0, fmt.Errorf("%s: unknown variant %q", path, k)
		}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map && rv.Len() == 1 {
		keys := rv.MapKeys()
		k, _ := asString(keys[0].Interface())
		for i, variant := range variants {
			if variant.Name == k {
				return i, nil
			}
		}
		return 0, fmt.Errorf("%s: unknown variant %q", path, k)
	}
	return 0, fmt.Errorf("%s: cannot resolve enum variant from %T", path, v)
}

func toBytes(v any, path string) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case []any:
		out := make([]byte, len(b))
		for i, x := range b {
			out[i] = byte(asInt(x))
		}
		return out, nil
	case string:
		return []byte(b), nil
	default:
		return nil, fmt.Errorf("%s: expected byte array, got %T", path, v)
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int8:
		return int(n)
	case int16:
		return int(n)
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint8:
		return int(n)
	case uint16:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case float32:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// asString coerces string and named-string (e.g. ObjectType, Permission) values
// to a plain string.
func asString(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.String && rv.Type().PkgPath() != "" {
		return rv.String(), true
	}
	return "", false
}

// asMap coerces map[string]any and named map types (e.g. Resource) to a plain
// map[string]any.
func asMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map {
		return nil, false
	}
	m := make(map[string]any, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		k, _ := asString(iter.Key().Interface())
		m[k] = iter.Value().Interface()
	}
	return m, true
}

// EncodeValue writes `v` (a serde_json-shaped value) for `kind`.
func EncodeValue(w *Writer, kind *WireKind, v any, path string) error {
	switch kind.T {
	case "u32":
		w.U32(uint32(asInt(v)))
	case "u64":
		w.U64(uint64(asInt(v)))
	case "u8":
		w.U8(asInt(v))
	case "bool":
		w.Bool(v.(bool))
	case "string":
		s, ok := asString(v)
		if !ok {
			return fmt.Errorf("%s: expected string, got %T", path, v)
		}
		w.Str(s)
	case "bytes":
		b, err := toBytes(v, path)
		if err != nil {
			return err
		}
		w.Bytes(b)
	case "bytesN":
		b, err := toBytes(v, path)
		if err != nil {
			return err
		}
		if len(b) != kind.N {
			return fmt.Errorf("%s: expected %d bytes, got %d", path, kind.N, len(b))
		}
		w.BytesN(b)
	case "struct", "enum":
		fields, variants, err := codecFor(kind)
		if err != nil {
			return err
		}
		if kind.T == "struct" {
			return encodeFields(w, fields, v, path)
		}
		return encodeVariants(w, variants, v, path)
	case "vec":
		arr, ok := v.([]any)
		if !ok {
			rv := reflect.ValueOf(v)
			if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
				return fmt.Errorf("%s: expected array, got %T", path, v)
			}
			arr = make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				arr[i] = rv.Index(i).Interface()
			}
		}
		w.U64(uint64(len(arr)))
		for i, x := range arr {
			if err := EncodeValue(w, kind.Of, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "opt":
		if v == nil {
			w.U8(0)
		} else {
			w.U8(1)
			if err := EncodeValue(w, kind.Of, v, path); err != nil {
				return err
			}
		}
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected map, got %T", path, v)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w.U64(uint64(len(keys)))
		for _, k := range keys {
			w.Str(k)
			if err := EncodeValue(w, kind.Of, m[k], path+"."+k); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unknown wire kind %q", path, kind.T)
	}
	return nil
}

func encodeFields(w *Writer, fields []Field, v any, path string) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("%s: expected struct object, got %T", path, v)
	}
	for _, f := range fields {
		fv, exists := m[f.Name]
		if !exists {
			return fmt.Errorf("%s.%s: missing field", path, f.Name)
		}
		if err := EncodeValue(w, f.Kind, fv, path+"."+f.Name); err != nil {
			return err
		}
	}
	return nil
}

func encodeVariants(w *Writer, variants []Variant, v any, path string) error {
	idx, err := variantIndex(variants, v, path)
	if err != nil {
		return err
	}
	w.U32(uint32(idx))
	variant := variants[idx]
	if len(variant.Fields) == 0 {
		return nil
	}
	if variant.IsNewtype() {
		m, _ := asMap(v)
		payload := m[variant.Name]
		return EncodeValue(w, variant.Fields[0].Kind, payload, path+"."+variant.Name)
	}
	m, ok := asMap(v)
	payload, ok := asMap(m[variant.Name])
	if !ok {
		return fmt.Errorf("%s.%s: expected struct object", path, variant.Name)
	}
	for _, f := range variant.Fields {
		if err := EncodeValue(w, f.Kind, payload[f.Name], path+"."+variant.Name+"."+f.Name); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Decode
// ---------------------------------------------------------------------------

// DecodeValue reads a serde_json-shaped value for `kind` from `r`.
func DecodeValue(r *Reader, kind *WireKind, path string) (any, error) {
	switch kind.T {
	case "u32":
		return float64(r.U32()), nil
	case "u64":
		return float64(r.U64()), nil
	case "u8":
		return float64(r.U8()), nil
	case "bool":
		return r.Bool(), nil
	case "string":
		return r.Str(), nil
	case "bytes":
		b := r.Bytes()
		out := make([]any, len(b))
		for i, x := range b {
			out[i] = float64(x)
		}
		return out, nil
	case "bytesN":
		b := r.BytesN(kind.N)
		out := make([]any, len(b))
		for i, x := range b {
			out[i] = float64(x)
		}
		return out, nil
	case "struct", "enum":
		fields, variants, err := codecFor(kind)
		if err != nil {
			return nil, err
		}
		if kind.T == "struct" {
			return decodeFields(r, fields)
		}
		return decodeVariants(r, variants, path)
	case "vec":
		n := r.U64()
		out := make([]any, 0, n)
		for i := uint64(0); i < n; i++ {
			x, err := DecodeValue(r, kind.Of, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	case "opt":
		if r.U8() == 0 {
			return nil, nil
		}
		return DecodeValue(r, kind.Of, path)
	case "map":
		n := r.U64()
		out := make(map[string]any, n)
		for i := uint64(0); i < n; i++ {
			k := r.Str()
			val, err := DecodeValue(r, kind.Of, path+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s: unknown wire kind %q", path, kind.T)
	}
}

func decodeFields(r *Reader, fields []Field) (any, error) {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		v, err := DecodeValue(r, f.Kind, f.Name)
		if err != nil {
			return nil, err
		}
		out[f.Name] = v
	}
	return out, nil
}

func decodeVariants(r *Reader, variants []Variant, path string) (any, error) {
	idx := r.U32()
	if idx >= uint32(len(variants)) {
		return nil, fmt.Errorf("%s: unknown variant tag %d", path, idx)
	}
	variant := variants[idx]
	if len(variant.Fields) == 0 {
		return variant.Name, nil
	}
	if variant.IsNewtype() {
		inner, err := DecodeValue(r, variant.Fields[0].Kind, path+"."+variant.Name)
		if err != nil {
			return nil, err
		}
		return map[string]any{variant.Name: inner}, nil
	}
	inner, err := decodeFields(r, variant.Fields)
	if err != nil {
		return nil, err
	}
	return map[string]any{variant.Name: inner}, nil
}

// ---------------------------------------------------------------------------
// Commands / responses / framing
// ---------------------------------------------------------------------------

// EncodeCommand encodes a NodeCommand value shape to its exact bincode bytes.
func EncodeCommand(v any) ([]byte, error) {
	w := NewWriter()
	if err := encodeVariants(w, NodeCommand, v, "command"); err != nil {
		return nil, err
	}
	return w.Result(), nil
}

// DecodeCommand decodes command bytes into the value shape.
func DecodeCommand(b []byte) (any, error) {
	r := NewReader(b)
	v, err := decodeVariants(r, NodeCommand, "command")
	if err != nil {
		return nil, err
	}
	if r.Err() != nil {
		return nil, r.Err()
	}
	return v, nil
}

// EncodeResponse encodes a NodeResponse value shape.
func EncodeResponse(v any) ([]byte, error) {
	w := NewWriter()
	if err := encodeVariants(w, NodeResponse, v, "response"); err != nil {
		return nil, err
	}
	return w.Result(), nil
}

// DecodeResponse decodes response bytes into the value shape.
func DecodeResponse(b []byte) (any, error) {
	r := NewReader(b)
	v, err := decodeVariants(r, NodeResponse, "response")
	if err != nil {
		return nil, err
	}
	if r.Err() != nil {
		return nil, r.Err()
	}
	return v, nil
}

// EncodeStruct encodes one of the shared wire structs by name.
func EncodeStruct(name string, v any) ([]byte, error) {
	w := NewWriter()
	if err := EncodeValue(w, StructOf(name), v, name); err != nil {
		return nil, err
	}
	return w.Result(), nil
}

// DecodeStruct decodes one of the shared wire structs by name.
func DecodeStruct(name string, b []byte) (any, error) {
	r := NewReader(b)
	v, err := DecodeValue(r, StructOf(name), name)
	if err != nil {
		return nil, err
	}
	if r.Err() != nil {
		return nil, r.Err()
	}
	return v, nil
}

// Frame wraps a payload the way the node transport does: u32 BE length + payload
// (the node and Rust SDK use tokio's write_u32/read_u32, which are big-endian).
func Frame(payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(payload)))
	copy(out[4:], payload)
	return out
}

// ReadFrame reads exactly one length-prefixed (u32 BE) frame from `r`.
func ReadFrame(r *Reader) ([]byte, error) {
	if r.err != nil || len(r.buf)-r.pos < 4 {
		return nil, errors.New("bincode: truncated frame header")
	}
	n := binary.BigEndian.Uint32(r.buf[r.pos : r.pos+4])
	r.pos += 4
	if !r.need(int(n)) {
		return nil, errors.New("bincode: truncated frame header")
	}
	p := append([]byte(nil), r.buf[r.pos:r.pos+int(n)]...)
	r.pos += int(n)
	return p, nil
}
