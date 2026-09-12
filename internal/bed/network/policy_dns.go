package network

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type dnsAddress struct {
	IP  netip.Addr
	TTL uint32
}

func refusedDNS(query dnsmessage.Message) ([]byte, error) {
	query.Header.Response = true
	query.Header.RCode = dnsmessage.RCodeRefused
	query.Answers, query.Authorities, query.Additionals = nil, nil, nil
	return query.Pack()
}

func (p *policyControl) exchange(ctx context.Context, payload []byte, upstream func() ([]byte, error)) ([]byte, error) {
	var query dnsmessage.Message
	if err := query.Unpack(payload); err != nil {
		return nil, err
	}
	if query.Header.Response || len(query.Questions) != 1 {
		return nil, fmt.Errorf("network: expected one DNS question")
	}
	p.mu.Lock()
	revision := p.revision
	allowed := !p.closed && p.current.domainAction(query.Questions[0].Name.String()) == "allow"
	p.mu.Unlock()
	if !allowed {
		return refusedDNS(query)
	}
	data, err := upstream()
	if err != nil {
		return nil, err
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(data); err != nil {
		return nil, err
	}
	if !answer.Header.Response || answer.Header.ID != query.Header.ID || len(answer.Questions) != 1 || answer.Questions[0] != query.Questions[0] {
		return nil, fmt.Errorf("network: mismatched DNS response")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.revision != revision {
		return refusedDNS(query)
	}
	var addresses []dnsAddress
	for _, record := range answer.Answers {
		// Deny aliases too; an allowed hostname cannot redirect DNS to a denied one.
		if cname, ok := record.Body.(*dnsmessage.CNAMEResource); ok && p.current.domainAction(cname.CNAME.String()) == "deny" {
			// A default-deny policy may intentionally allow a name whose CDN alias is
			// not listed. Only an explicit deny rule blocks its CNAME target.
			explicit := p.current
			explicit.DefaultAction = "allow"
			if explicit.domainAction(cname.CNAME.String()) == "deny" {
				return refusedDNS(query)
			}
		}
		var ip netip.Addr
		switch body := record.Body.(type) {
		case *dnsmessage.AResource:
			ip = netip.AddrFrom4(body.A)
		case *dnsmessage.AAAAResource:
			ip = netip.AddrFrom16(body.AAAA).Unmap()
		}
		if !ip.IsValid() {
			continue
		}
		if p.current.deniesIP(ip) {
			return refusedDNS(query)
		}
		ttl := min(record.Header.TTL, 300)
		if ttl == 0 {
			ttl = 1
		}
		addresses = append(addresses, dnsAddress{IP: ip, TTL: ttl})
	}
	if p.current.DefaultAction == "deny" && len(addresses) > 0 {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		// Publish DNS only after the kernel accepted its bounded, expiring IP set.
		now := time.Now()
		learned := make(map[netip.Addr]time.Time, len(p.learned)+len(addresses))
		for ip, expires := range p.learned {
			if expires.After(now) {
				learned[ip] = expires
			}
		}
		for _, address := range addresses {
			learned[address.IP] = now.Add(time.Duration(address.TTL) * time.Second)
		}
		if len(learned) > 4096 {
			return nil, fmt.Errorf("network: DNS policy address limit reached")
		}
		entries := make([]dnsAddress, 0, len(learned))
		for ip, expires := range learned {
			entries = append(entries, dnsAddress{IP: ip, TTL: uint32(max(1, expires.Sub(now).Seconds()))})
		}
		if err := p.learn(ctx, entries); err != nil {
			return nil, err
		}
		p.learned = learned
	}
	return data, nil
}
