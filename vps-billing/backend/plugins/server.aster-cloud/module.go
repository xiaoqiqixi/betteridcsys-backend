package astercloud

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/plugins/server.aster-cloud/controllerapi"
	"aster/billing/plugins/server.aster-cloud/serviceapi"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Provisioner interface {
	Send(context.Context, controllerapi.Request) (controllerapi.Result, error)
	Lookup(context.Context, controllerapi.Request) (controllerapi.Result, error)
}
type Services interface {
	Images(context.Context, string) ([]serviceapi.Image, error)
	Service(context.Context, string, string) (serviceapi.Service, error)
	Action(context.Context, string, serviceapi.ActionRequest) (serviceapi.ActionResult, bool, error)
	Operation(context.Context, string, string, string) (serviceapi.Operation, error)
	Console(context.Context, string, string) (serviceapi.Console, error)
	Metrics(context.Context, string, string, string) (serviceapi.Metrics, error)
	Snapshots(context.Context, string, string) (serviceapi.Snapshots, error)
}
type receipt struct {
	Request   serverv1.Request          `json:"request"`
	Response  serverv1.Response         `json:"response"`
	Phase     string                    `json:"phase"`
	Provision *controllerapi.Request    `json:"provision,omitempty"`
	Action    *serviceapi.ActionRequest `json:"action,omitempty"`
}
type Module struct {
	mu         sync.Mutex
	controller string
	provision  Provisioner
	services   Services
	directory  string
	lock       *os.File
}

