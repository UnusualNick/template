package httpapi

// Position handlers accept package-private interfaces defined in positions/contract.go.
// Generate local mocks from those actual contracts to test router wiring without
// introducing exported interfaces or duplicate test-only contract declarations.
//go:generate go run go.uber.org/mock/mockgen -source=positions/contract.go -destination=position_mocks_test.go -package=httpapi -mock_names=saveUsecase=MockPositionSaveUsecase,listUsecase=MockPositionListUsecase
