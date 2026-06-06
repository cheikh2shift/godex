package rxcache

import (
	"regexp"
	"sync"
)

func MustCompile(pattern string) *regexp.Regexp      { return globalCache.MustCompile(pattern) }
func Compile(pattern string) (*regexp.Regexp, error) { return globalCache.Compile(pattern) }

const globalCacheLimit = 1000

var globalCache = RegexpCache{m: make(map[string]*regexp.Regexp, globalCacheLimit), limit: globalCacheLimit}

type RegexpCache struct {
	m     map[string]*regexp.Regexp
	limit int
	mu    sync.RWMutex
}

func (c *RegexpCache) MustCompile(pattern string) *regexp.Regexp {
	r, err := c.Compile(pattern)
	if err != nil {
		panic(err)
	}
	return r
}

func (c *RegexpCache) Compile(pattern string) (*regexp.Regexp, error) {
	c.mu.RLock()
	r := c.m[pattern]
	c.mu.RUnlock()
	if r != nil {
		return r, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r = c.m[pattern]; r != nil {
		return r, nil
	}
	if n := len(c.m) - c.limit; c.limit > 0 && n > 0 {
		for k := range c.m {
			delete(c.m, k)
			n--
			if n <= 0 {
				break
			}
		}
	}
	r, err := regexp.Compile(pattern)
	if r != nil {
		c.m[pattern] = r
	}
	return r, err
}
