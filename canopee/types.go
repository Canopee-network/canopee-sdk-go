package canopee

// Typed model of the Canopee wire protocol. Field JSON tags match the wire
// (serde) names so typed values can be converted to the codec's value shape
// and back losslessly (byte arrays travel as number arrays on the wire, not
// base64).

type ObjectId = string
type IdentityId = string
type CapabilityId = string

type ObjectType string

const (
	ObjectTypeBlob            ObjectType = "Blob"
	ObjectTypeAppManifest     ObjectType = "AppManifest"
	ObjectTypeAppPointer      ObjectType = "AppPointer"
	ObjectTypeProfile         ObjectType = "Profile"
	ObjectTypeContactList     ObjectType = "ContactList"
	ObjectTypeHomeIndex       ObjectType = "HomeIndex"
	ObjectTypeCapability      ObjectType = "Capability"
	ObjectTypeCapabilityIndex ObjectType = "CapabilityIndex"
)

type Permission string

const (
	PermissionRead    Permission = "Read"
	PermissionWrite   Permission = "Write"
	PermissionPublish Permission = "Publish"
)

// Resource is an externally-tagged enum value: exactly one key present
// ("Object" → ReceiverId, "SharedName" → {owner,name}, "Channel" → name).
type Resource map[string]any

func ResourceObject(receiverId ObjectId) Resource {
	return Resource{"Object": receiverId}
}

func ResourceSharedName(owner IdentityId, name string) Resource {
	return Resource{"SharedName": map[string]any{"owner": owner, "name": name}}
}

func ResourceChannel(channel string) Resource {
	return Resource{"Channel": channel}
}

type ObjectMetadata struct {
	CreatedAt   uint64  `json:"created_at"`
	Size        uint64  `json:"size"`
	ContentType *string `json:"content_type"`
}

type ObjectPayload struct {
	Owner      IdentityId     `json:"owner"`
	Metadata   ObjectMetadata `json:"metadata"`
	ObjectType ObjectType     `json:"object_type"`
	Data       []byte         `json:"data"`
}

type StoredObject struct {
	ID        ObjectId      `json:"id"`
	Payload   ObjectPayload `json:"payload"`
	PublicKey []byte        `json:"public_key"`
	Signature []byte        `json:"signature"`
}

type ExportBundle struct {
	Version uint32       `json:"version"`
	Object  StoredObject `json:"object"`
}

type ObjectInfo struct {
	ID         ObjectId   `json:"id"`
	Owner      IdentityId `json:"owner"`
	Size       uint64     `json:"size"`
	Verified   bool       `json:"verified"`
	Name       *string    `json:"name"`
	ObjectType ObjectType `json:"object_type"`
}

type Profile struct {
	DisplayName string    `json:"display_name"`
	DhPublicKey [32]byte  `json:"dh_public_key"`
	Avatar      *ObjectId `json:"avatar"`
	Version     uint64    `json:"version"`
}

type Contact struct {
	Name        string   `json:"name"`
	PeerID      string   `json:"peer_id"`
	DhPublicKey [32]byte `json:"dh_public_key"`
	Note        *string  `json:"note"`
}

type ContactList struct {
	Contacts []Contact `json:"contacts"`
	Version  uint64    `json:"version"`
}

type HomeEntry struct {
	Name       string     `json:"name"`
	Object     ObjectId   `json:"object"`
	ObjectType ObjectType `json:"object_type"`
	Shared     bool       `json:"shared"`
	App        *string    `json:"app"`
}

type HomeIndex struct {
	Profile  *ObjectId   `json:"profile"`
	Contacts *ObjectId   `json:"contacts"`
	Entries  []HomeEntry `json:"entries"`
	Version  uint64      `json:"version"`
}

type AppPointerRecord struct {
	Name        string     `json:"name"`
	Owner       IdentityId `json:"owner"`
	Manifest    ObjectId   `json:"manifest"`
	PublishedAt uint64     `json:"published_at"`
	PublicKey   []byte     `json:"public_key"`
	Signature   []byte     `json:"signature"`
}

type Capability struct {
	ID          CapabilityId `json:"id"`
	Issuer      IdentityId   `json:"issuer"`
	Subject     IdentityId   `json:"subject"`
	Resource    Resource     `json:"resource"`
	Permissions []Permission `json:"permissions"`
	IssuedAt    uint64       `json:"issued_at"`
	ExpiresAt   *uint64      `json:"expires_at"`
	PublicKey   []byte       `json:"public_key"`
	Signature   []byte       `json:"signature"`
}

type CapabilityEntry struct {
	Capability Capability `json:"capability"`
	Revoked    bool       `json:"revoked"`
}

type CapabilityIndex struct {
	Entries []CapabilityEntry `json:"entries"`
	Version uint64            `json:"version"`
}

type PeerInfo struct {
	PeerID      string   `json:"peer_id"`
	Identity    *string  `json:"identity"`
	Username    *string  `json:"username"`
	DisplayName *string  `json:"display_name"`
	Addresses   []string `json:"addresses"`
}

type RelayReservationInfo struct {
	RelayPeerID string   `json:"relay_peer_id"`
	Renewal     bool     `json:"renewal"`
	ListenAddrs []string `json:"listen_addrs"`
}

type PubSubMessage struct {
	Topic  string  `json:"topic"`
	Source *string `json:"source"`
	Data   []byte  `json:"data"`
}

type DeviceInfo struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

type PairingQrData struct {
	Version    uint8  `json:"version"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	LanAddr    string `json:"lan_addr"`
	Code       string `json:"code"`
	SessionID  string `json:"session_id"`
}

type SyncResult struct {
	ProfileUpdated  bool `json:"profile_updated"`
	ContactsUpdated bool `json:"contacts_updated"`
	DevicesUpdated  bool `json:"devices_updated"`
}

type Device struct {
	PeerID     string `json:"peer_id"`
	DeviceName string `json:"device_name"`
}

type NodeStatus struct {
	Identity IdentityId `json:"identity"`
	Objects  uint64     `json:"objects"`
	Peers    uint64     `json:"peers"`
}
