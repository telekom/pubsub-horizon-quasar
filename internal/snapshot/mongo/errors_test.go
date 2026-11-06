// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestIsResumeTokenErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "InvalidResumeToken", err: mongo.CommandError{Code: errCodeInvalidResumeToken}, want: true},
		{name: "ChangeStreamFatalError", err: mongo.CommandError{Code: errCodeChangeStreamFatal}, want: true},
		{name: "ChangeStreamHistoryLost", err: mongo.CommandError{Code: errCodeChangeStreamHistoryLost}, want: true},
		{name: "Wrapped", err: fmt.Errorf("watch: %w", mongo.CommandError{Code: errCodeChangeStreamHistoryLost}), want: true},
		{name: "OtherServerError", err: mongo.CommandError{Code: 40647}, want: false},
		{name: "NonServerError", err: errors.New("boom"), want: false},
		{name: "Nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isResumeTokenErr(tt.err))
		})
	}
}

func TestIsIndexExistsErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "IndexOptionsConflict", err: mongo.CommandError{Code: errCodeIndexOptionsConflict}, want: true},
		{name: "IndexKeySpecsConflict", err: mongo.CommandError{Code: errCodeIndexKeySpecsConflict}, want: true},
		{name: "Wrapped", err: fmt.Errorf("create index: %w", mongo.CommandError{Code: errCodeIndexKeySpecsConflict}), want: true},
		{name: "OtherServerError", err: mongo.CommandError{Code: 40647}, want: false},
		{name: "NonServerError", err: errors.New("boom"), want: false},
		{name: "Nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isIndexExistsErr(tt.err))
		})
	}
}
