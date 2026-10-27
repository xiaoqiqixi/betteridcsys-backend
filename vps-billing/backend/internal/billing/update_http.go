package billing

import (
	"aster/billing/internal/updates"
	"aster/billing/pkg/pluginapi"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

type updateAuditStore interface {
	RecordUpdateHandoff(context.Context, User, updates.Intent) error
}

// UpdateService is injected only by trusted application setup. The production
// implementation always uses the fixed official repository and performs GETs.
type UpdateService interface {
	Snapshot() updates.View
	Check(context.Context) (updates.View, error)
	Prepare(context.Context, string, string, string, string, int64, string) (updates.Handoff, error)
}

// HostOperationSource exposes only a trusted local receipt snapshot. It cannot
// launch or authorize an operation and is independent of release catalog GETs.
type HostOperationSource interface {
	Snapshot() updates.OperationView
}

func (a *App) updateHTTP(w http.ResponseWriter, r *http.Request, actor User, path string) {
	if path == "/admin/updates/operation" {
		if r.Method != "GET" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		view := updates.OperationView{State: "unconfigured"}
		if a.Config.HostOperations != nil {
			view = a.Config.HostOperations.Snapshot()
		}
		jsonOut(w, 200, view)
		return
	}
	service := a.Config.Updates
	if path == "/admin/updates" {
		if r.Method != "GET" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
			return
		}
		view := (*updates.Service)(nil).Snapshot()
		if service != nil {
			view = service.Snapshot()
		}
		if service == nil {
			state := a.Config.Plugins.State()
			view.Current = updates.Current{CoreVersion: pluginapi.CoreVersion, SMTPVersion: state.Version, SMTPStatus: state.Status, PluginPolicyRevision: state.PolicyRevision}
		}
		jsonOut(w, 200, view)
		return
	}
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	if path == "/admin/updates/check" {
		var input struct{}
		if !updateBody(w, r, &input) {
			return
		}
		if service == nil {
			updateError(w, r, updates.ErrUnavailable)
			return
		}
		view, err := service.Check(r.Context())
		if err != nil {
			updateError(w, r, err)
			return
		}
		jsonOut(w, 200, view)
		return
	}
	var input struct {
		Component     string `json:"component"`
		Tag           string `json:"tag"`
		SourceSHA     string `json:"source_sha"`
		ReleaseID     int64  `json:"release_id"`
		CatalogSHA256 string `json:"catalog_sha256"`
		Acknowledged  bool   `json:"acknowledge_manual_handoff"`
	}
	if !updateBody(w, r, &input) {
		return
	}
	if !input.Acknowledged {
		fail(w, r, 400, "UPDATE_ACK_REQUIRED", "请明确确认需要由主机管理员在服务器启动受保护的更新工具")
		return
	}
	if service == nil {
		updateError(w, r, updates.ErrUnavailable)
		return
	}
	handoff, err := service.Prepare(r.Context(), actor.ID, input.Component, input.Tag, input.SourceSHA, input.ReleaseID, input.CatalogSHA256)
	if err != nil {
		updateError(w, r, err)
		return
	}
	store, ok := a.Store.(updateAuditStore)
	if !ok {
		a.internal(w, r, errors.New("update audit unavailable"))
		return
	}
	if err := store.RecordUpdateHandoff(r.Context(), actor, handoff.Intent); err != nil {
		a.internal(w, r, err)
		return
	}
	jsonOut(w, 202, handoff)
}

func updateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, updates.ErrBusy):
		fail(w, r, 409, "UPDATE_BUSY", "已有更新检查正在进行，请稍后重试")
	case errors.Is(err, updates.ErrStale):
		fail(w, r, 409, "UPDATE_STALE", "发布版本或本机状态已变化，请重新检查后选择")
	case errors.Is(err, updates.ErrIncompatible):
		fail(w, r, 409, "UPDATE_INCOMPATIBLE", "目标版本不兼容或没有更新，请先完成主机兼容性检查")
	default:
		fail(w, r, 503, "UPDATE_UNAVAILABLE", "无法验证官方发布信息；请检查主机网络和私有仓库只读授权")
	}
}

func updateBody(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "请使用 JSON 请求")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || len(raw) == 0 || pluginapi.DecodeStrict(raw, 4096, target) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		fail(w, r, 400, "INVALID_BODY", "请求格式不正确或包含重复、不支持的字段")
		return false
	}
	return true
}

func updateAudits(actor User, intent updates.Intent) ([]Audit, error) {
	if actor.Role != "admin" || actor.ID != intent.ActorID || intent.Schema != 2 || intent.Repository != updates.Repository || intent.RepositoryID != updates.RepositoryID || len(intent.RequestID) != 32 || !updates.ValidDistributionAssets(intent.Component, intent.Version, intent.DistributionFormat, intent.Assets) {
		return nil, errors.New("invalid update audit")
	}
	assets := intent.Assets
	intent.Assets = nil
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > 1000 {
		return nil, errors.New("invalid update audit")
	}
	// All executable identity facts remain in the durable audit. No tokens,
	// credentials, private filesystem paths or provider response text are stored.
	now := time.Now().UTC()
	audits := []Audit{{ID: intent.RequestID, UserID: actor.ID, ActorEmail: actor.Email, Action: "update.handoff_requested", Target: fmt.Sprintf("%s:%s", intent.Component, intent.Tag), CreatedAt: now, Detail: string(raw)}}
	// Keep the established 1000-character schema: asset facts are separate audit
	// rows bound to the same request ID, committed atomically with the request.
	for _, asset := range assets {
		raw, err := json.Marshal(struct {
			RequestID string        `json:"request_id"`
			Asset     updates.Asset `json:"asset"`
		}{intent.RequestID, asset})
		if err != nil || len(raw) > 1000 {
			return nil, errors.New("invalid update audit")
		}
		audits = append(audits, Audit{ID: ID(), UserID: actor.ID, ActorEmail: actor.Email, Action: "update.asset_verified", Target: intent.RequestID, CreatedAt: now, Detail: string(raw)})
	}
	return audits, nil
}
func (m *Memory) RecordUpdateHandoff(_ context.Context, actor User, intent updates.Intent) error {
	audits, err := updateAudits(actor, intent)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, audits...)
	return nil
}
func (m *MySQL) RecordUpdateHandoff(ctx context.Context, actor User, intent updates.Intent) error {
	audits, err := updateAudits(actor, intent)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, audit := range audits {
		if err := insertEmailAudit(ctx, tx, audit); err != nil {
			return err
		}
	}
	return tx.Commit()
}
