package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func (s *Service) ListProviders(ctx context.Context, req *openpay_v1.ListProvidersRequest) (*openpay_v1.ListProvidersResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermProvidersRead); err != nil {
		return nil, err
	}
	controls, err := s.repo.ListProviderControls(ctx)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListProvidersResponse{}
	for i, name := range s.registry.Names() {
		response.Providers = append(response.Providers, s.providerStatus(name, i+1, controls[name]))
	}
	return response, nil
}

func (s *Service) SetProviderOverride(ctx context.Context, req *openpay_v1.SetProviderOverrideRequest) (*openpay_v1.SetProviderOverrideResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermProvidersManage); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	if _, err := s.registry.Get(req.GetName()); err != nil {
		return nil, err
	}
	control := provider.Control{Reason: req.GetReason()}
	switch req.GetOverride() {
	case openpay_v1.ProviderOverride_PROVIDER_OVERRIDE_DISABLED:
		control.Disabled = true
	case openpay_v1.ProviderOverride_PROVIDER_OVERRIDE_FORCED:
		control.Forced = true
	case openpay_v1.ProviderOverride_PROVIDER_OVERRIDE_NONE:
	default:
		return nil, apperrors.New(apperrors.InvalidArgument, "unknown override")
	}
	caller, _ := appcontext.CallerFrom(ctx)
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := s.repo.SetProviderControl(ctx, req.GetName(), control, caller.UserID); err != nil {
			return err
		}
		return s.audit(ctx, auditParams{Action: "provider.override_set", ResourceType: "provider", ResourceID: req.GetName(),
			After: map[string]any{"override": req.GetOverride().String(), "reason": req.GetReason()}})
	})
	if err != nil {
		return nil, err
	}
	priority := 0
	for i, name := range s.registry.Names() {
		if name == req.GetName() {
			priority = i + 1
		}
	}
	return &openpay_v1.SetProviderOverrideResponse{Provider: s.providerStatus(req.GetName(), priority, control)}, nil
}

func (s *Service) providerStatus(name string, priority int, c provider.Control) *openpay_v1.ProviderStatus {
	h, _ := s.registry.Health(name)
	out := &openpay_v1.ProviderStatus{Name: name, Priority: int32(priority), Healthy: h.Healthy,
		ConsecutiveFailures: int32(h.ConsecutiveFailures), LastError: h.LastError,
		Disabled: c.Disabled, Forced: c.Forced, OverrideReason: c.Reason}
	if !h.OpenUntil.IsZero() && !h.Healthy {
		out.CircuitOpenUntil = timestamppb.New(h.OpenUntil)
	}
	return out
}
