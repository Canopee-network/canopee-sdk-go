package canopee

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live end-to-end test: drives a real `canopee-node` process (built from the
// main Canopee repo) purely through the Go SDK, mirroring the TypeScript and
// Rust integration scenarios.
//
// The node binary is located via CANOPEE_NODE_BIN (falls back to canopee-node
// on PATH); the test is skipped when none is available.

func nodeBinary(t *testing.T) string {
	if bin := os.Getenv("CANOPEE_NODE_BIN"); bin != "" {
		return bin
	}
	if _, err := exec.LookPath("canopee-node"); err == nil {
		return "canopee-node"
	}
	t.Skip("no canopee-node binary (set CANOPEE_NODE_BIN)")
	return ""
}

func spawnNode(t *testing.T, root string) *exec.Cmd {
	bin := nodeBinary(t)
	logf, err := os.OpenFile(filepath.Join(root, "node.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logf.Close() })
	cmd := exec.Command(bin)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = append(os.Environ(), "CANOPEE_APP_ROOT="+root, "CANOPEE_MDNS=0", "CANOPEE_BOOTSTRAP_ADDRS=", "RUST_LOG=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn node: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait() //nolint:errcheck
		}
	})
	return cmd
}

func waitForSocket(t *testing.T, socketPath string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("node socket never appeared at %s", socketPath)
}

func TestLiveNodeE2E(t *testing.T) {
	bin := nodeBinary(t)
	_ = bin
	root, err := os.MkdirTemp("", "canopee-e2e-go-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("CANOPEE_APP_ROOT", root)

	spawnNode(t, root)
	socketPath := filepath.Join(root, ".canopee", "node.sock")
	waitForSocket(t, socketPath)

	client := NewClient(socketPath)

	// ---- identity / status ----
	identity, err := client.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(identity, "canopee://identity/") {
		t.Fatalf("identity uri: %s", identity)
	}
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Identity != identity {
		t.Fatalf("status identity: %s", status.Identity)
	}
	if status.Peers != 0 {
		t.Fatalf("peers: %d", status.Peers)
	}
	if status.Objects < 1 {
		t.Fatalf("objects: %d", status.Objects)
	}

	// ---- storage ----
	id1, err := client.Put([]byte("live e2e (go)"), nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := client.Get(id1)
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Payload.Data) != "live e2e (go)" {
		t.Fatalf("data: %q", obj.Payload.Data)
	}
	if obj.ID != id1 {
		t.Fatalf("id: %s", obj.ID)
	}

	name := "e2e-name"
	if err := client.SetName(id1, name); err != nil {
		t.Fatal(err)
	}
	infos, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	var info *ObjectInfo
	for i := range infos {
		if infos[i].ID == id1 {
			info = &infos[i]
			break
		}
	}
	if info == nil {
		t.Fatal("stored object must be listed")
	}
	if info.Name == nil || *info.Name != name {
		t.Fatalf("name: %v", info.Name)
	}
	if info.ObjectType != ObjectTypeBlob {
		t.Fatalf("object type: %s", info.ObjectType)
	}

	a, err := client.Put([]byte("hello "), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Put([]byte("world"), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := client.Concat([]ObjectId{a, b}, ptr("greeting"))
	if err != nil {
		t.Fatal(err)
	}
	if joined == a {
		t.Fatal("concat returned origin")
	}
	joinedObj, err := client.Get(joined)
	if err != nil {
		t.Fatal(err)
	}
	if string(joinedObj.Payload.Data) != "hello world" {
		t.Fatalf("concat data: %q", joinedObj.Payload.Data)
	}

	bundle, err := client.ExportObject(id1)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Object.ID != id1 {
		t.Fatalf("bundle id: %s", bundle.Object.ID)
	}
	if err := client.DeleteObject(id1); err != nil {
		t.Fatal(err)
	}
	if err := client.ImportObject(*bundle); err != nil {
		t.Fatal(err)
	}
	reimported, err := client.Get(id1)
	if err != nil {
		t.Fatal(err)
	}
	if string(reimported.Payload.Data) != "live e2e (go)" {
		t.Fatalf("reimported data: %q", reimported.Payload.Data)
	}

	if err := client.DeleteObject(joined); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(joined); err == nil {
		t.Fatal("deleted object is gone")
	}
	if _, err := client.Get(strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing object is gone")
	}

	// ---- runtime / identity ----
	key, err := client.DhPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("dh public key length: %d", len(key))
	}

	profile, err := client.LoadProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile != nil {
		t.Fatalf("initial profile: %+v", profile)
	}
	dh := [32]byte{7}
	if _, err := client.SaveProfile(Profile{DisplayName: "alice", DhPublicKey: dh, Avatar: nil, Version: 0}); err != nil {
		t.Fatal(err)
	}
	profile, err = client.LoadProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.DisplayName != "alice" {
		t.Fatalf("display name: %q", profile.DisplayName)
	}
	if profile.Version != 1 {
		t.Fatalf("version: %d", profile.Version)
	}
	if _, err := client.SaveProfile(Profile{DisplayName: "alice", DhPublicKey: dh, Avatar: nil, Version: 0}); err != nil {
		t.Fatal(err)
	}
	profile, err = client.LoadProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Version != 2 {
		t.Fatalf("version after re-save: %d", profile.Version)
	}

	cl, err := client.LoadContactList()
	if err != nil {
		t.Fatal(err)
	}
	if cl != nil {
		t.Fatalf("initial contact list: %+v", cl)
	}
	dh9 := [32]byte{9}
	if _, err := client.SaveContactList(ContactList{
		Contacts: []Contact{{Name: "bob", PeerID: "12D3KooB", DhPublicKey: dh9, Note: nil}},
		Version:  0,
	}); err != nil {
		t.Fatal(err)
	}
	cl, err = client.LoadContactList()
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.Contacts) != 1 || cl.Contacts[0].Name != "bob" {
		t.Fatalf("contacts: %+v", cl.Contacts)
	}
	if cl.Version != 1 {
		t.Fatalf("contact list version: %d", cl.Version)
	}

	app := "e2e"
	pic, err := client.PutObject([]byte("png bytes"), ObjectTypeBlob, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SaveHomeIndex(HomeIndex{
		Profile:  nil,
		Contacts: nil,
		Entries:  []HomeEntry{{Name: "pic.png", Object: pic, ObjectType: ObjectTypeBlob, Shared: false, App: &app}},
		Version:  0,
	}); err != nil {
		t.Fatal(err)
	}
	index, err := client.LoadHomeIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 1 || index.Entries[0].Shared {
		t.Fatalf("home index: %+v", index.Entries)
	}
	if err := client.SetHomeEntryShared("pic.png", true); err != nil {
		t.Fatal(err)
	}
	index, err = client.LoadHomeIndex()
	if err != nil {
		t.Fatal(err)
	}
	if !index.Entries[0].Shared {
		t.Fatal("entry must be shared")
	}
	if err := client.SetHomeEntryShared("missing", true); err == nil {
		t.Fatal("unknown entry rejected")
	}

	// ---- user records ----
	ownProfile, err := client.ResolveProfile(identity)
	if err != nil {
		t.Fatal(err)
	}
	if ownProfile == nil || ownProfile.DisplayName != "alice" {
		t.Fatalf("own profile: %+v", ownProfile)
	}

	// Pointers are cached locally and published fire-and-forget on the DHT, so
	// a self publish resolves immediately even with zero peers.
	if err := client.PublishPointer("e2e:demo", id1); err != nil {
		t.Fatal(err)
	}
	rec, err := client.ResolvePointer(identity, "e2e:demo")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("just-published pointer must resolve")
	}
	if rec.Manifest != id1 || rec.Name != "e2e:demo" {
		t.Fatalf("pointer: %+v", rec)
	}

	// App pointers await the DHT put directly, which needs at least one peer;
	// on a hermetic single node they fail cleanly with the quorum error.
	err = client.PublishAppPointer("my-app", id1)
	var cmdErr *Error
	if err == nil || !errors.As(err, &cmdErr) || !strings.Contains(cmdErr.Message, "quorum") {
		t.Fatalf("app pointer error: %v", err)
	}

	if err := client.Announce(id1); err != nil {
		t.Fatal(err)
	}
	peers, err := client.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Fatalf("peers: %d", len(peers))
	}

	// publishing with zero subscribers fails cleanly, not hanging
	err = client.Publish("e2e-topic", []byte("ping"))
	var pubErr *Error
	if err == nil || !errors.As(err, &pubErr) {
		t.Fatalf("publish with zero subscribers: %v", err)
	}

	// ---- devices ----
	dev, err := client.Device()
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.PeerID) <= 10 || len(dev.DeviceName) == 0 {
		t.Fatalf("device: %+v", dev)
	}
	devs, err := client.DeviceList()
	if err != nil {
		t.Fatal(err)
	}
	own := false
	for _, d := range devs {
		if d.DeviceID == dev.PeerID {
			own = true
		}
	}
	if !own {
		t.Fatal("own device registered")
	}
	other := "12D3KooWE2eTestDeviceGo"
	if err := client.AddDevice(other, "e2e-device"); err != nil {
		t.Fatal(err)
	}
	if !someDevice(client, other) {
		t.Fatal("added device not listed")
	}
	if err := client.RemoveDevice(other); err != nil {
		t.Fatal(err)
	}
	if someDevice(client, other) {
		t.Fatal("removed device still listed")
	}

	// ---- capabilities ----
	cidx, err := client.ListCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if cidx != nil {
		t.Fatalf("initial capabilities: %+v", cidx)
	}
	cap, err := client.GrantCapability(
		"canopee://identity/bob",
		ResourceChannel("team-news"),
		[]Permission{PermissionRead, PermissionWrite},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(cap.Permissions[0]) != "Read" || string(cap.Permissions[1]) != "Write" {
		t.Fatalf("permissions: %v", cap.Permissions)
	}
	if cap.Issuer != identity {
		t.Fatalf("issuer: %s", cap.Issuer)
	}
	valid, reason, err := client.CheckCapability(*cap)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatalf("capability valid: reason=%s", reason)
	}
	res := ResourceChannel("team-news")
	if err := assertAccess(client, "canopee://identity/bob", PermissionRead, res, true, t); err != nil {
		t.Fatal(err)
	}
	if err := assertAccess(client, "canopee://identity/bob", PermissionWrite, res, true, t); err != nil {
		t.Fatal(err)
	}
	if err := assertAccess(client, "canopee://identity/bob", PermissionPublish, res, false, t); err != nil {
		t.Fatal(err)
	}
	if err := assertAccess(client, "canopee://identity/mallory", PermissionRead, res, false, t); err != nil {
		t.Fatal(err)
	}
	if err := assertAccess(client, "canopee://identity/bob", PermissionRead, ResourceChannel("other"), false, t); err != nil {
		t.Fatal(err)
	}

	cidx, err = client.ListCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if len(cidx.Entries) != 1 || cidx.Entries[0].Revoked {
		t.Fatalf("capability index: %+v", cidx.Entries)
	}

	if err := client.RevokeCapability(cap.ID); err != nil {
		t.Fatal(err)
	}
	cidx, err = client.ListCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if !cidx.Entries[0].Revoked {
		t.Fatal("capability must be revoked")
	}
	valid, reason, err = client.CheckCapability(*cap)
	if err != nil {
		t.Fatal(err)
	}
	if valid || !strings.Contains(reason, "revoked") {
		t.Fatalf("revoked capability: valid=%v reason=%s", valid, reason)
	}
	if err := assertAccess(client, "canopee://identity/bob", PermissionRead, res, false, t); err != nil {
		t.Fatal(err)
	}

	// ---- pub/sub (ack + clean close; no message can arrive without peers) ----
	sub, err := client.Subscribe("e2e-sub")
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()

	if err := client.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func assertAccess(c *Client, subject IdentityId, perm Permission, res Resource, want bool, t *testing.T) error {
	t.Helper()
	got, err := c.CheckAccess(subject, perm, res)
	if err != nil {
		return err
	}
	if got != want {
		return &Error{Message: "access mismatch"}
	}
	return nil
}

func someDevice(c *Client, id string) bool {
	devs, err := c.DeviceList()
	if err != nil {
		return false
	}
	for _, d := range devs {
		if d.DeviceID == id {
			return true
		}
	}
	return false
}
