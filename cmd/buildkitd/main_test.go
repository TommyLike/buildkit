package main

import (
	"context"
	"testing"

	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestApplyMainFlagsProxyNetwork(t *testing.T) {
	cfg := config.Config{}
	err := runApplyMainFlags(t, []string{"--proxy-network"}, &cfg)
	require.NoError(t, err)
	require.True(t, cfg.ProxyNetwork)
}

func TestApplyMainFlagsProxyNetworkOverridesConfig(t *testing.T) {
	cfg := config.Config{ProxyNetwork: true}
	err := runApplyMainFlags(t, []string{"--proxy-network=false"}, &cfg)
	require.NoError(t, err)
	require.False(t, cfg.ProxyNetwork)
}

func TestApplyMainFlagsProxyUpstreamURL(t *testing.T) {
	cfg := config.Config{}
	err := runApplyMainFlags(t, []string{"--proxy-upstream-url=http://squid.internal:3128"}, &cfg)
	require.NoError(t, err)
	require.Equal(t, "http://squid.internal:3128", cfg.Proxy.UpstreamURL)
}

func TestApplyMainFlagsProxyUpstreamCACert(t *testing.T) {
	cfg := config.Config{}
	err := runApplyMainFlags(t, []string{"--proxy-upstream-cacert=/etc/buildkit/squid-ca.pem"}, &cfg)
	require.NoError(t, err)
	require.Equal(t, "/etc/buildkit/squid-ca.pem", cfg.Proxy.UpstreamCACert)
}

func TestApplyMainFlagsProxyUpstreamOverridesConfig(t *testing.T) {
	cfg := config.Config{Proxy: config.ProxyConfig{
		UpstreamURL:    "http://old.internal:3128",
		UpstreamCACert: "/etc/buildkit/old-ca.pem",
	}}
	err := runApplyMainFlags(t, []string{
		"--proxy-upstream-url=http://new.internal:3128",
		"--proxy-upstream-cacert=/etc/buildkit/new-ca.pem",
	}, &cfg)
	require.NoError(t, err)
	require.Equal(t, "http://new.internal:3128", cfg.Proxy.UpstreamURL)
	require.Equal(t, "/etc/buildkit/new-ca.pem", cfg.Proxy.UpstreamCACert)
}

func runApplyMainFlags(t *testing.T, args []string, cfg *config.Config) error {
	t.Helper()

	cmd := &cli.Command{
		Name: "buildkitd",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "proxy-network",
			},
			&cli.StringFlag{
				Name: "proxy-upstream-url",
			},
			&cli.StringFlag{
				Name: "proxy-upstream-cacert",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return applyMainFlags(cmd, cfg, nil)
		},
	}
	return cmd.Run(context.Background(), append([]string{"buildkitd"}, args...))
}
