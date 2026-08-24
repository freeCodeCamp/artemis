package observability

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	classCtxCanceled   = "ctx.canceled"
	classCtxDeadline   = "ctx.deadline"
	classGRPCCanceled  = "grpc.canceled"
	classGRPCDeadline  = "grpc.deadline"
	classPGInRecovery  = "pg.in_recovery"
	classPGLockTimeout = "pg.lock_timeout"
	classPGConnClosed  = "pg.conn_closed"
	classUnexpectedEOF = "io.unexpected_eof"
	classDNSTemporary  = "net.dns_temporary"
	classDNSPermanent  = "net.dns"
	classUnclassified  = "unclassified"
)

var transientClasses = map[string]bool{
	classCtxCanceled:   true,
	classCtxDeadline:   true,
	classGRPCCanceled:  true,
	classGRPCDeadline:  true,
	classPGInRecovery:  true,
	classPGLockTimeout: true,
	classPGConnClosed:  true,
	classUnexpectedEOF: true,
	classDNSTemporary:  true,
}

var shutdownClasses = map[string]bool{
	classCtxCanceled:  true,
	classGRPCCanceled: true,
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return classCtxCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classCtxDeadline
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		switch st.Code() {
		case codes.Canceled:
			return classGRPCCanceled
		case codes.DeadlineExceeded:
			return classGRPCDeadline
		default:
			return "grpc." + st.Code().String()
		}
	}
	if pg.IsInRecovery(err) {
		return classPGInRecovery
	}
	if pg.IsLockTimeout(err) {
		return classPGLockTimeout
	}
	if pg.IsConnClosed(err) {
		return classPGConnClosed
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return classUnexpectedEOF
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.Temporary() {
			return classDNSTemporary
		}
		return classDNSPermanent
	}
	if code, ok := pg.SQLState(err); ok {
		return "pg." + code
	}
	return classUnclassified
}

func IsTransient(err error) bool { return transientClasses[errorClass(err)] }
