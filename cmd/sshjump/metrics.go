package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	userConnections = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sshjump_user_connection_total",
			Help: "Counting user connection",
		},
		[]string{"user"},
	)

	userTunnels = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sshjump_user_tunnel_total",
			Help: "Counting user tunnel",
		},
		[]string{"user"},
	)
)
