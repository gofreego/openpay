package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// UpsertCustomer registers a person the first time one of a product's users
// transacts. Called by a product backend, which is why it takes a service
// credential rather than an operator permission.
func (s *Service) UpsertCustomer(ctx context.Context, req *openpay_v1.UpsertCustomerRequest) (*openpay_v1.UpsertCustomerResponse, error) {
	if _, err := auth.RequireService(ctx); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	// Naturally idempotent on external_ref, so no Idempotency-Key is required:
	// demanding one for an operation that cannot duplicate anything would be
	// ceremony without a guarantee.
	customer := &dao.Customer{
		PublicID:    ids.New(ids.Customer),
		ExternalRef: req.GetExternalRef(),
	}

	var created bool
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.repo.UpsertCustomer(ctx, customer); err != nil {
			return err
		}
		if !created {
			// Nothing changed, so there is nothing to audit. Recording a
			// "created" entry on every repeat lookup would bury the real ones.
			return nil
		}
		return s.audit(ctx, auditParams{
			Action:       "customer.created",
			ResourceType: "customer",
			ResourceID:   customer.PublicID,
			After:        customer,
		})
	})
	if err != nil {
		return nil, err
	}

	return &openpay_v1.UpsertCustomerResponse{
		Customer: toProtoCustomer(customer),
		Created:  created,
	}, nil
}

// GetCustomer is readable by the product backend that serves the person, and by
// an operator handling support.
func (s *Service) GetCustomer(ctx context.Context, req *openpay_v1.GetCustomerRequest) (*openpay_v1.GetCustomerResponse, error) {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok {
		return nil, apperrors.New(apperrors.Unauthenticated, "authentication required")
	}
	if !caller.IsService() {
		if err := auth.RequireOperator(ctx, auth.PermCustomersRead); err != nil {
			return nil, err
		}
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	var (
		customer *dao.Customer
		err      error
	)
	switch identifier := req.GetIdentifier().(type) {
	case *openpay_v1.GetCustomerRequest_Id:
		customer, err = s.repo.GetCustomerByPublicID(ctx, identifier.Id)
	case *openpay_v1.GetCustomerRequest_ExternalRef:
		customer, err = s.repo.GetCustomerByExternalRef(ctx, identifier.ExternalRef)
	default:
		return nil, apperrors.New(apperrors.InvalidArgument, "an id or external_ref is required")
	}
	if err != nil {
		return nil, err
	}

	return &openpay_v1.GetCustomerResponse{Customer: toProtoCustomer(customer)}, nil
}

func toProtoCustomer(c *dao.Customer) *openpay_v1.Customer {
	return &openpay_v1.Customer{
		Id:          c.PublicID,
		ExternalRef: c.ExternalRef,
		Status:      toProtoCustomerStatus(c.Status),
		CreatedAt:   timestamppb.New(c.CreatedAt),
		UpdatedAt:   timestamppb.New(c.UpdatedAt),
	}
}

func toProtoCustomerStatus(s dao.CustomerStatus) openpay_v1.CustomerStatus {
	switch s {
	case dao.CustomerActive:
		return openpay_v1.CustomerStatus_CUSTOMER_STATUS_ACTIVE
	case dao.CustomerBlocked:
		return openpay_v1.CustomerStatus_CUSTOMER_STATUS_BLOCKED
	case dao.CustomerMerged:
		return openpay_v1.CustomerStatus_CUSTOMER_STATUS_MERGED
	default:
		return openpay_v1.CustomerStatus_CUSTOMER_STATUS_UNSPECIFIED
	}
}
