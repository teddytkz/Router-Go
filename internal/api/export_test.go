package api

import "router-go/internal/relay"

// SetDeployer lets tests point the Vercel/Cloudflare deployer at fake servers.
func SetDeployer(f func() *relay.Deployer) { newDeployer = f }
