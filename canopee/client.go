package canopee

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
)

// Error is returned when the node replies `NodeResponse::Error`.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// NodeNotRunningError is returned when nothing is listening on the socket.
type NodeNotRunningError struct{}

func (e *NodeNotRunningError) Error() string { return "Canopee node is not running" }

// Client talks to a running canopee-node over its Unix socket. Like the Rust
// SDK, every request/response call opens its own connection and closes it
// after the single reply; only Subscribe keeps a persistent stream.
type Client struct {
	SocketPath string
}

// NewClient returns a client for an explicit socket path.
func NewClient(socketPath string) *Client { return &Client{SocketPath: socketPath} }

// DefaultSocketPath mirrors the node's Config::new(): $CANOPEE_APP_ROOT/.canopee/node.sock,
// else ~/.canopee/node.sock.
func DefaultSocketPath() string {
	if root := os.Getenv("CANOPEE_APP_ROOT"); root != "" {
		return filepath.Join(root, ".canopee", "node.sock")
	}
	if u, err := user.Current(); err == nil {
		return filepath.Join(u.HomeDir, ".canopee", "node.sock")
	}
	return filepath.Join(os.TempDir(), "canopee", "node.sock")
}

// NewDefaultClient returns a client for the default socket path.
func NewDefaultClient() *Client { return &Client{SocketPath: DefaultSocketPath()} }

// Request sends a NodeCommand (as its serde_json-shaped value, e.g.
// map[string]any{"Put": ...} or the string "Status") and returns the payload
// of the expected response variant, converted from the wire shape. The node's
// `Error` reply is returned as a *Error.
func (c *Client) Request(command any) (any, error) {
	return c.request(command, "")
}

func (c *Client) request(command any, expect string) (any, error) {
	payload, err := EncodeCommand(command)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", c.SocketPath)
	if err != nil {
		return nil, &NodeNotRunningError{}
	}
	defer conn.Close()
	if err := writeFrame(conn, payload); err != nil {
		return nil, err
	}
	reply, err := readFrame(conn)
	if err != nil {
		return nil, err
	}
	value, err := DecodeResponse(reply)
	if err != nil {
		return nil, err
	}
	return unwrap(value, expect)
}

// command builds a command shape from a variant name and optional fields.
func command(variant string, fields map[string]any) any {
	if fields == nil {
		return variant
	}
	return map[string]any{variant: fields}
}

