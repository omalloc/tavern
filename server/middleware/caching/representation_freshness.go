package caching

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/cachecontrol"
)

var errRepresentationPolicy = errors.New("response is not eligible for representation storage")

func saturatedAge(age, elapsed int64) int64 {
	if age > math.MaxInt64-elapsed {
		return math.MaxInt64
	}
	return age + elapsed
}

// representationFreshness normalizes the stored Date/Age and subtracts the
// origin's current age from its lifetime. All time comes from the injected
// repository clock, including Expires and response-delay accounting.
func representationFreshness(header http.Header, started, received time.Time) (int64, error) {
	control := cachecontrol.Parse(strings.Join(header.Values("Cache-Control"), ","))
	private, _ := control.Private()
	noCache, _ := control.NoCache()
	if control.NoStore() || private || noCache {
		return 0, errRepresentationPolicy
	}
	var lifetime int64
	seconds := func(value string) (int64, error) {
		n, err := strconv.ParseInt(strings.Trim(value, `"`), 10, 64)
		if err != nil || n < 0 || n > int64(math.MaxInt64/int64(time.Second)) {
			return 0, errRepresentationPolicy
		}
		return n, nil
	}
	if value := header.Get(protocol.ProtocolCacheTime); value != "" {
		n, err := seconds(value)
		if err != nil || n == 0 {
			return 0, errRepresentationPolicy
		}
		lifetime = n
	} else if value, ok := control["s-maxage"]; ok {
		n, err := seconds(value)
		if err != nil {
			return 0, err
		}
		lifetime = n
	} else if value, ok := control["max-age"]; ok {
		n, err := seconds(value)
		if err != nil {
			return 0, err
		}
		lifetime = n
	} else if value := header.Get("Expires"); value != "" {
		expires, err := http.ParseTime(value)
		if err != nil {
			return 0, errRepresentationPolicy
		}
		date := received
		if value := header.Get("Date"); value != "" {
			date, err = http.ParseTime(value)
			if err != nil {
				return 0, errRepresentationPolicy
			}
		}
		lifetime = max(int64(0), expires.Unix()-date.Unix())
	} else if len(control) == 0 {
		lifetime = int64(xhttp.DefaultProtocolCacheTime / time.Second)
	} else {
		return 0, errRepresentationPolicy
	}
	date := received
	if value := header.Get("Date"); value != "" {
		var err error
		date, err = http.ParseTime(value)
		if err != nil {
			return 0, errRepresentationPolicy
		}
	} else {
		header.Set("Date", date.UTC().Format(http.TimeFormat))
	}
	age := int64(0)
	if value := header.Get("Age"); value != "" {
		var err error
		age, err = strconv.ParseInt(value, 10, 64)
		if err != nil || age < 0 || len(header.Values("Age")) != 1 {
			return 0, errRepresentationPolicy
		}
	}
	delay := max(int64(0), received.Unix()-started.Unix())
	if age > math.MaxInt64-delay {
		age = math.MaxInt64
	} else {
		age += delay
	}
	age = max(age, received.Unix()-date.Unix())
	header.Set("Age", strconv.FormatInt(age, 10))
	return received.Unix() + max(int64(0), lifetime-age), nil
}

func opaqueETag(value string) (string, bool) {
	value = strings.TrimPrefix(value, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	for i := 1; i < len(value)-1; i++ {
		if value[i] < 0x21 || value[i] == '"' || value[i] == 0x7f {
			return "", false
		}
	}
	return value, true
}
