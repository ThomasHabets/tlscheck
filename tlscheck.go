package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

var (
	concurrency = flag.Int64("concurrency", 100, "Connection concurrency.")
	timeout     = flag.Duration("timeout", 10*time.Second, "Connection timeout.")
	warnTime    = flag.Duration("warn_time", 7*24*time.Hour, "Warn if expiring soon.")
	ipv6        = flag.Bool("ipv6", true, "Connect to IPv6 targets too.")
)

func parse(host string) (string, string, error) {
	var connectHost, tlsHost string
	t := strings.Split(host, "/")
	if len(t) == 2 {
		tlsHost, connectHost = t[0], t[1]
	} else {
		connectHost = host
		tlsHost = host
	}

	var err error
	tlsHost, _, err = net.SplitHostPort(tlsHost)
	if err != nil {
		return "", "", err
	}
	return connectHost, tlsHost, nil
}

func check(ctx context.Context, host string) []error {
	connectHost, tlsHost, err := parse(host)
	if err != nil {
		return []error{err}
	}

	// Dial.
	h, connectPort, err := net.SplitHostPort(connectHost)
	if err != nil {
		return []error{err}
	}
	allEndpoints, err := net.LookupHost(h)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, endpoint := range allEndpoints {
		// If IPv6 and IPv6 turned off, skip.
		if !*ipv6 && strings.Contains(endpoint, ":") {
			log.Debugf("%q Skipping IPv6 address %q", host, endpoint)
			continue
		}

		// Set timeout per connection.
		ctx2, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()

		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx2, "tcp", net.JoinHostPort(endpoint, connectPort))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		defer conn.Close()

		// TLS handshake.
		c := tls.Client(conn, &tls.Config{
			ServerName: tlsHost,
		})
		if err := c.Handshake(); err != nil {
			return []error{fmt.Errorf("handshake error for %q: %v", tlsHost, err)}
		}
		defer c.Close()

		// Check connection state.
		s := c.ConnectionState()
		for _, cert := range s.PeerCertificates {
			if time.Now().After(cert.NotAfter) {
				errs = append(errs, fmt.Errorf("%q: Expired cert %q", host, cert.Subject.CommonName))
				continue
			}
			remaining := cert.NotAfter.Sub(time.Now())
			if remaining < *warnTime {
				log.Warningf("%q: Remaining time on %q: %v", host, cert.Subject.CommonName, remaining)
			}
		}
	}
	return errs
}

func main() {
	flag.Parse()
	ctx := context.Background()
	sem := semaphore.NewWeighted(*concurrency)

	// TODO: Do concurrent lookups here, then call concurrent checks.
	for _, host := range flag.Args() {
		host := host
		sem.Acquire(ctx, 1)
		go func() {
			defer sem.Release(1)
			if host[0] == '#' {
				return
			}

			if errs := check(ctx, host); len(errs) > 0 {
				for _, err := range errs {
					log.Errorf("%q: %v", host, err)
				}
			} else {
				log.Infof("%q: OK", host)
			}
		}()
	}
	sem.Acquire(ctx, *concurrency)
}
