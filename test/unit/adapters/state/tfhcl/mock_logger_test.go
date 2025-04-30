package tfhcl_test

import (
	"context"

	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/stretchr/testify/mock"
)

// MockLogger is a mock implementation of ports.Logger
type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) Debugf(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Infof(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Warnf(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Errorf(ctx context.Context, err error, format string, args ...any) {
	m.Called(ctx, err, format, args)
}

func (m *MockLogger) WithFields(fields map[string]any) ports.Logger {
	args := m.Called(fields)
	return args.Get(0).(ports.Logger)
}

// NewTestLogger creates a new MockLogger with basic mocking set up
func NewTestLogger() *MockLogger {
	mockLogger := new(MockLogger)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
	mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything).Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	return mockLogger
}
