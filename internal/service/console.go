package service

import (
	"context"
	"strconv"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Read endpoints the admin console needs beyond the product-facing API.
// All are operator-only and product-scoped like every other read.

func (s *Service) ListOrders(ctx context.Context, req *openpay_v1.ListOrdersRequest) (*openpay_v1.ListOrdersResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	f := &filter.Order{Limit: int(req.GetLimit()), Offset: int(req.GetOffset()), Scope: scope}
	if f.ProductID, err = s.scopedProductFilter(ctx, scope, req.GetProductId()); err != nil {
		return nil, err
	}
	if req.GetCustomerId() != "" {
		c, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
		if err != nil {
			return nil, err
		}
		f.CustomerID = &c.ID
	}
	for status, proto := range orderStatuses {
		if proto == req.GetStatus() {
			f.Status = status
		}
	}
	orders, total, err := s.repo.ListOrders(ctx, f)
	if err != nil {
		return nil, err
	}
	out := &openpay_v1.ListOrdersResponse{Total: total}
	for _, o := range orders {
		out.Orders = append(out.Orders, orderSummary(o))
	}
	return out, nil
}

var orderStatuses = map[dao.OrderStatus]openpay_v1.OrderStatus{
	dao.OrderPendingPayment:    openpay_v1.OrderStatus_ORDER_STATUS_PENDING_PAYMENT,
	dao.OrderPaid:              openpay_v1.OrderStatus_ORDER_STATUS_PAID,
	dao.OrderPartiallyRefunded: openpay_v1.OrderStatus_ORDER_STATUS_PARTIALLY_REFUNDED,
	dao.OrderRefunded:          openpay_v1.OrderStatus_ORDER_STATUS_REFUNDED,
	dao.OrderFailed:            openpay_v1.OrderStatus_ORDER_STATUS_FAILED,
	dao.OrderCancelled:         openpay_v1.OrderStatus_ORDER_STATUS_CANCELLED,
}

func (s *Service) SearchRefunds(ctx context.Context, req *openpay_v1.SearchRefundsRequest) (*openpay_v1.SearchRefundsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	f := &filter.Refund{Limit: int(req.GetLimit()), Offset: int(req.GetOffset()), Scope: scope}
	if f.ProductID, err = s.scopedProductFilter(ctx, scope, req.GetProductId()); err != nil {
		return nil, err
	}
	for status, proto := range refundStatuses {
		if proto == req.GetStatus() {
			f.Status = status
		}
	}
	refunds, total, err := s.repo.SearchRefunds(ctx, f)
	if err != nil {
		return nil, err
	}
	out := &openpay_v1.SearchRefundsResponse{Total: total}
	for _, r := range refunds {
		out.Refunds = append(out.Refunds, toProtoRefund(r))
	}
	return out, nil
}

// GetPaymentActivity gathers what passed between us and the provider about a
// payment and its refunds: every request-log row naming one of its attempts,
// provider ids or refunds, and every webhook about those provider objects.
func (s *Service) GetPaymentActivity(ctx context.Context, req *openpay_v1.GetPaymentActivityRequest) (*openpay_v1.GetPaymentActivityResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	p, _, err := s.visiblePayment(ctx, req.GetId(), auth.PermPaymentsRead)
	if err != nil {
		return nil, err
	}
	attempts, err := s.repo.ListPaymentAttempts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	refunds, err := s.repo.ListPaymentRefunds(ctx, p.ID)
	if err != nil {
		return nil, err
	}

	refs := []string{p.PublicID}
	objects := map[string][]string{} // provider -> its object ids
	for _, a := range attempts {
		refs = append(refs, a.PublicID)
		if a.ProviderPaymentID != nil {
			refs = append(refs, *a.ProviderPaymentID)
			objects[a.Provider] = append(objects[a.Provider], *a.ProviderPaymentID)
		}
	}
	for _, r := range refunds {
		refs = append(refs, r.PublicID)
		if r.ProviderRefundID != nil {
			refs = append(refs, *r.ProviderRefundID)
			objects[r.Provider] = append(objects[r.Provider], *r.ProviderRefundID)
		}
	}

	out := &openpay_v1.GetPaymentActivityResponse{}
	requests, err := s.repo.ListProviderRequests(ctx, refs)
	if err != nil {
		return nil, err
	}
	for _, x := range requests {
		out.Requests = append(out.Requests, &openpay_v1.ProviderRequestRecord{
			Provider: x.Provider, Operation: x.Operation, Reference: x.Reference, Request: x.Request,
			Response: x.Response, Error: x.Error, DurationMs: int32(x.DurationMs), CreatedAt: timestamppb.New(x.CreatedAt),
		})
	}
	for provider, ids := range objects {
		events, err := s.repo.ListProviderEventsFor(ctx, provider, ids)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			rec := &openpay_v1.ProviderEventRecord{
				Provider: e.Provider, EventId: e.EventID, EventType: e.EventType, ObjectKind: e.ObjectKind,
				ObjectId: deref(e.ObjectID), Payload: string(e.Payload), ReceivedAt: timestamppb.New(e.ReceivedAt),
				Attempts: int32(e.Attempts), LastError: deref(e.LastError),
			}
			if e.ProcessedAt != nil {
				rec.ProcessedAt = timestamppb.New(*e.ProcessedAt)
			}
			out.Events = append(out.Events, rec)
		}
	}
	return out, nil
}

func (s *Service) ListAuditLog(ctx context.Context, req *openpay_v1.ListAuditLogRequest) (*openpay_v1.ListAuditLogResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermAuditRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	f := &filter.Audit{ResourceType: req.GetResourceType(), ResourceID: req.GetResourceId(),
		ActorID: req.GetActorId(), Limit: int(req.GetLimit()), Scope: scope}
	if req.GetCursor() != "" {
		if f.BeforeID, err = strconv.ParseInt(req.GetCursor(), 10, 64); err != nil || f.BeforeID <= 0 {
			return nil, apperrors.New(apperrors.InvalidArgument, "cursor %q is not one this API issued", req.GetCursor())
		}
	}
	f.WithDefaults()
	// One extra row says whether an older page exists.
	limit := f.Limit
	f.Limit++
	entries, err := s.repo.ListAuditLog(ctx, f)
	if err != nil {
		return nil, err
	}
	out := &openpay_v1.ListAuditLogResponse{}
	if len(entries) > limit {
		entries = entries[:limit]
		out.NextCursor = strconv.FormatInt(entries[limit-1].ID, 10)
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, &openpay_v1.AuditEntry{
			Id: strconv.FormatInt(e.ID, 10), ActorType: string(e.ActorType), ActorId: e.ActorID, Action: e.Action,
			ResourceType: e.ResourceType, ResourceId: e.ResourceID, ProductId: deref(e.ProductPublicID),
			RequestId: e.RequestID, Before: string(e.Before), After: string(e.After), CreatedAt: timestamppb.New(e.CreatedAt),
		})
	}
	return out, nil
}

// scopedProductFilter resolves an optional product filter on a listing,
// refusing a product outside the caller's scope.
func (s *Service) scopedProductFilter(ctx context.Context, scope *filter.ProductScope, productID string) (*int64, error) {
	if productID == "" {
		return nil, nil
	}
	p, err := s.repo.GetProductByPublicID(ctx, productID)
	if err != nil {
		return nil, err
	}
	if err := requireProductInScope(scope, p.ID); err != nil {
		return nil, err
	}
	return &p.ID, nil
}
