// Code generated by mockery v2.53.3. DO NOT EDIT.

package mocks

import (
	context "context"

	mock "github.com/stretchr/testify/mock"
)

// ErrorHandler is an autogenerated mock type for the ErrorHandler type
type ErrorHandler struct {
	mock.Mock
}

// Handle provides a mock function with given fields: service, operation, err, ctx
func (_m *ErrorHandler) Handle(service string, operation string, err error, ctx context.Context) error {
	ret := _m.Called(service, operation, err, ctx)

	if len(ret) == 0 {
		panic("no return value specified for Handle")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, string, error, context.Context) error); ok {
		r0 = rf(service, operation, err, ctx)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// NewErrorHandler creates a new instance of ErrorHandler. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewErrorHandler(t interface {
	mock.TestingT
	Cleanup(func())
}) *ErrorHandler {
	mock := &ErrorHandler{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
