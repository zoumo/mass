//go:build !coretest

package core

import (
	"net/http"
	"time"
)

func commitGate(_ string) {}

func (*Server) testDispatch(http.ResponseWriter, *http.Request) bool { return false }

func outboxNow() time.Time { return time.Now().UTC() }

func scheduledTaskNow() time.Time { return time.Now().UTC() }
