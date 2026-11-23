package push

// Mock the standard RoundTripper for tests that verify response-body ownership
// and cancellation without replacing the actual HTTP client implementation.
//go:generate go run go.uber.org/mock/mockgen -destination=mocks_test.go -package=push net/http RoundTripper
