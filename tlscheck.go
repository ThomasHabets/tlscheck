package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"time"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

var (
	concurrency = flag.Int64("concurrency", 100, "Connection concurrency.")
	timeout     = flag.Duration("timeout", 2*time.Second, "Connection timeout.")
	warnTime     = flag.Duration("warn_time", 7*24*time.Hour, "Warn if expiring soon.")
)

func check(ctx context.Context, host string) error {
	var connectHost, tlsHost string
	{
		t := strings.Split(host, "/")
		if len(t) == 2 {
			tlsHost, connectHost = t[0],t[1]
		} else {
			connectHost = host
			tlsHost = host
		}
	}
	
	
	ctx2, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx2, "tcp", connectHost)
	if err != nil {
		return err
	}
	defer conn.Close()

	h, _, err := net.SplitHostPort(tlsHost)
	if err != nil {
		return err
	}
	c := tls.Client(conn, &tls.Config{
		ServerName: h,
	})
	if err := c.Handshake(); err != nil {
		return fmt.Errorf("handshake error for %q: %v", h, err)
	}
	defer c.Close()

	s := c.ConnectionState()
	for _, cert := range s.PeerCertificates {
		if time.Now().After(cert.NotAfter) {
			return fmt.Errorf("%q: Expired cert %q", host, cert.Subject.CommonName)
		}
		remaining := cert.NotAfter.Sub(time.Now())
		if remaining < *warnTime {
			log.Warningf("%q: Remaining time on %q: %v", host, cert.Subject.CommonName, remaining)
		}
	}
	return nil
}

func main() {
	flag.Parse()
	ctx := context.Background()
	sem := semaphore.NewWeighted(*concurrency)
	for _, host := range flag.Args() {
		host := host
		sem.Acquire(ctx, 1)
		go func() {
			defer sem.Release(1)
			if host[0] == '#' {
				return
			}

			if err := check(ctx, host); err != nil {
				log.Errorf("%q: %v", host, err)
			} else {
				log.Infof("%q: OK", host)
			}
		}()
	}
	sem.Acquire(ctx, *concurrency)
}
