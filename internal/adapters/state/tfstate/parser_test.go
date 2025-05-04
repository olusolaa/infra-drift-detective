// internal/adapters/state/tfstate/parser_test.go
package tfstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func loadRawState(t *testing.T, filePath string) *State {
	t.Helper()
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	var st State
	require.NoError(t, json.Unmarshal(data, &st))
	return &st
}

// ----------------------------------------------------------------------------
// parseAndCache
// ----------------------------------------------------------------------------
func TestStateParser_ParseAndCache(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Maybe().Return(mockLogger)
	mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()
	mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

	ctx := context.Background()

	t.Run("success and cache", func(t *testing.T) {
		fp := filepath.Join("testdata", "sample_ec2_raw.tfstate")
		p := newStateParser(fp, mockLogger)

		st, err := p.parseAndCache(ctx)
		require.NoError(t, err)
		require.NotNil(t, st)
		assert.Equal(t, 5, st.Version)

		// second call returns the same pointer (cached)
		st2, err2 := p.parseAndCache(ctx)
		require.NoError(t, err2)
		assert.Same(t, st, st2)
	})

	t.Run("file not found", func(t *testing.T) {
		fp := filepath.Join("testdata", "does-not-exist.tfstate")
		p := newStateParser(fp, mockLogger)

		st, err := p.parseAndCache(ctx)
		require.Error(t, err)
		assert.Nil(t, st)

		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)

		// cached error
		_, err2 := p.parseAndCache(ctx)
		assert.ErrorIs(t, err2, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		fp := filepath.Join("testdata", "invalid_json.tfstate")
		p := newStateParser(fp, mockLogger)

		st, err := p.parseAndCache(ctx)
		require.Error(t, err)
		assert.Nil(t, st)

		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
	})

	t.Run("empty file", func(t *testing.T) {
		fp := filepath.Join("testdata", "empty.tfstate")
		p := newStateParser(fp, mockLogger)

		st, err := p.parseAndCache(ctx)
		require.Error(t, err)
		assert.Nil(t, st)

		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
	})

	t.Run("unsupported version", func(t *testing.T) {
		fp := filepath.Join("testdata", "version4.tfstate") // same layout but "version":4
		p := newStateParser(fp, mockLogger)

		_, err := p.parseAndCache(ctx)
		require.Error(t, err)

		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeUnsupportedStateVersion, appErr.Code)
	})

	t.Run("context cancelled", func(t *testing.T) {
		fp := filepath.Join("testdata", "sample_ec2_raw.tfstate")
		p := newStateParser(fp, mockLogger)

		ctxCancel, cancel := context.WithCancel(ctx)
		cancel()

		st, err := p.parseAndCache(ctxCancel)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, st)
	})

	t.Run("concurrency safety", func(t *testing.T) {
		fp := filepath.Join("testdata", "sample_ec2_raw.tfstate")
		p := newStateParser(fp, mockLogger)

		var wg sync.WaitGroup
		n := 50
		wg.Add(n)

		var firstSt *State
		var firstErr error
		var once sync.Once

		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				st, err := p.parseAndCache(ctx)
				once.Do(func() {
					firstSt, firstErr = st, err
				})
				time.Sleep(1 * time.Millisecond)
				assert.Same(t, firstSt, st)
				assert.Equal(t, firstErr, err)
			}()
		}
		wg.Wait()
		require.NoError(t, firstErr)
		require.NotNil(t, firstSt)
	})
}

// ----------------------------------------------------------------------------
// findResourcesInState
// ----------------------------------------------------------------------------
func TestFindResourcesInState(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

	st := loadRawState(t, filepath.Join("testdata", "nested_modules_raw.tfstate"))

	t.Run("existing kind", func(t *testing.T) {
		res, err := findResourcesInState(st, domain.KindComputeInstance, mockLogger)
		require.NoError(t, err)
		require.Len(t, res, 2)

		addr := func(r *Resource) string {
			if r.Module != "" {
				return r.Module + "." + r.Type + "." + r.Name
			}
			return r.Type + "." + r.Name
		}

		assert.Equal(t, "aws_instance.root_ec2", addr(res[0]))
		assert.Equal(t, "module.nested.aws_instance.child_ec2", addr(res[1]))
	})

	t.Run("non‑existing kind", func(t *testing.T) {
		res, err := findResourcesInState(st, domain.KindStorageBucket, mockLogger)
		require.NoError(t, err)
		assert.Empty(t, res)
	})

	t.Run("nil state", func(t *testing.T) {
		res, err := findResourcesInState(nil, domain.KindComputeInstance, mockLogger)
		require.NoError(t, err)
		assert.Nil(t, res)
	})
}

// ----------------------------------------------------------------------------
// findSpecificResource
// ----------------------------------------------------------------------------
func TestFindSpecificResource(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

	st := loadRawState(t, filepath.Join("testdata", "nested_modules_raw.tfstate"))

	t.Run("root resource", func(t *testing.T) {
		r, err := findSpecificResource(st, domain.KindComputeInstance, "aws_instance.root_ec2", mockLogger)
		require.NoError(t, err)
		require.NotNil(t, r)
		assert.Equal(t, "", r.Module)
		assert.Equal(t, "aws_instance", r.Type)
		assert.Equal(t, "root_ec2", r.Name)
	})

	t.Run("nested resource", func(t *testing.T) {
		r, err := findSpecificResource(st, domain.KindComputeInstance, "module.nested.aws_instance.child_ec2", mockLogger)
		require.NoError(t, err)
		require.NotNil(t, r)
		assert.Equal(t, "module.nested", r.Module)
		assert.Equal(t, "child_ec2", r.Name)
	})

	t.Run("identifier not found", func(t *testing.T) {
		r, err := findSpecificResource(st, domain.KindComputeInstance, "aws_instance.nope", mockLogger)
		require.Error(t, err)
		assert.Nil(t, r)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("found but wrong kind", func(t *testing.T) {
		r, err := findSpecificResource(st, domain.KindStorageBucket, "aws_instance.root_ec2", mockLogger)
		require.Error(t, err)
		assert.Nil(t, r)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("nil state", func(t *testing.T) {
		_, err := findSpecificResource(nil, domain.KindComputeInstance, "anything", mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state is nil")
	})
}
