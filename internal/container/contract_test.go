package container

// Generate package-local mocks directly from the worker's actual interfaces
// so shutdown tests exercise wiring without duplicating the contracts here.
//go:generate go run go.uber.org/mock/mockgen -source=../worker/positions/worker.go -destination=mocks_test.go -package=container -mock_names=StaleTrips=MockStaleTrips,PositionRequester=MockPositionRequester
