package speaker

import (
	"context"
	"net"
	"sort"
	"sync"
)

// LocalSubnets returns one /24 base address (x.y.z.0) per active, non-loopback
// IPv4 interface. Larger networks are only scanned around this computer's address.
func LocalSubnets() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			base := net.IPv4(ip4[0], ip4[1], ip4[2], 0).To4()
			if !seen[base.String()] {
				seen[base.String()] = true
				out = append(out, base)
			}
		}
	}
	return out
}

// Hosts24 lists x.y.z.1 … x.y.z.254 for each base.
func Hosts24(bases []net.IP) []string {
	var hosts []string
	for _, b := range bases {
		b4 := b.To4()
		if b4 == nil {
			continue
		}
		for i := 1; i < 255; i++ {
			hosts = append(hosts, net.IPv4(b4[0], b4[1], b4[2], byte(i)).String())
		}
	}
	return hosts
}

// Scan queries /info on every host with at most `parallel` requests at a
// time and returns the speakers that answered, sorted by name. found is
// called for each speaker as soon as it answers (may be nil).
func (c *Client) Scan(ctx context.Context, hosts []string, parallel int, found func(Speaker)) []Speaker {
	if parallel < 1 {
		parallel = 1
	}
	var (
		mu  sync.Mutex
		res []Speaker
		wg  sync.WaitGroup
		sem = make(chan struct{}, parallel)
	)
	for _, h := range hosts {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				defer func() { <-sem }()
				s, err := c.Info(ctx, h)
				if err != nil {
					return
				}
				mu.Lock()
				res = append(res, s)
				mu.Unlock()
				if found != nil {
					found(s)
				}
			}(h)
		}
	}
	wg.Wait()
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}