func unwrap(value any, expect string) (any, error) {
	m, ok := value.(map[string]any)
	if !ok {
		if expect == "" {
			return value, nil
		}
		if want, ok2 := value.(string); ok2 && want == expect {
			return want, nil
		}
		return nil, fmt.Errorf("unexpected response: %v", value)
	}
	if len(m) == 1 {
		for k, v := range m {
			if k == "Error" {
				msg, _ := asString(v)
				if msg == "" {
					if em, ok := v.(map[string]any); ok {
						msg, _ = asString(em["message"])
					}
				}
				return nil, &Error{Message: msg}
			}
			if expect == "" || k == expect {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected response: %v", value)
}

// ---------------------------------------------------------------------------
// Socket framing helpers
// ---------------------------------------------------------------------------

func writeFrame(w io.Writer, payload []byte) error {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// ---------------------------------------------------------------------------
// Typed client API (API-for-API parity with the Rust SDK)
// ---------------------------------------------------------------------------

// Storage.

// Put stores a blob and returns its content-addressed id.
func (c *Client) Put(data []byte, name *string) (ObjectId, error) {
	return c.objectCreated(command("Put", map[string]any{"data": data, "name": optString(name)}))
}

// PutObject stores a blob with an explicit object type.
func (c *Client) PutObject(data []byte, objectType ObjectType, name *string) (ObjectId, error) {
	return c.objectCreated(command("PutObject", map[string]any{"data": data, "object_type": objectType, "name": optString(name)}))
}

func (c *Client) objectCreated(cmd any) (ObjectId, error) {
	p, err := c.request(cmd, "ObjectCreated")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["id"].(string), nil
}

// Concat concatenates the payloads of the given objects into a new blob.
func (c *Client) Concat(ids []ObjectId, name *string) (ObjectId, error) {
	p, err := c.request(command("Concat", map[string]any{"ids": ids, "name": optString(name)}), "Concatenated")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["id"].(string), nil
}

// Get retrieves and (locally) verifies a stored object.
func (c *Client) Get(id ObjectId) (*StoredObject, error) {
	p, err := c.request(command("Get", map[string]any{"id": id}), "Object")
	if err != nil {
		return nil, err
	}
	var obj StoredObject
	if err := fromShape(p.(map[string]any)["object"], &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// List returns the node's local object index.
func (c *Client) List() ([]ObjectInfo, error) {
	p, err := c.request("List", "Objects")
	if err != nil {
		return nil, err
	}
	var out []ObjectInfo
	if err := fromShape(p.(map[string]any)["objects"], &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteObject removes a stored object.
func (c *Client) DeleteObject(id ObjectId) error {
	_, err := c.request(command("DeleteObject", map[string]any{"id": id}), "ObjectDeleted")
	return err
}

// SetName gives a stored object a human-readable name (local convenience).
func (c *Client) SetName(id ObjectId, name string) error {
	_, err := c.request(command("SetName", map[string]any{"id": id, "name": name}), "NameSet")
	return err
}

// ExportObject returns the object as a transferable bundle.
func (c *Client) ExportObject(id ObjectId) (*ExportBundle, error) {
	p, err := c.request(command("Export", map[string]any{"id": id}), "Exported")
	if err != nil {
		return nil, err
	}
	var b ExportBundle
	if err := fromShape(p.(map[string]any)["bundle"], &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// ImportObject imports a previously exported bundle.
func (c *Client) ImportObject(bundle ExportBundle) error {
	shape, err := toShape(bundle)
	if err != nil {
		return err
	}
	_, err = c.request(command("Import", map[string]any{"bundle": shape}), "Imported")
	return err
}

// Runtime / identity.

func (c *Client) Status() (*NodeStatus, error) {
	p, err := c.request("Status", "Status")
	if err != nil {
		return nil, err
	}
	var s NodeStatus
	if err := fromShape(p, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) Identity() (IdentityId, error) {
	p, err := c.request("Identity", "Identity")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["identity_id"].(string), nil
}

func (c *Client) Shutdown() error {
	_, err := c.request("Shutdown", "ShutdownAccepted")
	return err
}

func (c *Client) DhPublicKey() ([32]byte, error) {
	p, err := c.request("DhPublicKey", "DhPublicKey")
	if err != nil {
		return [32]byte{}, err
	}
	var key [32]byte
	if err := fromShape(p.(map[string]any)["key"], &key); err != nil {
		return [32]byte{}, err
	}
	return key, nil
}

// SaveProfile stores a new Profile version and returns the object id.
func (c *Client) SaveProfile(profile Profile) (ObjectId, error) {
	shape, err := toShape(profile)
	if err != nil {
		return "", err
	}
	p, err := c.request(command("SaveProfile", map[string]any{"profile": shape}), "ProfileSaved")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["id"].(string), nil
}

func (c *Client) LoadProfile() (*Profile, error) {
	p, err := c.request("LoadProfile", "Profile")
	if err != nil {
		return nil, err
	}
	prof, _ := p.(map[string]any)["profile"]
	if prof == nil {
		return nil, nil
	}
	var out Profile
	if err := fromShape(prof, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SaveContactList(list ContactList) (ObjectId, error) {
	shape, err := toShape(list)
	if err != nil {
		return "", err
	}
	p, err := c.request(command("SaveContactList", map[string]any{"list": shape}), "ContactListSaved")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["id"].(string), nil
}

func (c *Client) LoadContactList() (*ContactList, error) {
	p, err := c.request("LoadContactList", "ContactList")
	if err != nil {
		return nil, err
	}
	lst, _ := p.(map[string]any)["list"]
	if lst == nil {
		return nil, nil
	}
	var out ContactList
	if err := fromShape(lst, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SaveHomeIndex(index HomeIndex) (ObjectId, error) {
	shape, err := toShape(index)
	if err != nil {
		return "", err
	}
	p, err := c.request(command("SaveHomeIndex", map[string]any{"index": shape}), "HomeIndexSaved")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["id"].(string), nil
}

func (c *Client) LoadHomeIndex() (*HomeIndex, error) {
	p, err := c.request("LoadHomeIndex", "HomeIndex")
	if err != nil {
		return nil, err
	}
	idx, _ := p.(map[string]any)["index"]
	if idx == nil {
		return nil, nil
	}
	var out HomeIndex
	if err := fromShape(idx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SetHomeEntryShared(name string, shared bool) error {
	_, err := c.request(command("SetHomeEntryShared", map[string]any{"name": name, "shared": shared}), "HomeIndexSaved")
	return err
}

func (c *Client) ExportIdentity(passphrase string) ([]byte, error) {
	p, err := c.request(command("ExportIdentity", map[string]any{"passphrase": passphrase}), "IdentityExported")
	if err != nil {
		return nil, err
	}
	shape, _ := p.(map[string]any)["bytes"]
	var out []byte
	if err := fromShape(shape, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ImportIdentity(bytes []byte, passphrase string, overwrite bool) (IdentityId, error) {
	p, err := c.request(command("ImportIdentity", map[string]any{"bytes": bytes, "passphrase": passphrase, "overwrite": overwrite}), "IdentityImported")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["identity_id"].(string), nil
}

// User records across the network.

func (c *Client) PublishPointer(name string, target ObjectId) error {
	_, err := c.request(command("PublishPointer", map[string]any{"name": name, "target": target}), "PointerPublished")
	return err
}

func (c *Client) PublishAppPointer(name string, manifest ObjectId) error {
	_, err := c.request(command("PublishAppPointer", map[string]any{"name": name, "manifest": manifest}), "AppPointerPublished")
	return err
}

func (c *Client) ResolveAppPointer(owner IdentityId, name string) (*AppPointerRecord, error) {
	p, err := c.request(command("ResolveAppPointer", map[string]any{"owner": owner, "name": name}), "AppPointer")
	if err != nil {
		return nil, err
	}
	rec, _ := p.(map[string]any)["record"]
	if rec == nil {
		return nil, nil
	}
	var out AppPointerRecord
	if err := fromShape(rec, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ResolvePointer(owner IdentityId, name string) (*AppPointerRecord, error) {
	p, err := c.request(command("ResolvePointer", map[string]any{"owner": owner, "name": name}), "Pointer")
	if err != nil {
		return nil, err
	}
	rec, _ := p.(map[string]any)["record"]
	if rec == nil {
		return nil, nil
	}
	var out AppPointerRecord
	if err := fromShape(rec, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ClaimUsername(username string) error {
	_, err := c.request(command("ClaimUsername", map[string]any{"username": username}), "UsernameClaimed")
	return err
}

func (c *Client) ResolveUsername(username string) (*IdentityId, error) {
	p, err := c.request(command("ResolveUsername", map[string]any{"username": username}), "UsernameOwner")
	if err != nil {
		return nil, err
	}
	owner, _ := p.(map[string]any)["owner"].(string)
	if owner == "" && p.(map[string]any)["owner"] == nil {
		return nil, nil
	}
	return &owner, nil
}

func (c *Client) ShowUsername() (*string, error) {
	p, err := c.request("ShowUsername", "Username")
	if err != nil {
		return nil, err
	}
	u, _ := p.(map[string]any)["username"]
	if u == nil {
		return nil, nil
	}
	s := u.(string)
	return &s, nil
}

func (c *Client) ResolveProfile(owner IdentityId) (*Profile, error) {
	p, err := c.request(command("ResolveProfile", map[string]any{"owner": owner}), "Profile")
	if err != nil {
		return nil, err
	}
	prof, _ := p.(map[string]any)["profile"]
	if prof == nil {
		return nil, nil
	}
	var out Profile
	if err := fromShape(prof, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Devices & pairing.

func (c *Client) Device() (*Device, error) {
	p, err := c.request("Device", "Device")
	if err != nil {
		return nil, err
	}
	var d Device
	if err := fromShape(p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) DeviceList() ([]DeviceInfo, error) {
	p, err := c.request("DeviceList", "DeviceList")
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	if err := fromShape(p.(map[string]any)["devices"], &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ResolveOwnerDevice(owner IdentityId) (*string, error) {
	p, err := c.request(command("ResolveOwnerDevice", map[string]any{"owner": owner}), "OwnerDevice")
	if err != nil {
		return nil, err
	}
	peer, _ := p.(map[string]any)["peer_id"]
	if peer == nil {
		return nil, nil
	}
	s := peer.(string)
	return &s, nil
}

func (c *Client) AddDevice(deviceID, deviceName string) error {
	_, err := c.request(command("AddDevice", map[string]any{"device_id": deviceID, "device_name": deviceName}), "DeviceAdded")
	return err
}

func (c *Client) RemoveDevice(deviceID string) error {
	_, err := c.request(command("RemoveDevice", map[string]any{"device_id": deviceID}), "DeviceRemoved")
	return err
}

func (c *Client) InitiatePairing() (*PairingQrData, error) {
	p, err := c.request("InitiatePairing", "PairingQr")
	if err != nil {
		return nil, err
	}
	var qr PairingQrData
	if err := fromShape(p.(map[string]any)["qr"], &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}

func (c *Client) CompletePairing(qr PairingQrData, code string) (string, error) {
	qrShape, err := toShape(qr)
	if err != nil {
		return "", err
	}
	p, err := c.request(command("CompletePairing", map[string]any{"qr": qrShape, "code": code}), "PairingComplete")
	if err != nil {
		return "", err
	}
	return p.(map[string]any)["message"].(string), nil
}

func (c *Client) SyncFromPeer(peerID string) (*SyncResult, error) {
	p, err := c.request(command("SyncFromPeer", map[string]any{"peer_id": peerID}), "SyncComplete")
	if err != nil {
		return nil, err
	}
	var r SyncResult
	if err := fromShape(p.(map[string]any)["result"], &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) SyncDeviceList() (*SyncResult, error) {
	p, err := c.request("SyncDeviceList", "SyncComplete")
	if err != nil {
		return nil, err
	}
	var r SyncResult
	if err := fromShape(p.(map[string]any)["result"], &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Networking / DHT.

func (c *Client) Dial(addr string) error {
	_, err := c.request(command("Dial", map[string]any{"addr": addr}), "Dialed")
	return err
}

func (c *Client) ListenViaRelay(relayAddr string) error {
	_, err := c.request(command("ListenViaRelay", map[string]any{"relay_addr": relayAddr}), "ListeningViaRelay")
	return err
}

func (c *Client) Publish(topic string, data []byte) error {
	_, err := c.request(command("Publish", map[string]any{"topic": topic, "data": data}), "Published")
	return err
}

func (c *Client) Peers() ([]PeerInfo, error) {
	p, err := c.request("Peers", "Peers")
	if err != nil {
		return nil, err
	}
	var out []PeerInfo
	if err := fromShape(p.(map[string]any)["peers"], &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RelayReservations() ([]RelayReservationInfo, error) {
	p, err := c.request("RelayReservations", "RelayReservations")
	if err != nil {
		return nil, err
	}
	var out []RelayReservationInfo
	if err := fromShape(p.(map[string]any)["reservations"], &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) FindProviders(id ObjectId) ([]string, error) {
	p, err := c.request(command("FindProviders", map[string]any{"id": id}), "Providers")
	if err != nil {
		return nil, err
	}
	var out []string
	if err := fromShape(p.(map[string]any)["peer_ids"], &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) FetchObject(peerID string, id ObjectId) (*StoredObject, error) {
	p, err := c.request(command("FetchObject", map[string]any{"peer_id": peerID, "id": id}), "Object")
	if err != nil {
		return nil, err
	}
	var obj StoredObject
	if err := fromShape(p.(map[string]any)["object"], &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

func (c *Client) Announce(id ObjectId) error {
	_, err := c.request(command("Announce", map[string]any{"id": id}), "Announced")
	return err
}

func (c *Client) PutObjectPublic(data []byte, objectType ObjectType, name *string) (ObjectId, error) {
	return c.objectCreated(command("PutObjectPublic", map[string]any{"data": data, "object_type": objectType, "name": optString(name)}))
}

func (c *Client) ShareObject(name string, object ObjectId, app *string, recipients [][]byte) (ObjectId, error) {
	rec := make([]any, 0, len(recipients))
	for _, r := range recipients {
		arr := make([]any, len(r))
		for i, b := range r {
			arr[i] = b
		}
		rec = append(rec, arr)
	}
	return c.objectCreated(command("ShareObject", map[string]any{"name": name, "object": object, "app": optString(app), "recipients": rec}))
}

// Subscription is a persistent pub/sub stream (mirrors NodeCommand::Subscribe).
type Subscription struct {
	conn  net.Conn
	rd    *bufio.Reader
	ch    chan PubSubMessage
	done  chan struct{}
	err   error
	close chan struct{}
}

// Subscribe opens a persistent stream on `topic`. The node first replies
// Subscribed, then pushes PubSubMessage frames until the client disconnects.
func (c *Client) Subscribe(topic string) (*Subscription, error) {
	payload, err := EncodeCommand(command("Subscribe", map[string]any{"topic": topic}))
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", c.SocketPath)
	if err != nil {
		return nil, &NodeNotRunningError{}
	}
	rd := bufio.NewReader(conn)
	if err := writeFrame(conn, payload); err != nil {
		conn.Close()
		return nil, err
	}
	first, err := readFrame(rd)
	if err != nil {
		conn.Close()
		return nil, err
	}
	value, err := DecodeResponse(first)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if s, ok := value.(string); !ok || s != "Subscribed" {
		conn.Close()
		return nil, fmt.Errorf("subscribe rejected: %v", value)
	}
	sub := &Subscription{
		conn:  conn,
		rd:    rd,
		ch:    make(chan PubSubMessage, 16),
		done:  make(chan struct{}),
		close: make(chan struct{}),
	}
	go sub.readLoop()
	return sub, nil
}

func (s *Subscription) readLoop() {
	defer close(s.done)
	defer close(s.ch)
	for {
		select {
		case <-s.close:
			return
		default:
		}
		frame, err := readFrame(s.rd)
		if err != nil {
			s.err = err
			return
		}
		value, err := DecodeResponse(frame)
		if err != nil {
			s.err = err
			return
		}
		inner, err := unwrap(value, "PubSub")
		if err != nil {
			s.err = err
			return
		}
		var msg PubSubMessage
		if err := fromShape(inner, &msg); err != nil {
			s.err = err
			return
		}
		s.ch <- msg
	}
}

// Channel yields PubSubMessages until the stream ends or Close is called.
func (s *Subscription) Channel() (<-chan PubSubMessage, <-chan struct{}) {
	return s.ch, s.done
}

// Next blocks for the next message; (nil, nil) when the stream ends.
func (s *Subscription) Next() (*PubSubMessage, error) {
	msg, more := <-s.ch
	if !more {
		return nil, s.err
	}
	return &msg, nil
}

// Close ends the stream.
func (s *Subscription) Close() {
	select {
	case <-s.close:
	default:
		close(s.close)
	}
	s.conn.Close()
	<-s.done
}

// Capabilities.

func (c *Client) GrantCapability(subject IdentityId, resource Resource, permissions []Permission, expiresAt *uint64) (*Capability, error) {
	exp := any(nil)
	if expiresAt != nil {
		exp = *expiresAt
	}
	p, err := c.request(command("GrantCapability", map[string]any{
		"subject":     subject,
		"resource":    resource,
		"permissions": permissions,
		"expires_at":  exp,
	}), "CapabilityGranted")
	if err != nil {
		return nil, err
	}
	var cap Capability
	if err := fromShape(p.(map[string]any)["capability"], &cap); err != nil {
		return nil, err
	}
	return &cap, nil
}

func (c *Client) ListCapabilities() (*CapabilityIndex, error) {
	p, err := c.request("ListCapabilities", "Capabilities")
	if err != nil {
		return nil, err
	}
	idx, _ := p.(map[string]any)["index"]
	if idx == nil {
		return nil, nil
	}
	var out CapabilityIndex
	if err := fromShape(idx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RevokeCapability(id CapabilityId) error {
	_, err := c.request(command("RevokeCapability", map[string]any{"id": id}), "CapabilityRevoked")
	return err
}

func (c *Client) CheckCapability(cap Capability) (valid bool, reason string, err error) {
	shape, err := toShape(cap)
	if err != nil {
		return false, "", err
	}
	p, err := c.request(command("CheckCapability", map[string]any{"capability": shape}), "CapabilityCheck")
	if err != nil {
		return false, "", err
	}
	m := p.(map[string]any)
	return m["valid"].(bool), m["reason"].(string), nil
}

func (c *Client) CheckAccess(subject IdentityId, permission Permission, resource Resource) (bool, error) {
	p, err := c.request(command("CheckAccess", map[string]any{
		"subject":    subject,
		"permission": permission,
		"resource":   resource,
	}), "AccessAllowed")
	if err != nil {
		return false, err
	}
	return p.(map[string]any)["allowed"].(bool), nil
}

// Serve sessions.

func (c *Client) StartServeSession(edgeAddr string, appID ObjectId) (rootURL string, err error) {
	p, err := c.request(command("StartServeSession", map[string]any{"edge_addr": edgeAddr, "app_id": appID}), "ServeSessionStarted")
	if err != nil {
		return "", err
	}
	m := p.(map[string]any)
	return m["root_url"].(string), nil
}

func (c *Client) StopServeSession() error {
	_, err := c.request("StopServeSession", "ServeSessionStopped")
	return err
}

// helpers

func optString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

var errNotConnected = errors.New("not connected")
