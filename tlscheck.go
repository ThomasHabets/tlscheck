// Copyright 2018 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"os"
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
	debug       = flag.Bool("debug", false, "Debug.")
	endpoints   = flag.String("endpoints", "/dev/stdin", "File with list of endpoints.")
)

// take arg line and return what to connect to, and the TLS hostname.
// E.g.:
//   foo/bar:443 -> bar:443, foo, nil
//   foo:443 -> foo:443, foo, nil
func parse(host string) (string, string, error) {
	t := strings.Split(host, "/")
	if len(t) == 2 {
		return t[1], t[0], nil
	}

	tlsHost, _, err := net.SplitHostPort(host)
	if err != nil {
		return "", "", err
	}
	return host, tlsHost, nil
}

// return true for [2001:db8::1]:1234.
func ipv6Endpoint(endpoint string) bool {
	h, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		log.Fatalf("Internal error: can't split %q: %v", endpoint, err)
	}
	return strings.Contains(h, ":")
}

// Try to connect to endpoint (host:port), and negotiate TLS with host `tlsHost`.
func check(ctx context.Context, endpoint, tlsHost string) error {
	// If IPv6 and IPv6 turned off, skip.
	if !*ipv6 && ipv6Endpoint(endpoint) {
		log.Debugf("Skipping IPv6 address %q", endpoint)
		return nil
	}
	log.Debugf("Checking endpoint %q host %q", endpoint, tlsHost)

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()

	// TLS handshake.
	c := tls.Client(conn, &tls.Config{
		ServerName: tlsHost,
	})
	if err := c.Handshake(); err != nil {
		return fmt.Errorf("handshake error for %q: %v", tlsHost, err)
	}
	defer c.Close()

	// Check connection state.
	for _, cert := range c.ConnectionState().PeerCertificates {
		if time.Now().After(cert.NotAfter) {
			return fmt.Errorf("Expired cert %q", cert.Subject.CommonName)
		}
		remaining := durTrunc(cert.NotAfter.Sub(time.Now()), time.Hour)
		if remaining < *warnTime {
			log.Warningf("Remaining time on %q endpoint %q: %v (%v)", cert.Subject.CommonName, endpoint, durFormat(remaining), cert.NotAfter)
		}
	}
	return nil
}

// time.Duration.Truncate() doesn't seem to be in Go 1.8.
func durTrunc(d time.Duration, t time.Duration) time.Duration {
	return d - (d % t)
}

func durFormat(d time.Duration) string {
	ret := ""
	if d > 24*time.Hour {
		ret += fmt.Sprintf("%dd ", d/(24*time.Hour))
		d %= 24 * time.Hour
	}
	ret += fmt.Sprintf("%dh", d/time.Hour)
	return ret
}

func getEndpoints() []string {
	b, err := ioutil.ReadFile(*endpoints)
	if err != nil {
		log.Fatalf("Failed to read %q: %v", *endpoints, err)
	}
	var ret []string
	for _, line := range strings.Split(string(b), "\n") {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		ret = append(ret, line)
	}
	return ret
}

func main() {
	flag.Parse()
	ctx := context.Background()
	sem := semaphore.NewWeighted(*concurrency)

	if *debug {
		log.SetLevel(log.DebugLevel)
	}

	ep := getEndpoints()
	addrs := make([][]string, len(ep), len(ep))
	ports := make([]string, len(ep), len(ep))
	tlsHosts := make([]string, len(ep), len(ep))

	log.Debugf("Resolving…")
	for n, line := range ep {
		n := n
		host, tlsHost, err := parse(line)
		if err != nil {
			log.Fatalf("Failed to parse line %q: %v", line, err)
		}
		var hostOnly string
		hostOnly, ports[n], err = net.SplitHostPort(host)
		if err != nil {
			log.Fatalf("%q is not host:port: %v", host, err)
		}
		tlsHosts[n] = tlsHost
		sem.Acquire(ctx, 1)
		go func() {
			defer sem.Release(1)
			var err error
			addrs[n], err = net.LookupHost(hostOnly)
			if err != nil {
				log.Errorf("Failed to resolve %q: %v", hostOnly, err)
			}
			log.Debugf("Resolved %q to %d", host, len(addrs[n]))
		}()
	}
	sem.Acquire(ctx, *concurrency)
	sem.Release(*concurrency)

	log.Debugf("Connecting…")
	errCh := make(chan bool, 1)
	for n, line := range flag.Args() {
		n := n
		line := line
		for _, endpointAddr := range addrs[n] {
			endpointAddr := endpointAddr
			sem.Acquire(ctx, 1)
			go func() {
				defer sem.Release(1)

				// Set timeout per connection.
				ctx2, cancel := context.WithTimeout(ctx, *timeout)
				defer cancel()

				endpoint := net.JoinHostPort(endpointAddr, ports[n])
				if err := check(ctx2, endpoint, tlsHosts[n]); err != nil {
					log.Errorf("%q: Endpoint %q: %v", line, endpoint, err)
					select {
					case errCh <- true:
					default:
					}
				}
			}()
		}
	}
	sem.Acquire(ctx, *concurrency)
	select {
	case <-errCh:
		os.Exit(1)
	default:
		os.Exit(0)
	}
}
