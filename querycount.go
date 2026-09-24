package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
)

// Each request counts the SQL statements it runs on the host pool (AuthKit's
// included) and reports them as Server-Timing: a page whose cost grows with
// its items shows in the browser and in tests.
type queryCountKey struct{}

type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if n, ok := ctx.Value(queryCountKey{}).(*atomic.Int64); ok {
		n.Add(1)
	}
	return ctx
}

func (queryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func countQueries(c fiber.Ctx) error {
	n := new(atomic.Int64)
	c.SetContext(context.WithValue(c.Context(), queryCountKey{}, n))
	c.RequestCtx().SetUserValue(queryCountKey{}, n) // the context of adapted net/http handlers
	err := c.Next()
	c.Set("Server-Timing", fmt.Sprintf(`sql;desc="%d queries"`, n.Load()))
	return err
}
