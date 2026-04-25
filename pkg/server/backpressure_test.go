package server_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/nobelk/reverb/pkg/auth"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/limiter"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	pb "github.com/nobelk/reverb/pkg/server/proto"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// newReverbClient constructs a minimal in-memory Reverb client for the
// backpressure tests. Distinct from setupTestServer in http_test.go because
// these tests need direct control over which middleware is wired.
func newReverbClient(t *testing.T) *reverb.Client {
	t.Helper()
	cfg := reverb.Config{
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	c, err := reverb.New(cfg, fake.New(8), memory.New(), flat.New(0))
	require.NoError(t, err)
	return c
}

// TestHTTP_RateLimit_Returns429 verifies that once a tenant exceeds its
// token-bucket budget, subsequent requests are rejected with 429 and a
// Retry-After header — and that health/metrics paths remain unaffected.
func TestHTTP_RateLimit_Returns429(t *testing.T) {
	client := newReverbClient(t)
	// 1 token/sec, burst of 2 → first 2 succeed, 3rd is throttled.
	reg := limiter.NewRegistry(1, 2, nil)
	srv := server.NewHTTPServer(client, ":0", nil, server.WithRateLimiter(reg))

	body := map[string]any{"namespace": "ns", "prompt": "hi", "model_id": "m"}

	for i := range 2 {
		rec := postJSON(t, srv, "/v1/lookup", body)
		require.NotEqual(t, http.StatusTooManyRequests, rec.Code,
			"burst slot %d should not be throttled (got %d)", i, rec.Code)
	}

	rec := postJSON(t, srv, "/v1/lookup", body)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	retry := rec.Header().Get("Retry-After")
	require.NotEmpty(t, retry, "Retry-After header must be set on 429")
	secs, err := strconv.Atoi(retry)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, secs, 1, "Retry-After must be at least 1 second")

	// Health endpoints are exempt.
	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	srv.ServeHTTP(healthRec, healthReq)
	assert.Equal(t, http.StatusOK, healthRec.Code, "/healthz must bypass rate limit")
}

// TestHTTP_RateLimit_PerTenantIsolation confirms that one noisy tenant cannot
// starve another tenant of their own bucket allowance.
func TestHTTP_RateLimit_PerTenantIsolation(t *testing.T) {
	client := newReverbClient(t)
	reg := limiter.NewRegistry(1, 1, nil)
	authn, err := auth.NewAuthenticator(reverb.AuthConfig{
		Enabled: true,
		Tenants: []reverb.Tenant{
			{ID: "tenant-a", APIKeys: []string{"key-a"}},
			{ID: "tenant-b", APIKeys: []string{"key-b"}},
		},
	})
	require.NoError(t, err)
	srv := server.NewHTTPServer(client, ":0", authn, server.WithRateLimiter(reg))

	body := []byte(`{"namespace":"ns","prompt":"hi","model_id":"m"}`)

	doRequest := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/lookup", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	// Tenant A burns its single token, then is throttled.
	require.NotEqual(t, http.StatusTooManyRequests, doRequest("key-a"))
	require.Equal(t, http.StatusTooManyRequests, doRequest("key-a"))

	// Tenant B is on a separate bucket and must still succeed.
	assert.NotEqual(t, http.StatusTooManyRequests, doRequest("key-b"),
		"tenant-b should be unaffected by tenant-a's exhaustion")
}

// TestGRPC_RateLimit_ResourceExhausted verifies that an over-rate gRPC call
// returns codes.ResourceExhausted (the standard gRPC equivalent of HTTP 429).
// Routes through bufconn so the unary interceptor chain actually fires —
// calling the server methods directly bypasses the interceptors entirely.
func TestGRPC_RateLimit_ResourceExhausted(t *testing.T) {
	client := newReverbClient(t)
	reg := limiter.NewRegistry(1, 1, nil)
	grpcSrv := server.NewGRPCServer(client, nil, server.WithGRPCRateLimiter(reg))

	lis := bufconn.Listen(1 << 20)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	c := pb.NewReverbServiceClient(conn)
	req := &pb.LookupRequest{Namespace: "ns", Prompt: "hi", ModelId: "m"}

	// First call within budget.
	_, err = c.Lookup(context.Background(), req)
	require.NoError(t, err)

	// Second call exhausts the bucket → ResourceExhausted.
	_, err = c.Lookup(context.Background(), req)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "expected a gRPC status error, got %T", err)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}
