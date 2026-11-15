package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

func CandidateSubnets(local, override string) []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range strings.Split(override, ",") {
		p, e := netip.ParsePrefix(strings.TrimSpace(s))
		if e == nil && p.Addr().Is4() {
			out = append(out, p.Masked())
		}
	}
	if len(out) > 0 {
		return out
	}
	a, e := netip.ParseAddr(local)
	if e == nil && a.Is4() {
		out = append(out, netip.PrefixFrom(a, 24).Masked())
	}
	return out
}

type Host struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
	Port int    `json:"port"`
}
type Discovery struct {
	LocalIP, Subnets string
	Probe            func(context.Context, string) bool
	Lookup           func(context.Context, string) string
}

func LocalIPv4() string {
	c, e := net.Dial("udp4", "8.8.8.8:53")
	if e != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}
func (d Discovery) Discover(ctx context.Context, maxResults int) []Host {
	nets := CandidateSubnets(d.LocalIP, d.Subnets)
	candidates := []string{}
	for _, p := range nets {
		for a := p.Addr(); p.Contains(a) && len(candidates) < 4096; a = a.Next() { // /31 and /32 have no reserved endpoints.
			if p.Bits() < 31 && (a == p.Addr() || !p.Contains(a.Next())) {
				continue
			}
			if a.String() != d.LocalIP {
				candidates = append(candidates, a.String())
			}
		}
		if len(candidates) >= 4096 {
			break
		}
	}
	probe := d.Probe
	if probe == nil {
		probe = func(ctx context.Context, ip string) bool {
			ctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
			defer cancel()
			c, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "445"))
			if e != nil {
				return false
			}
			c.Close()
			return true
		}
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, ip string) string {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			names, e := net.DefaultResolver.LookupAddr(ctx, ip)
			if e != nil || len(names) == 0 {
				return ""
			}
			return strings.SplitN(names[0], ".", 2)[0]
		}
	}
	responsive := make([]bool, len(candidates))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() == nil {
					responsive[i] = probe(ctx, candidates[i])
				}
			}
		})
	}
	for i := range candidates {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	out := []Host{}
	if maxResults <= 0 {
		maxResults = 32
	}
	for i, ok := range responsive {
		if ok {
			ip := candidates[i]
			out = append(out, Host{ip, ip, 445})
			if len(out) >= maxResults {
				break
			}
		}
	}
	names := make(chan int)
	for range min(16, len(out)) {
		wg.Go(func() {
			for i := range names {
				if name := lookup(ctx, out[i].IP); name != "" {
					out[i].Name = name
				}
			}
		})
	}
	for i := range out {
		names <- i
	}
	close(names)
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		namedI, namedJ := out[i].IP != out[i].Name, out[j].IP != out[j].Name
		if namedI != namedJ {
			return namedI
		}
		return netip.MustParseAddr(out[i].IP).Less(netip.MustParseAddr(out[j].IP))
	})
	return out
}
func ParseShares(output string) []string {
	out := []string{}
	hidden := map[string]bool{"IPC$": true, "ADMIN$": true, "print$": true, "NETLOGON": true, "SYSVOL": true}
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !hidden[s] && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "disk") {
			add(strings.TrimSpace(parts[1]))
		}
	}
	if len(out) == 0 {
		for _, line := range strings.Split(output, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && strings.EqualFold(parts[1], "disk") {
				add(parts[0])
			}
		}
	}
	return out
}

type OutputRunner func(context.Context, string, []string) (string, string, error)

func outputRun(ctx context.Context, name string, args []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	e := cmd.Run()
	return stdout.String(), stderr.String(), e
}
func ListShares(ctx context.Context, host, user, password, domain string, invoke OutputRunner) ([]string, error) {
	if !safeHost.MatchString(host) || user == "" || strings.ContainsAny(user+password+domain, "\r\n") {
		return nil, errors.New("host, username, password are required")
	}
	if domain == "" {
		domain = "WORKGROUP"
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"-L", "//" + host}
	if password != "" {
		f, e := secretFile("jackery-smbauth-", "username = "+user+"\npassword = "+password+"\ndomain = "+domain+"\n")
		if e != nil {
			return nil, e
		}
		defer os.Remove(f)
		args = append(args, "-A", f, "-g")
	} else {
		args = append(args, "-U", domain+"/"+user, "-g", "-N")
	}
	if invoke == nil {
		invoke = outputRun
	}
	stdout, stderr, e := invoke(ctx, "smbclient", args)
	if e == nil {
		return ParseShares(stdout), nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, errors.New("smbclient timed out after 8s")
	}
	if errors.Is(e, exec.ErrNotFound) || errors.Is(e, os.ErrNotExist) {
		return nil, errors.New("smbclient not installed in container")
	}
	lines := strings.Split(strings.TrimSpace(stderr+"\n"+stdout), "\n")
	chosen := ""
	for _, line := range lines {
		l := strings.ToLower(line)
		if strings.Contains(line, "NT_STATUS_") || strings.Contains(l, "session setup failed") || (strings.Contains(l, "connection to") && strings.Contains(l, "failed")) {
			chosen = strings.TrimSpace(line)
			break
		}
	}
	if chosen == "" && len(lines) > 0 {
		chosen = strings.TrimSpace(lines[0])
	}
	if chosen == "" {
		chosen = fmt.Sprintf("smbclient failed: %v", e)
	}
	return nil, errors.New(chosen)
}
