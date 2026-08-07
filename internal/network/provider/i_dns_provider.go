package provider

import (
	"time"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

type RateLimited struct {
	Msg        string
	RetryAfter time.Duration
}

func (e *RateLimited) Error() string {
	return e.Msg
}

type IDnsProvider interface {
	Name() string
	EnsureA(name, ip string, ttl int) (string, error)
	EnsureSrv(record model.SrvRecordData) (string, error)
	EnsureHttps(record model.HttpsRecordData) (string, error)
	Remove(recordType, name, expect string) (string, error)
}
