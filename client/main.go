package client

import (
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"

	"github.com/dhcmrlchtdj/dns/config"
)

///

type dnsClient func(string, uint16) []Answer

type DNSClient struct {
	cache      sync.Map // MAP("domain|type") => dnsCached
	router     dnsRouter
	staticIpV4 map[string]string
	staticIpV6 map[string]string
}

///

func (c *DNSClient) Init(forwards []config.Server) {
	for _, forward := range forwards {
		parsed, err := url.Parse(forward.DNS)
		if err != nil {
			log.Error().Str("module", "client").Str("dns", forward.DNS).Msg("invalid config")
			panic(err)
		}
		var cli dnsClient
		switch parsed.Scheme {
		case "ipv4":
			if c.staticIpV4 == nil {
				c.staticIpV4 = make(map[string]string)
			}
			for _, domain := range forward.Domain {
				c.staticIpV4[dns.Fqdn(domain)] = parsed.Host
			}
			continue
		case "ipv6":
			if c.staticIpV6 == nil {
				c.staticIpV6 = make(map[string]string)
			}
			for _, domain := range forward.Domain {
				c.staticIpV6[dns.Fqdn(domain)] = parsed.Host
			}
			continue
		case "udp":
			cli = GetUDPClient(parsed.Host)
		case "doh":
			parsed.Scheme = "https"
			cli = GetDoHClient(parsed.String(), forward.HttpsProxy)
		case "tcp", "dot":
			log.Error().Str("module", "client").Str("dns", forward.DNS).Msg("WIP")
			continue
		default:
			log.Error().Str("module", "client").Str("dns", forward.DNS).Msg("unsupported scheme")
			continue
		}

		for _, domain := range forward.Domain {
			c.router.add(dns.Fqdn(domain), cli)
		}
	}
}

///

func (c *DNSClient) Query(name string, qtype uint16) []Answer {
	log.Info().Str("module", "client").Str("domain", name).Uint16("type", qtype).Msg("query")

	name = dns.Fqdn(name)

	// from staticIp
	if qtype == dns.TypeA {
		staticIp, found := c.staticIpV4[name]
		if found {
			log.Debug().Str("module", "client").Str("domain", name).Uint16("type", qtype).Msg("staticIpV4 hit")
			return []Answer{{Name: name, Type: qtype, TTL: 60, Data: staticIp}}
		}
	} else if qtype == dns.TypeAAAA {
		staticIp, found := c.staticIpV6[name]
		if found {
			log.Debug().Str("module", "client").Str("domain", name).Uint16("type", qtype).Msg("staticIpV6 hit")
			return []Answer{{Name: name, Type: qtype, TTL: 60, Data: staticIp}}
		}
	}

	cacheKey := name + "|" + strconv.Itoa(int(qtype))

	// from cache
	cached, found := c.cacheGet(cacheKey)
	if found {
		log.Debug().Str("module", "client").Str("domain", name).Uint16("type", qtype).Msg("cache hit")
		return cached
	}

	// by config
	cli := c.router.route(name)
	if cli == nil {
		log.Debug().Str("module", "client").Str("domain", name).Uint16("type", qtype).Msg("not found")
		return nil
	}
	ans := cli(name, qtype)
	c.cacheSet(cacheKey, ans)
	return ans
}

///

type dnsCached struct {
	answer  []Answer
	expired time.Time
}

func (c *DNSClient) cacheSet(key string, answer []Answer) {
	if len(answer) == 0 {
		return
	}

	minTTL := answer[0].TTL
	for _, ans := range answer {
		if ans.TTL < minTTL {
			minTTL = ans.TTL
		}
	}

	val := dnsCached{
		answer:  answer,
		expired: time.Now().Add(time.Duration(minTTL) * time.Second),
	}
	c.cache.Store(key, &val)
}

func (c *DNSClient) cacheGet(key string) ([]Answer, bool) {
	val, found := c.cache.Load(key)
	if !found {
		return nil, false
	}

	cached, ok := val.(*dnsCached)
	if !ok {
		c.cache.Delete(key)
		return nil, false
	}

	elapsed := cached.expired.Sub(time.Now())
	ttl := int(math.Ceil(elapsed.Seconds()))
	if ttl <= 0 {
		log.Debug().Str("module", "client.cache").Str("key", key).Msg("expired")
		c.cache.Delete(key)
		return nil, false
	}

	for idx := range cached.answer {
		cached.answer[idx].TTL = ttl
	}

	return cached.answer, true
}

///

type Answer struct {
	// The record owner.
	Name string `json:"name"`
	// The type of DNS record.
	Type uint16 `json:"type"`
	// The number of seconds the answer can be stored in cache before it is considered stale.
	TTL int `json:"TTL"`
	// The value of the DNS record for the given name and type.
	Data string `json:"data"`
}
