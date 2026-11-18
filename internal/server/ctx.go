package server

import (
	"context"
	"time"
)

func rContext() context.Context {
	ctx, _ := context.WithTimeout(context.Background(), 15*time.Second)
	return ctx
}
