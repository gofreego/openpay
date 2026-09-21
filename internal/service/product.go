package service

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func (s *Service) CreateProduct(ctx context.Context, req *openpay_v1.CreateProductRequest) (*openpay_v1.CreateProductResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermProductsWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	// idempotent owns the transaction, so the product row, its audit entry and
	// the recorded response all commit together.
	return idempotent(ctx, s.repo, "CreateProduct", req,
		func(ctx context.Context) (*openpay_v1.CreateProductResponse, error) {
			product := &dao.Product{
				PublicID:        ids.New(ids.Product),
				Code:            req.GetCode(),
				Name:            req.GetName(),
				Status:          dao.ProductActive,
				DefaultCurrency: req.GetDefaultCurrency(),
			}

			if err := s.repo.CreateProduct(ctx, product); err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action:       "product.created",
				ResourceType: "product",
				ResourceID:   product.PublicID,
				ProductID:    &product.ID,
				After:        product,
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.CreateProductResponse{Product: toProtoProduct(product)}, nil
		})
}

func (s *Service) GetProduct(ctx context.Context, req *openpay_v1.GetProductRequest) (*openpay_v1.GetProductResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermProductsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	product, err := s.repo.GetProductByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetProductResponse{Product: toProtoProduct(product)}, nil
}

func (s *Service) ListProducts(ctx context.Context, req *openpay_v1.ListProductsRequest) (*openpay_v1.ListProductsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermProductsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	products, total, err := s.repo.ListProducts(ctx, &filter.Product{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
		Search: req.GetSearch(),
		Status: fromProtoProductStatus(req.GetStatus()),
	})
	if err != nil {
		return nil, err
	}

	response := &openpay_v1.ListProductsResponse{Total: total}
	for _, product := range products {
		response.Products = append(response.Products, toProtoProduct(product))
	}
	return response, nil
}

func (s *Service) UpdateProduct(ctx context.Context, req *openpay_v1.UpdateProductRequest) (*openpay_v1.UpdateProductResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermProductsWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "UpdateProduct", req,
		func(ctx context.Context) (*openpay_v1.UpdateProductResponse, error) {
			updated := &dao.Product{
				PublicID: req.GetId(),
				Name:     req.GetName(),
				Status:   fromProtoProductStatus(req.GetStatus()),
			}

			// Read the prior state inside the transaction so the audit entry's
			// before and after describe the same committed change.
			before, err := s.repo.GetProductByPublicID(ctx, req.GetId())
			if err != nil {
				return nil, err
			}
			if err := s.repo.UpdateProduct(ctx, updated); err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action:       "product.updated",
				ResourceType: "product",
				ResourceID:   updated.PublicID,
				ProductID:    &updated.ID,
				Before:       before,
				After:        updated,
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.UpdateProductResponse{Product: toProtoProduct(updated)}, nil
		})
}

func toProtoProduct(p *dao.Product) *openpay_v1.Product {
	return &openpay_v1.Product{
		Id:              p.PublicID,
		Code:            p.Code,
		Name:            p.Name,
		Status:          toProtoProductStatus(p.Status),
		DefaultCurrency: p.DefaultCurrency,
		CreatedAt:       timestamppb.New(p.CreatedAt),
		UpdatedAt:       timestamppb.New(p.UpdatedAt),
	}
}

func toProtoProductStatus(s dao.ProductStatus) openpay_v1.ProductStatus {
	switch s {
	case dao.ProductActive:
		return openpay_v1.ProductStatus_PRODUCT_STATUS_ACTIVE
	case dao.ProductSuspended:
		return openpay_v1.ProductStatus_PRODUCT_STATUS_SUSPENDED
	default:
		return openpay_v1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func fromProtoProductStatus(s openpay_v1.ProductStatus) dao.ProductStatus {
	switch s {
	case openpay_v1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return dao.ProductActive
	case openpay_v1.ProductStatus_PRODUCT_STATUS_SUSPENDED:
		return dao.ProductSuspended
	default:
		// Unspecified means "no filter" on a listing. Writes reject it through
		// the proto's not_in: [0] rule before reaching here.
		return ""
	}
}

type auditParams struct {
	Action       string
	ResourceType string
	ResourceID   string
	ProductID    *int64
	Before       any
	After        any
}

// audit records a change. It must run inside the transaction that makes the
// change, so the two commit together.
func (s *Service) audit(ctx context.Context, p auditParams) error {
	caller, _ := appcontext.CallerFrom(ctx)

	actorType, actorID := dao.ActorSystem, "system"
	switch {
	case caller.IsOperator():
		actorType, actorID = dao.ActorOperator, caller.UserID
	case caller.IsService():
		actorType, actorID = dao.ActorService, caller.CredentialID
	}

	before, err := marshalAuditState(p.Before)
	if err != nil {
		return err
	}
	after, err := marshalAuditState(p.After)
	if err != nil {
		return err
	}

	return s.repo.RecordAudit(ctx, &dao.AuditEntry{
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       p.Action,
		ResourceType: p.ResourceType,
		ResourceID:   p.ResourceID,
		ProductID:    p.ProductID,
		RequestID:    caller.RequestID,
		Before:       before,
		After:        after,
	})
}

func marshalAuditState(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to encode audit state")
	}
	return b, nil
}
