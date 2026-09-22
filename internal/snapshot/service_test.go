// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

const snapshotTestURIEnv = "QUASAR_TEST_SNAPSHOT_URI"

func TestSnapshotConnectionProcess(t *testing.T) {
	uri := os.Getenv(snapshotTestURIEnv)
	if uri == "" {
		t.Skip("only executed as a subprocess")
	}
	if strings.HasPrefix(uri, "mongodb+srv://") {
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("probe-password DNS failure")
			},
		}
	}
	log.Logger = zerolog.New(os.Stdout)
	c := testConfig()
	c.URI = uri
	c.OperationTimeout = 2 * time.Second
	require.NoError(t, c.Validate())
	s := newService(c)
	defer s.cancel()
	defer s.disconnect()
	ctx, cancel := context.WithTimeout(t.Context(), c.OperationTimeout)
	defer cancel()
	require.NoError(t, s.initialize(ctx))
}

func requireSnapshotConnectionFatal(t *testing.T, uri, operation string, sensitive ...string) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestSnapshotConnectionProcess$")
	command.Env = append(os.Environ(), snapshotTestURIEnv+"="+uri)
	output, err := command.CombinedOutput()
	require.NoError(t, ctx.Err(), "the child must exit itself, not be killed by the test timeout: %s", output)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError, string(output))
	require.Equal(t, 1, exitError.ExitCode(), string(output))
	text := string(output)
	require.Contains(t, text, `"level":"fatal"`)
	require.Contains(t, text, `"message":"Subscription snapshot connection failed"`)
	require.Contains(t, text, operation+" dedicated subscription snapshot client failed")
	for _, value := range append([]string{uri, "mongodb://", "mongodb+srv://"}, sensitive...) {
		require.NotContains(t, text, value)
	}
	return text
}

func TestInitialSnapshotConnectionFailures(t *testing.T) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	tests := []struct {
		name      string
		uri       string
		operation string
	}{
		{"scheme", "https://probe-user:probe-password@example.invalid", "connect"},
		{"option", "mongodb://probe-user:probe-password@localhost/?connectTimeoutMS=probe-password", "connect"},
		{"credential escape", "mongodb://probe-user:probe%zz-password@localhost", "connect"},
		{"SRV port", "mongodb+srv://probe-user:probe-password@example.invalid:27017", "connect"},
		{
			"SRV discovery",
			"mongodb+srv://probe-user:probe-password@example.invalid/?srvServiceName=custom&authSource=private-auth",
			"connect",
		},
		{"ping timeout", "mongodb://probe-user:probe-password@" + listener.Addr().String(), "ping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireSnapshotConnectionFatal(t, tt.uri, tt.operation, "probe-user", "probe-password", "probe%zz-password", "private-auth")
		})
	}
}
