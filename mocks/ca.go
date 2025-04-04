package mocks

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	capb "github.com/letsencrypt/boulder/ca/proto"
	corepb "github.com/letsencrypt/boulder/core/proto"
)

// MockCA is a mock of a CA that always returns the cert from PEM in response to
// IssueCertificate.
type MockCA struct {
	PEM []byte
}

// IssueCertificate is a mock
func (ca *MockCA) IssueCertificate(ctx context.Context, req *capb.IssueCertificateRequest, _ ...grpc.CallOption) (*capb.IssueCertificateResponse, error) {
	precert, err := ca.issuePrecertificate(ctx, req)
	if err != nil {
		return nil, err
	}
	cert, err := ca.issueCertificateForPrecertificate(ctx, &capb.IssueCertificateForPrecertificateRequest{
		DER:             precert.DER,
		SCTs:            nil,
		RegistrationID:  req.RegistrationID,
		OrderID:         req.OrderID,
		CertProfileHash: precert.CertProfileHash,
	})
	if err != nil {
		return nil, err
	}
	return &capb.IssueCertificateResponse{DER: cert.Der}, nil
}

// issuePrecertificate is a mock
func (ca *MockCA) issuePrecertificate(_ context.Context, req *capb.IssueCertificateRequest, _ ...grpc.CallOption) (*capb.IssuePrecertificateResponse, error) {
	if ca.PEM == nil {
		return nil, fmt.Errorf("MockCA's PEM field must be set before calling IssueCertificate")
	}
	block, _ := pem.Decode(ca.PEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	profHash := sha256.Sum256([]byte(req.CertProfileName))
	return &capb.IssuePrecertificateResponse{
		DER:             cert.Raw,
		CertProfileHash: profHash[:8],
		CertProfileName: req.CertProfileName,
	}, nil
}

// issueCertificateForPrecertificate is a mock
func (ca *MockCA) issueCertificateForPrecertificate(_ context.Context, req *capb.IssueCertificateForPrecertificateRequest, _ ...grpc.CallOption) (*corepb.Certificate, error) { //nolint:unparam // `error` is always nil
	now := time.Now()
	expires := now.Add(1 * time.Hour)

	return &corepb.Certificate{
		Der:            req.DER,
		RegistrationID: 1,
		Serial:         "mock",
		Digest:         "mock",
		Issued:         timestamppb.New(now),
		Expires:        timestamppb.New(expires),
	}, nil
}

type MockOCSPGenerator struct{}

// GenerateOCSP is a mock
func (ca *MockOCSPGenerator) GenerateOCSP(ctx context.Context, req *capb.GenerateOCSPRequest, _ ...grpc.CallOption) (*capb.OCSPResponse, error) {
	return nil, nil
}

type MockCRLGenerator struct{}

// GenerateCRL is a mock
func (ca *MockCRLGenerator) GenerateCRL(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[capb.GenerateCRLRequest, capb.GenerateCRLResponse], error) {
	return nil, nil
}
