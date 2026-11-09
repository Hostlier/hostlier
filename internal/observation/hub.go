package observation

import "hostlier/internal/snapshot"

// Bus is the latest-state contract shared with lightweight consumers.
type Bus = snapshot.Bus

// Hub is the in-memory implementation used when durable delivery is unnecessary.
type Hub = snapshot.Hub

// New constructs an in-process observation bus.
func New() *Hub { return snapshot.New() }
