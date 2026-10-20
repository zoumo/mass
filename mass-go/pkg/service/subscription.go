package service

import "github.com/zoumo/mass/mass-go/pkg/events"

// Subscription observes future events only; cancellation has no Agent side effect.
type Subscription = events.Subscription