func New(controller string, p Provisioner, s Services, directory string) (*Module, error) {
	if p == nil || s == nil || controller == "" || !filepath.IsAbs(directory) {
		return nil, serverv1.ErrConfiguration
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, serverv1.ErrUnavailable
	}
	lock, err := os.OpenFile(filepath.Join(directory, "worker.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, serverv1.ErrUnavailable
	}
	return &Module{controller: controller, provision: p, services: s, directory: directory, lock: lock}, nil
}
func (m *Module) Close() error          { return m.lock.Close() }
func key(id string) string              { h := sha256.Sum256([]byte(id)); return hex.EncodeToString(h[:]) }
func (m *Module) path(id string) string { return filepath.Join(m.directory, key(id)+".json") }
func (m *Module) load(id string) (receipt, error) {
	var r receipt
	f, err := os.OpenFile(m.path(id), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > serverv1.MaximumPayload || info.Mode().Perm()&0077 != 0 {
		return r, serverv1.ErrUnavailable
	}
	raw := make([]byte, info.Size())
	if _, err = f.ReadAt(raw, 0); err != nil {
		return r, serverv1.ErrUncertain
	}
	if json.Unmarshal(raw, &r) != nil || r.Request.OperationID != id || len(r.Request.Secrets) > 0 {
		return r, serverv1.ErrUncertain
	}
	return r, nil
}
func (m *Module) save(r receipt, create bool) error {
	if len(r.Request.Secrets) > 0 || r.Action != nil && r.Action.Params.Password != "" {
		return serverv1.ErrConfiguration
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > serverv1.MaximumPayload {
		return serverv1.ErrConfiguration
	}
	path := m.path(r.Request.OperationID)
	tmp := path + ".tmp"
	// A stale temporary file has no authority; the committed receipt determines
	// whether a network operation may be sent. Exclusive worker lock serializes it.
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return serverv1.ErrUnavailable
	}
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return serverv1.ErrUnavailable
	}
	if create {
		err = os.Link(tmp, path)
	} else {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return serverv1.ErrUnavailable
	}
	dir, err := os.Open(m.directory)
	if err != nil {
		return serverv1.ErrUnavailable
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *Module) Call(ctx context.Context, in serverv1.Request) (serverv1.Response, error) {
	if ctx.Err() != nil {
		return serverv1.Response{}, ctx.Err()
	}
	if in.Verify() != nil {
		return serverv1.Response{}, serverv1.ErrConfiguration
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := serverv1.Response{Protocol: serverv1.Protocol, State: "done", OperationID: in.OperationID, Digest: in.Digest}
	switch in.Method {
	case "describe":
		d := Description()
		out.Description = &d
		return out, nil
	case "validate_product":
		var err error
		out.Config, out.ConfigDigest, err = Description().ProductSchema.Normalize(in.Config)
		return out, err
	case "test_connection", "images":
		images, err := m.services.Images(ctx, strings.Repeat("0", 32))
		if err != nil {
			return out, serverv1.ErrUnavailable
		}
		if in.Method == "images" {
			convert(images, &out.Images)
		}
		return out, nil
	case "operation_status":
		r, err := m.load(in.OperationID)
		if err != nil || r.Request.Digest != in.Digest || in.ServiceID != "" && in.ServiceID != r.Request.ServiceID || in.OwnerRef != "" && in.OwnerRef != r.Request.OwnerRef {
			return out, serverv1.ErrUncertain
		}
		if err = m.lookup(ctx, &r); err != nil {
			return out, serverv1.ErrUncertain
		}
		return r.Response, nil
	case "status":
		remote, err := m.services.Service(ctx, in.ResourceID, in.OwnerRef)
		if err != nil {
			return out, serverv1.ErrUnavailable
		}
		var service serverv1.Service
		convert(remote, &service)
		service.Protocol = serverv1.Protocol
		service.Capabilities.Rescue = false
		service.CustomActions = []serverv1.CustomAction{}
		out.Service = &service
		return out, nil
	case "console":
		value, err := m.services.Console(ctx, in.ResourceID, in.OwnerRef)
		convert(value, &out.Console)
		return out, err
	case "metrics":
		value, err := m.services.Metrics(ctx, in.ResourceID, in.OwnerRef, in.Range)
		convert(value, &out.Metrics)
		return out, err
	case "snapshots":
		value, err := m.services.Snapshots(ctx, in.ResourceID, in.OwnerRef)
		convert(value, &out.Snapshots)
		return out, err
	}
	if !Description().Has(in.Method) {
		return out, serverv1.ErrUnavailable
	}
	if previous, err := m.load(in.OperationID); err == nil {
		if previous.Request.Digest != in.Digest {
			return out, serverv1.ErrConfiguration
		}
		return previous.Response, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, serverv1.ErrUncertain
	}
	if err := validateInput(in); err != nil {
		return out, err
	}
	r := receipt{Request: in.Redacted(), Response: out}
	r.Response.State = "uncertain"
	if in.Method == "create" {
		normalized, _, err := Description().ProductSchema.Normalize(in.Config)
		if err != nil {
			return out, err
		}
		r.Request.Config = normalized
		if in.ResourceID != "" {
			// Explicit retry of a definitively failed setup. Verify ownership and
			// continue on the already allocated resource; never send create again.
			remote, err := m.services.Service(ctx, in.ResourceID, in.OwnerRef)
			if err != nil || remote.ResourceID != in.ResourceID || remote.OwnerRef != in.OwnerRef || stringField(in.Input, "image_id") == "" {
				return out, serverv1.ErrConfiguration
			}
			r.Response.ResourceID = in.ResourceID
			r.Response.State = "accepted"
			r.Phase = "setup_ready"
			if err = m.save(r, true); err != nil {
				return out, err
			}
			if err = m.startSetup(ctx, &r); err != nil {
				return out, serverv1.ErrUncertain
			}
			return r.Response, nil
		}
		if password, ok := in.Secrets["password"].(string); ok && password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
			if err != nil {
				return out, serverv1.ErrConfiguration
			}
			r.Request.Input = cloneFields(in.Input)
			r.Request.Input["password_hash"] = string(hash)
		}
		request, err := m.createRequest(r.Request)
		if err != nil {
			return out, err
		}
		r.Provision = &request
		r.Phase = "create_inflight"
		if err = m.save(r, true); err != nil {
			return out, err
		}
		result, err := m.provision.Send(ctx, request)
		if err == nil && result.Matches(request) {
			m.createResult(&r, result)
		}
		if err = m.save(r, false); err != nil {
			return out, serverv1.ErrUncertain
		}
		// A synchronous create may start its setup in this same call. A deferred
		// create is advanced by Run; operation_status itself never sends a mutation.
		if r.Phase == "setup_ready" {
			if err = m.startSetup(ctx, &r); err != nil {
				return out, serverv1.ErrUncertain
			}
		}
	} else {
		request, err := m.actionRequest(in)
		if err != nil {
			return out, err
		}
		safe := request
		safe.Params = safe.Params.Redacted()
		r.Action = &safe
		r.Phase = "action_inflight"
		if err = m.save(r, true); err != nil {
			return out, err
		}
		result, _, err := m.services.Action(ctx, in.ResourceID, request)
		if err == nil && result.OperationID == request.OperationID {
			switch result.Status {
			case "accepted":
				r.Response.State = "accepted"
			case "rejected":
				r.Response.State = "failed"
				r.Phase = "terminal"
			}
		}
		if err = m.save(r, false); err != nil {
			return out, serverv1.ErrUncertain
		}
	}
	return r.Response, nil
}
func (m *Module) createRequest(in serverv1.Request) (controllerapi.Request, error) {
	spec := controllerapi.ProductSpec{Mode: "preset", ControllerID: m.controller, RemoteProductID: stringField(in.Config, "preset_id")}
	hash, err := spec.Digest()
	if err != nil {
		return controllerapi.Request{}, serverv1.ErrConfiguration
	}
	hostname := strings.ToLower(strings.Split(stringField(in.Input, "hostname"), ".")[0])
	hostname = strings.Trim(hostname, "-")
	if len(hostname) > 63 {
		hostname = strings.TrimRight(hostname[:63], "-")
	}
	if hostname == "" {
		hostname = "service-" + key(in.ServiceID)[:12]
	}
	r := controllerapi.Request{Protocol: controllerapi.Protocol, OperationID: upstreamID(in.OperationID), OrderID: upstreamID(in.ServiceID), OwnerRef: in.OwnerRef, Hostname: hostname, ConfigurationRevision: 1, ConfigurationSHA256: hash, Product: spec}
	_, _, err = r.BytesAndDigest()
	return r, err
}
func upstreamID(id string) string {
	if len(id) == 32 {
		if _, err := hex.DecodeString(id); err == nil {
			return id
		}
	}
	return key(id)[:32]
}
func (m *Module) createResult(r *receipt, result controllerapi.Result) {
	switch result.Status {
	case "accepted":
		r.Response.State = "accepted"
	case "rejected":
		r.Response.State = "failed"
		r.Phase = "terminal"
	case "succeeded":
		if !serviceapi.ValidID(result.ResourceID) {
			return
		}
		r.Response.ResourceID = result.ResourceID
		if stringField(r.Request.Input, "image_id") != "" {
			r.Phase = "setup_ready"
			r.Response.State = "accepted"
		} else {
			r.Phase = "terminal"
			r.Response.State = "done"
		}
	}
}
func (m *Module) startSetup(ctx context.Context, r *receipt) error {
	request := r.Request
	request.Method = "reinstall"
	request.ResourceID = r.Response.ResourceID
	request.OperationID = key(r.Request.OperationID + ":setup")[:32]
	var keys []string
	convert(request.Input["ssh_keys"], &keys)
	if stringField(request.Input, "password_hash") == "" && len(keys) == 0 {
		password := make([]byte, 32)
		if _, err := rand.Read(password); err != nil {
			return err
		}
		request.Secrets = serverv1.Fields{"password": hex.EncodeToString(password)}
		clear(password)
	}
	action, err := m.actionRequest(request)
	if err != nil {
		return err
	}
	safe := action
	safe.Params = safe.Params.Redacted()
	r.Action = &safe
	r.Phase = "setup_inflight"
	if err = m.save(*r, false); err != nil {
		return err
	}
	result, _, sendErr := m.services.Action(ctx, r.Response.ResourceID, action)
	r.Response.State = "uncertain"
	if sendErr == nil && result.OperationID == action.OperationID {
		switch result.Status {
		case "accepted":
			r.Response.State = "accepted"
		case "rejected":
			r.Response.State = "failed"
			r.Phase = "terminal"
		}
	}
	return m.save(*r, false)
}
func (m *Module) lookup(ctx context.Context, r *receipt) error {
	if r.Phase == "terminal" || r.Phase == "setup_ready" {
		return nil
	}
	if r.Phase == "create_inflight" {
		if r.Provision == nil {
			return serverv1.ErrUncertain
		}
		result, err := m.provision.Lookup(ctx, *r.Provision)
		if err != nil || !result.Matches(*r.Provision) {
			return serverv1.ErrUncertain
		}
		m.createResult(r, result)
	} else {
		if r.Action == nil {
			return serverv1.ErrUncertain
		}
		resource := r.Request.ResourceID
		if r.Phase == "setup_inflight" {
			resource = r.Response.ResourceID
		}
		result, err := m.services.Operation(ctx, resource, r.Request.OwnerRef, r.Action.OperationID)
		if err != nil || result.OperationID != r.Action.OperationID || result.Action != r.Action.Action {
			return serverv1.ErrUncertain
		}
		switch result.Status {
		case "accepted", "running":
			r.Response.State = "accepted"
		case "succeeded":
			r.Response.State = "done"
			r.Phase = "terminal"
		case "failed":
			r.Response.State = "failed"
			r.Phase = "terminal"
		default:
			r.Response.State = "uncertain"
		}
	}
	return m.save(*r, false)
}

// Run advances known setup stages and reconciles receipts. It never repeats an
// in-flight create/action, even after a process crash or a lost acknowledgement.
func (m *Module) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := m.Advance(ctx); err != nil && ctx.Err() != nil {
				return nil
			}
		}
	}
}
func (m *Module) Advance(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
			continue
		}
		if count >= 100 {
			break
		}
		raw, err := os.ReadFile(filepath.Join(m.directory, entry.Name()))
		if err != nil {
			return err
		}
		var r receipt
		if len(raw) > serverv1.MaximumPayload || json.Unmarshal(raw, &r) != nil {
			return serverv1.ErrUncertain
		}
		if r.Phase == "terminal" {
			continue
		}
		count++
		if err = m.lookup(ctx, &r); err != nil {
			continue
		}
		if r.Phase == "setup_ready" {
			if err = m.startSetup(ctx, &r); err != nil {
				return err
			}
		}
	}
	return nil
}
func convert(from, to any) { b, _ := json.Marshal(from); _ = json.Unmarshal(b, to) }
func cloneFields(in serverv1.Fields) serverv1.Fields {
	out := serverv1.Fields{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func stringField(in serverv1.Fields, key string) string { v, _ := in[key].(string); return v }
