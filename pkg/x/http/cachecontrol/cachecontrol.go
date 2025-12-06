// Copyright Marc Weistroff
// Copyright Fabien Potencier
// Parts of this code comes from the "Symfony HttpFoundation" source code.
// Use of this source code is governed by a MIT license.

// Package cachecontrol provides HTTP Cache-Control header parsing with some
// utility functions to quickly deal with directives values.
package cachecontrol

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var rx = regexp.MustCompile(`([a-zA-Z][a-zA-Z_-]*)\s*(?:=(?:"([^"]*)"|([^ \t",;]*)))?`)

// CacheControl holds Cache-Control directives and gives a few utility methods to quickly deal
// with directives values
type CacheControl map[string]string

func (c CacheControl) Public() bool {
	_, ok := c["public"]
	return ok
}

func (c CacheControl) NoStore() bool {
	_, ok := c["no-store"]
	return ok
}

func (c CacheControl) NoTransform() bool {
	_, ok := c["no-transform"]
	return ok
}

func (c CacheControl) OnlyIfCached() bool {
	_, ok := c["only-if-cached"]
	return ok
}

func (c CacheControl) MaxAge() time.Duration {
	return c.timedDirective("max-age")
}

func (c CacheControl) Private() (bool, string) {
	str, ok := c["private"]
	return ok, str
}

func (c CacheControl) NoCache() (bool, string) {
	str, ok := c["no-cache"]
	return ok, str
}

func (c CacheControl) MustRevalidate() bool {
	_, ok := c["must-revalidate"]
	return ok
}

func (c CacheControl) ProxyRevalidate() bool {
	_, ok := c["proxy-revalidate"]
	return ok
}

func (c CacheControl) MinFresh() time.Duration {
	return c.timedDirective("min-fresh")
}

// MaxStale returns -1 if the directive wasn't present or if an error happened
// during parsing the value. It returns math.MaxInt64 if it was present but if
// no value was provided. Otherwise, it returns the provided duration
func (c CacheControl) MaxStale() time.Duration {
	t, ok := c["max-stale"]
	if !ok {
		return -1
	}
	if t == "" {
		return math.MaxInt64
	}

	i, err := strconv.Atoi(t)
	if err != nil {
		return -1
	}
	return time.Duration(i) * time.Second
}

func (c CacheControl) timedDirective(key string) time.Duration {
	t, ok := c[key]
	if !ok {
		return -1
	}

	i, err := strconv.Atoi(t)
	if err != nil {
		return -1
	}
	return time.Duration(i) * time.Second
}

func (c CacheControl) Cacheable() bool {
	if c.NoStore() {
		return false
	}

	if ok, _ := c.NoCache(); ok {
		return false
	}

	if ok, _ := c.Private(); ok {
		return false
	}

	if c.MustRevalidate() {
		return false
	}

	if c.ProxyRevalidate() {
		return false
	}

	if c.MaxAge() == -1 {
		return false
	}

	return true
}

// Parse parses the cache directive of a Cache-Control header and returns a CacheControl
func Parse(directive string) CacheControl {
	cc := CacheControl{}
	matches := rx.FindAllString(directive, -1)

	for _, match := range matches {
		var key, value string
		key = match
		if index := strings.Index(match, "="); index != -1 {
			key, value = match[:index], match[index+1:]
		}
		cc[strings.ToLower(key)] = strings.TrimSpace(value)
	}

	return cc
}
